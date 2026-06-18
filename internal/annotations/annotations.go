// Package annotations is the operator-added overlay on entities: free-form
// key/value notes a human or an assistant attaches to an entity (owner, runbook
// link, ticket, a remark). It is NOT producer truth — it lives in a per-tenant
// Pebble sidecar, separate from the event log, and never enters the projection
// or replay (0.7.0). Annotations key on the entity's logical id, so they survive
// identity changes and re-attach if the entity resurrects (#183).
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

// Get returns the entity's annotation, or ok=false if it has none.
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
	if len(cur.Values) == 0 {
		if derr := s.db.Delete([]byte(id), pebble.Sync); derr != nil {
			return Annotation{}, fmt.Errorf("clearing annotation %s: %w", id, derr)
		}
		return Annotation{Values: map[string]string{}, Author: author, UpdatedAt: now}, nil
	}
	blob, err := json.Marshal(cur)
	if err != nil {
		return Annotation{}, fmt.Errorf("encoding annotation %s: %w", id, err)
	}
	if err := s.db.Set([]byte(id), blob, pebble.Sync); err != nil {
		return Annotation{}, fmt.Errorf("writing annotation %s: %w", id, err)
	}
	return cur, nil
}

// Delete removes all annotations for an entity.
func (s *Store) Delete(id string) error {
	if err := s.db.Delete([]byte(id), pebble.Sync); err != nil {
		return fmt.Errorf("deleting annotation %s: %w", id, err)
	}
	return nil
}
