package store

import (
	"testing"

	"github.com/cockroachdb/pebble"

	"github.com/toise-dev/toise/internal/model"
)

// TestDropSnapshot: dropping the snapshot leaves the log untouched but makes the
// next read find none, and resets the snapshot_seq metric (#166 P1).
func TestDropSnapshot(t *testing.T) {
	s := newTestStore(t)
	events := []model.Event{mkEntityEvent("a", model.EntityCreated, ts(0))}
	if err := s.WriteSnapshot(7, events, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := s.ReadSnapshot(); err != nil || !ok {
		t.Fatalf("precondition: ReadSnapshot ok=%v err=%v, want true/<nil>", ok, err)
	}
	if err := s.DropSnapshot(); err != nil {
		t.Fatalf("DropSnapshot: %v", err)
	}
	if _, _, _, ok, err := s.ReadSnapshot(); err != nil || ok {
		t.Fatalf("after drop: ReadSnapshot ok=%v err=%v, want false/<nil>", ok, err)
	}
	if seq := s.SnapshotSeq(); seq != 0 {
		t.Errorf("SnapshotSeq after drop = %d, want 0", seq)
	}
}

// TestReadSnapshotRejectsCorruptHeader: a snapshot too short to hold the 8-byte
// reference sequence is an error — the condition the boot path tolerates by
// falling back to a full replay instead of failing to start (#166 P1).
func TestReadSnapshotRejectsCorruptHeader(t *testing.T) {
	s := newTestStore(t)
	if err := s.db.Set(snapshotKey, []byte{0x01, 0x02, 0x03}, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := s.ReadSnapshot(); err == nil || ok {
		t.Fatalf("ReadSnapshot on a 3-byte snapshot: ok=%v err=%v, want false/error", ok, err)
	}
}

// TestReadSnapshotTruncatedLivenessSection pins #164: a torn write that
// truncates the liveness section must not fail ReadSnapshot — the projection
// events before it are intact, and the blob is only a hint the sweep
// self-heals without. The partial bytes are returned so the engine's decoder
// rejects them on the same path as JSON-level corruption.
func TestReadSnapshotTruncatedLivenessSection(t *testing.T) {
	blob := []byte(`{"refs":{"a":{"p1":"2026-01-01T00:00:00Z"}}}`)
	events := []model.Event{mkEntityEvent("a", model.EntityCreated, ts(0))}
	for name, drop := range map[string]int{
		"mid blob body":     5,
		"mid length prefix": len(blob) + 2,
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.WriteSnapshot(1, events, blob); err != nil {
				t.Fatal(err)
			}
			v, closer, gerr := s.db.Get(snapshotKey)
			if gerr != nil {
				t.Fatal(gerr)
			}
			full := append([]byte(nil), v...)
			_ = closer.Close()
			if err := s.db.Set(snapshotKey, full[:len(full)-drop], pebble.Sync); err != nil {
				t.Fatal(err)
			}

			seq, evs, liveness, ok, err := s.ReadSnapshot()
			if err != nil || !ok {
				t.Fatalf("ReadSnapshot on truncated liveness: ok=%v err=%v, want true/<nil>", ok, err)
			}
			if seq != 1 || len(evs) != 1 {
				t.Fatalf("seq=%d events=%d, want 1/1", seq, len(evs))
			}
			if string(liveness) == string(blob) {
				t.Fatal("liveness should come back partial after truncation, not whole")
			}
		})
	}
}
