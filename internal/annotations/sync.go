package annotations

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// ObjectStore is the slice of a shared object store the sync needs. It is
// satisfied by logship.Sink — annotations ride the same store the resilience
// pillar already requires, so a cluster gains shared annotations with zero new
// configuration and zero node-to-node coupling (#348, ADR 0029: nodes never
// talk to each other; they meet at the object store).
type ObjectStore interface {
	Put(ctx context.Context, name string, data []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
}

// SyncOnce reconciles one tenant's annotation sidecar with the shared store
// under prefix, both directions, last-writer-wins by UpdatedAt.
//
// Shape and its consequences, stated rather than discovered:
//
//   - Rows are keyed remotely by the hex of their local key, so any key —
//     identity fingerprints and pre-migration ids alike — round-trips through
//     any backend's name rules.
//   - Tombstones travel like values: a removal is a row with empty values and
//     a fresh timestamp, so it WINS over the stale value on the other node
//     instead of being resurrected by it.
//   - Last-writer-wins assumes wall clocks agree to within the write cadence
//     of two operators editing the SAME annotation on DIFFERENT nodes — a rare
//     race for operator notes, and the loser is a full row, never a corrupted
//     merge. NTP-synced fleets are the assumption, as everywhere else here.
//   - Volumes are operator notes, not telemetry: the full sweep each cycle is
//     deliberate simplicity. If annotations ever number in the thousands, add
//     a changed-since cursor then, with the measurement in hand.
func SyncOnce(ctx context.Context, s *Store, remote ObjectStore, prefix string) (pushed, pulled int, err error) {
	// Pull first: applying newer remote rows before pushing means a row that
	// lost last-writer-wins is corrected locally instead of clobbering the
	// winner remotely on this same pass.
	names, err := remote.List(ctx, prefix)
	if err != nil {
		return 0, 0, fmt.Errorf("listing shared annotations under %s: %w", prefix, err)
	}
	remoteSeen := make(map[string]Annotation, len(names))
	for _, name := range names {
		key, derr := keyFromName(prefix, name)
		if derr != nil {
			// A foreign object under our prefix: skip it rather than fail the
			// sync an unrelated write could then wedge forever.
			continue
		}
		blob, gerr := remote.Get(ctx, name)
		if gerr != nil {
			return pushed, pulled, fmt.Errorf("reading shared annotation %s: %w", name, gerr)
		}
		var ra Annotation
		if uerr := json.Unmarshal(blob, &ra); uerr != nil {
			return pushed, pulled, fmt.Errorf("decoding shared annotation %s: %w", name, uerr)
		}
		remoteSeen[key] = ra
		la, lok, lerr := s.rawGet(key)
		if lerr != nil {
			return pushed, pulled, lerr
		}
		if !lok || ra.UpdatedAt.After(la.UpdatedAt) {
			if aerr := s.Apply(key, ra); aerr != nil {
				return pushed, pulled, aerr
			}
			pulled++
		}
	}

	// Push what is locally newer than (or absent from) the shared store.
	err = s.Scan(func(key string, la Annotation) error {
		if ra, ok := remoteSeen[key]; ok && !la.UpdatedAt.After(ra.UpdatedAt) {
			return nil
		}
		blob, merr := json.Marshal(la)
		if merr != nil {
			return fmt.Errorf("encoding annotation %s: %w", key, merr)
		}
		if perr := remote.Put(ctx, nameFor(prefix, key), blob); perr != nil {
			return fmt.Errorf("pushing annotation %s: %w", key, perr)
		}
		pushed++
		return nil
	})
	return pushed, pulled, err
}

// rawGet reads a row without the tombstone-hiding Get applies: sync must
// compare against the tombstone's timestamp, or a deletion loses to the stale
// value it deleted.
func (s *Store) rawGet(id string) (Annotation, bool, error) {
	v, closer, err := s.db.Get([]byte(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return Annotation{}, false, nil
	}
	if err != nil {
		return Annotation{}, false, fmt.Errorf("reading annotation %s: %w", id, err)
	}
	defer func() { _ = closer.Close() }()
	var a Annotation
	if uerr := json.Unmarshal(v, &a); uerr != nil {
		return Annotation{}, false, fmt.Errorf("decoding annotation %s: %w", id, uerr)
	}
	return a, true, nil
}

func nameFor(prefix, key string) string {
	return prefix + "/" + hex.EncodeToString([]byte(key)) + ".ann"
}

func keyFromName(prefix, name string) (string, error) {
	rest := strings.TrimPrefix(name, prefix+"/")
	rest = strings.TrimSuffix(rest, ".ann")
	if rest == name || strings.Contains(rest, "/") {
		return "", fmt.Errorf("not an annotation object: %s", name)
	}
	b, err := hex.DecodeString(rest)
	if err != nil {
		return "", fmt.Errorf("not an annotation object: %s", name)
	}
	return string(b), nil
}
