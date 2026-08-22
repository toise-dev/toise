// Package annotations is the operator-added overlay on entities: free-form
// key/value notes a human or an assistant attaches to an entity (owner, runbook
// link, ticket, a remark). It is NOT producer truth — it lives in a per-tenant
// Pebble sidecar, separate from the event log, and never enters the projection
// or replay (0.7.0).
//
// Annotations key on the entity's IDENTITY fingerprint, not on its logical id.
// A logical id is minted per replica, so an id-keyed overlay cannot be shared
// between the nodes of a cluster and is lost when an entity returns after the
// resurrection window with a fresh id. The fingerprint is derived from the
// identifying attributes alone (ADR 0017), so every node computes the same one
// for the same thing. Rows written under the old scheme migrate on first touch;
// see GetAt.
package annotations

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
)

// Annotation is the overlay on one entity: the merged key/values plus who last
// wrote them and when.
type Annotation struct {
	Values    map[string]string `json:"values"`
	Author    string            `json:"author,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Store is a per-tenant key/value sidecar keyed by entity logical id. It is a
// distinct Pebble database, never part of the event log or its replay.
type Store struct{ db *pebble.DB }

// Open opens (or creates) the annotations sidecar at dir.
func Open(dir string) (*Store, error) {
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("opening annotations store at %s: %w", dir, err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// GetAt returns the annotation stored under key, falling back to the legacy
// key and migrating the row across when it is found there.
//
// The overlay used to be keyed by the entity's logical id. That made it
// unshareable: replicas mint their own ids, so the same machine carries a
// different id on each, and a row copied between nodes would attach to nothing
// (#348). It also lost the annotation whenever an entity came back after the
// resurrection window and was minted a fresh id (#344). Keying on the identity
// fingerprint fixes both, because every replica computes the same fingerprint
// for the same identity.
//
// Migration is lazy rather than a startup sweep: a bulk pass would need the
// projection loaded and would have to decide what to do with rows whose id no
// longer resolves. Moving each row the first time it is touched is crash-safe,
// costs one extra read on a miss, and leaves untouched rows readable for as
// long as their id resolves.
func (s *Store) GetAt(key, legacyKey string) (Annotation, bool, error) {
	a, ok, err := s.Get(key)
	if err != nil || ok || legacyKey == "" || legacyKey == key {
		return a, ok, err
	}
	old, ok, err := s.Get(legacyKey)
	if err != nil || !ok {
		return Annotation{}, false, err
	}
	if merr := s.migrate(key, legacyKey, old); merr != nil {
		// The row is readable; failing the read because it could not be moved
		// would hide an annotation an operator wrote. Report it as found.
		return old, true, nil
	}
	return old, true, nil
}

// SetAt merges values under key, first migrating any row still held under the
// legacy key so the merge sees what the operator wrote earlier.
func (s *Store) SetAt(key, legacyKey string, values map[string]string, author string, now time.Time) (Annotation, error) {
	if legacyKey != "" && legacyKey != key {
		if _, _, err := s.GetAt(key, legacyKey); err != nil {
			return Annotation{}, err
		}
	}
	return s.Set(key, values, author, now)
}

// migrate moves a row from the legacy key to the identity key. It writes before
// deleting: a crash between the two leaves a duplicate that the next read
// resolves, whereas the reverse order would lose the annotation.
func (s *Store) migrate(key, legacyKey string, a Annotation) error {
	blob, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encoding annotation for migration to %s: %w", key, err)
	}
	if err := s.db.Set([]byte(key), blob, pebble.Sync); err != nil {
		return fmt.Errorf("migrating annotation to %s: %w", key, err)
	}
	if err := s.db.Delete([]byte(legacyKey), pebble.Sync); err != nil {
		return fmt.Errorf("clearing legacy annotation %s: %w", legacyKey, err)
	}
	return nil
}

// Get returns the annotation stored under the given key, or ok=false if there
// is none. Callers holding an entity should prefer GetAt, which handles the
// legacy key.
func (s *Store) Get(id string) (Annotation, bool, error) {
	v, closer, err := s.db.Get([]byte(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return Annotation{}, false, nil
	}
	if err != nil {
		return Annotation{}, false, fmt.Errorf("reading annotation %s: %w", id, err)
	}
	defer func() { _ = closer.Close() }()
	var a Annotation
	if err := json.Unmarshal(v, &a); err != nil {
		return Annotation{}, false, fmt.Errorf("decoding annotation %s: %w", id, err)
	}
	if len(a.Values) == 0 {
		// A tombstone: the annotation was removed. It reads as absence; only
		// Scan (the sync path) sees it.
		return Annotation{}, false, nil
	}
	return a, true, nil
}

// Set merges values into the entity's annotation (upsert): a key with an empty
// value removes that key; author and the timestamp are recorded. When no values
// remain the row is deleted. Returns the resulting annotation.
func (s *Store) Set(id string, values map[string]string, author string, now time.Time) (Annotation, error) {
	cur, _, err := s.Get(id)
	if err != nil {
		return Annotation{}, err
	}
	if cur.Values == nil {
		cur.Values = map[string]string{}
	}
	for k, v := range values {
		if v == "" {
			delete(cur.Values, k)
		} else {
			cur.Values[k] = v
		}
	}
	cur.Author = author
	cur.UpdatedAt = now
	// A removal writes a TOMBSTONE (empty values, fresh UpdatedAt) rather than
	// deleting the row: reads treat it as absence, but sync can see it — a hard
	// delete is indistinguishable from "never existed", so a removed annotation
	// would resurrect from the shared store on the next pull (#348).
	if len(cur.Values) == 0 {
		cur.Values = map[string]string{}
	}
	if err := s.Apply(id, cur); err != nil {
		return Annotation{}, err
	}
	return cur, nil
}

// Apply writes a row verbatim — values, author and timestamp as given,
// tombstones included. It is the sync path's write: a remote row that won
// last-writer-wins must land exactly as its author wrote it, not be re-merged
// or re-stamped, or two nodes converge to different bytes.
func (s *Store) Apply(id string, a Annotation) error {
	blob, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encoding annotation %s: %w", id, err)
	}
	if err := s.db.Set([]byte(id), blob, pebble.Sync); err != nil {
		return fmt.Errorf("writing annotation %s: %w", id, err)
	}
	return nil
}

// Scan visits every row, tombstones included — the sync path's read. Reads for
// serving go through Get, which hides tombstones.
func (s *Store) Scan(fn func(id string, a Annotation) error) error {
	iter, err := s.db.NewIter(nil)
	if err != nil {
		return fmt.Errorf("opening annotations iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()
	for iter.First(); iter.Valid(); iter.Next() {
		var a Annotation
		if err := json.Unmarshal(iter.Value(), &a); err != nil {
			return fmt.Errorf("decoding annotation %s: %w", iter.Key(), err)
		}
		if err := fn(string(iter.Key()), a); err != nil {
			return err
		}
	}
	return iter.Error()
}

// Delete removes all annotations for an entity.
func (s *Store) Delete(id string) error {
	if err := s.db.Delete([]byte(id), pebble.Sync); err != nil {
		return fmt.Errorf("deleting annotation %s: %w", id, err)
	}
	return nil
}
