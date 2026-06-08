package store_test

import (
	"path/filepath"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

func mustOpenStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, oerr := store.Open(dir, store.DefaultConfig())
	if oerr != nil {
		t.Fatalf("open %s: %v", dir, oerr)
	}
	return s
}

// TestSnapshotThenTailEqualsFullReplay proves the #49 acceptance: restoring from a
// snapshot and replaying only the tail yields the same graph as a full replay.
func TestSnapshotThenTailEqualsFullReplay(t *testing.T) {
	s := mustOpenStore(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	add := func(ev model.Event) {
		t.Helper()
		if e := s.Append(ev); e != nil {
			t.Fatalf("append: %v", e)
		}
	}

	a, b := model.EntityID("a"), model.EntityID("b")
	add(entEvt(a, model.EntityCreated, tsx(0)))
	add(entEvt(a, model.EntityAttributeUpdated, tsx(10)))
	add(entEvt(b, model.EntityCreated, tsx(20)))
	add(relEvt(a, b, tsx(30)))

	atSnapshot := projection.New()
	if e := atSnapshot.Replay(s); e != nil {
		t.Fatalf("replay: %v", e)
	}
	seq := s.Sequence()
	if e := s.WriteSnapshot(seq, atSnapshot.SnapshotEvents(tsx(30))); e != nil {
		t.Fatalf("write snapshot: %v", e)
	}

	// The tail, appended after the snapshot.
	add(entEvt(a, model.EntityAttributeUpdated, tsx(40)))
	add(entEvt("c", model.EntityCreated, tsx(50)))

	// Rebuild from snapshot + tail.
	restored := projection.New()
	rseq, evs, ok, rerr := s.ReadSnapshot()
	if rerr != nil || !ok {
		t.Fatalf("ReadSnapshot ok=%v err=%v", ok, rerr)
	}
	if rseq != seq {
		t.Errorf("snapshot seq = %d, want %d", rseq, seq)
	}
	for i := range evs {
		restored.Apply(evs[i])
	}
	if serr := s.ScanFrom(rseq, func(_ uint64, ev model.Event) error {
		restored.Apply(ev)
		return nil
	}); serr != nil {
		t.Fatalf("scan tail: %v", serr)
	}

	full := projection.New()
	if e := full.Replay(s); e != nil {
		t.Fatalf("full replay: %v", e)
	}

	if restored.EntityCount() != full.EntityCount() || restored.RelationCount() != full.RelationCount() {
		t.Errorf("snapshot+tail %d/%d != full replay %d/%d",
			restored.EntityCount(), restored.RelationCount(), full.EntityCount(), full.RelationCount())
	}
	if full.EntityCount() != 3 || full.RelationCount() != 1 {
		t.Errorf("graph = %d/%d, want 3 entities / 1 relation", full.EntityCount(), full.RelationCount())
	}
	if s.SnapshotSeq() != seq || s.SnapshotsWritten() != 1 {
		t.Errorf("snapshot counters: seq=%d written=%d, want %d/1", s.SnapshotSeq(), s.SnapshotsWritten(), seq)
	}
}

// TestCheckpointRestore proves a backup (Pebble checkpoint) restores on a clean
// path and yields the same graph.
func TestCheckpointRestore(t *testing.T) {
	s := mustOpenStore(t, t.TempDir())
	add := func(ev model.Event) {
		t.Helper()
		if e := s.Append(ev); e != nil {
			t.Fatalf("append: %v", e)
		}
	}
	a := model.EntityID("a")
	add(entEvt(a, model.EntityCreated, tsx(0)))
	add(entEvt("b", model.EntityCreated, tsx(10)))
	add(relEvt(a, "b", tsx(20)))

	orig := projection.New()
	if e := orig.Replay(s); e != nil {
		t.Fatalf("replay: %v", e)
	}

	dst := filepath.Join(t.TempDir(), "backup")
	if e := s.Checkpoint(dst); e != nil {
		t.Fatalf("checkpoint: %v", e)
	}
	_ = s.Close()

	restored := mustOpenStore(t, dst)
	t.Cleanup(func() { _ = restored.Close() })
	g := projection.New()
	if e := g.Replay(restored); e != nil {
		t.Fatalf("replay checkpoint: %v", e)
	}
	if g.EntityCount() != orig.EntityCount() || g.RelationCount() != orig.RelationCount() {
		t.Errorf("restored %d/%d != original %d/%d",
			g.EntityCount(), g.RelationCount(), orig.EntityCount(), orig.RelationCount())
	}
}
