package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"

	"github.com/toise-dev/toise/internal/model"
	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// snapshotKey holds the latest projection snapshot inside Pebble, so a consistent
// store copy (Checkpoint) includes it. The value is: an 8-byte big-endian
// reference sequence, then length-delimited (uint32 + bytes) marshaled events,
// then optionally the livenessSentinel length prefix followed by one
// length-delimited opaque liveness blob (#139). Snapshots written before the
// liveness section simply end after the events and read fine.
var snapshotKey = []byte("meta/snapshot")

// livenessSentinel is an impossible event length marking the liveness section.
const livenessSentinel = uint32(0xFFFFFFFF)

// WriteSnapshot persists a projection snapshot: the reference sequence (events at
// or before it are reflected in the snapshot) and the synthetic events that
// reconstruct the live graph. On restart the snapshot is applied, then only events
// after seq are replayed — bounding restart time by snapshot age, not history (#45
// /#49). seq should be read BEFORE the graph is sampled, so the replayed tail
// overlaps rather than skips (re-applying a create/add is idempotent).
func (s *Store) WriteSnapshot(seq uint64, events []model.Event, liveness []byte) error {
	var buf bytes.Buffer
	buf.Write(encodeU64(seq))
	var lenbuf [4]byte
	for i := range events {
		b, err := proto.Marshal(events[i].ToProto())
		if err != nil {
			return fmt.Errorf("marshaling snapshot event %d: %w", i, err)
		}
		binary.BigEndian.PutUint32(lenbuf[:], uint32(len(b)))
		buf.Write(lenbuf[:])
		buf.Write(b)
	}
	if len(liveness) > 0 {
		binary.BigEndian.PutUint32(lenbuf[:], livenessSentinel)
		buf.Write(lenbuf[:])
		binary.BigEndian.PutUint32(lenbuf[:], uint32(len(liveness)))
		buf.Write(lenbuf[:])
		buf.Write(liveness)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.db.Set(snapshotKey, buf.Bytes(), pebble.Sync); err != nil {
		return fmt.Errorf("writing snapshot: %w", err)
	}
	s.snapshotSeq = seq
	s.snapshotsWritten++
	return nil
}

// ReadSnapshot returns the stored snapshot's reference sequence and events. ok is
// false when no snapshot exists yet.
func (s *Store) ReadSnapshot() (seq uint64, events []model.Event, liveness []byte, ok bool, err error) {
	v, closer, gerr := s.db.Get(snapshotKey)
	if errors.Is(gerr, pebble.ErrNotFound) {
		return 0, nil, nil, false, nil
	}
	if gerr != nil {
		return 0, nil, nil, false, fmt.Errorf("reading snapshot: %w", gerr)
	}
	data := make([]byte, len(v))
	copy(data, v)
	_ = closer.Close()

	if len(data) < 8 {
		return 0, nil, nil, false, fmt.Errorf("snapshot too short: %d bytes", len(data))
	}
	seq = binary.BigEndian.Uint64(data[:8])
	rest := data[8:]
	for len(rest) > 0 {
		if len(rest) < 4 {
			return 0, nil, nil, false, errors.New("snapshot truncated (length prefix)")
		}
		n := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if n == livenessSentinel {
			// The liveness section: one length-delimited opaque blob, owned by
			// the change engine (#139). Pre-section snapshots never reach here.
			if len(rest) < 4 {
				return 0, nil, nil, false, errors.New("snapshot truncated (liveness length)")
			}
			ln := binary.BigEndian.Uint32(rest[:4])
			rest = rest[4:]
			if uint32(len(rest)) < ln {
				return 0, nil, nil, false, errors.New("snapshot truncated (liveness body)")
			}
			liveness = rest[:ln]
			rest = rest[ln:]
			continue
		}
		if uint32(len(rest)) < n {
			return 0, nil, nil, false, errors.New("snapshot truncated (event body)")
		}
		var pe toisev1.Event
		if uerr := proto.Unmarshal(rest[:n], &pe); uerr != nil {
			return 0, nil, nil, false, fmt.Errorf("decoding snapshot event: %w", uerr)
		}
		events = append(events, model.EventFromProto(&pe))
		rest = rest[n:]
	}
	return seq, events, liveness, true, nil
}

// ScanFrom visits events whose sequence is strictly greater than afterSeq, in
// append order. ScanFrom(0, fn) is equivalent to Scan. Used to replay the tail
// after applying a snapshot.
func (s *Store) ScanFrom(afterSeq uint64, fn func(seq uint64, ev model.Event) error) error {
	lower := primaryKey(afterSeq + 1)
	upper := prefixUpperBound([]byte(primaryPrefix))
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return fmt.Errorf("opening tail iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()
	for iter.First(); iter.Valid(); iter.Next() {
		seq := seqFromKeySuffix(iter.Key())
		var pe toisev1.Event
		if uerr := proto.Unmarshal(iter.Value(), &pe); uerr != nil {
			return fmt.Errorf("decoding seq %d: %w", seq, uerr)
		}
		if ferr := fn(seq, model.EventFromProto(&pe)); ferr != nil {
			return ferr
		}
	}
	return iter.Error()
}

// Checkpoint writes a consistent copy of the store into destDir (which must not
// exist) using Pebble's checkpoint — a live, lock-free snapshot suitable for
// backup. Restore by pointing --data-dir at a copy of destDir. (#49)
func (s *Store) Checkpoint(destDir string) error {
	if err := s.db.Checkpoint(destDir); err != nil {
		return fmt.Errorf("checkpointing store to %s: %w", destDir, err)
	}
	return nil
}

// SnapshotSeq and SnapshotsWritten back the snapshot metrics.
func (s *Store) SnapshotSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotSeq
}

func (s *Store) SnapshotsWritten() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotsWritten
}
