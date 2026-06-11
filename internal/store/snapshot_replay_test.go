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

	a, b, d := model.EntityID("a"), model.EntityID("b"), model.EntityID("d")
	add(entEvt(a, model.EntityCreated, tsx(0)))
	add(entEvt(a, model.EntityAttributeUpdated, tsx(10)))
	add(entEvt(b, model.EntityCreated, tsx(20)))
	add(relEvt(a, b, tsx(30)))
	add(entEvt(d, model.EntityCreated, tsx(31)))
	add(entEvt(d, model.EntityDeleted, tsx(32)))

	atSnapshot := projection.New()
	if e := atSnapshot.Replay(s); e != nil {
		t.Fatalf("replay: %v", e)
	}
	seq := s.Sequence()
	if e := s.WriteSnapshot(seq, atSnapshot.SnapshotEvents(tsx(30)), nil); e != nil {
		t.Fatalf("write snapshot: %v", e)
	}

	// The tail, appended after the snapshot.
	add(entEvt(a, model.EntityAttributeUpdated, tsx(40)))
	add(entEvt("c", model.EntityCreated, tsx(50)))

	// Rebuild from snapshot + tail.
	restored := projection.New()
	rseq, evs, _, ok, rerr := s.ReadSnapshot()
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
	if _, ok := restored.MatchIdentity(model.TypeHost,
		[]model.KeyValue{{Key: "host.id", Value: model.StringValue("a")}}); !ok {
		t.Error("MatchIdentity(a) missed after snapshot+tail restore")
	}
	if _, ok := restored.MatchIdentity(model.TypeHost,
		[]model.KeyValue{{Key: "host.id", Value: model.StringValue("d")}}); ok {
		t.Error("soft-deleted entity d is matchable after restore: resurrected by the snapshot (#106)")
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

// TestSnapshotRestoreDoesNotResurrectDeleted is the #106 repro: an entity
// deleted before the snapshot must stay deleted after a restore. Its
// EntityDeleted predates the snapshot sequence and is never replayed, so a
// snapshot that emits soft-deleted entities resurrects them — permanently,
// since the restarted engine holds no liveness reference to reap them.
func TestSnapshotRestoreDoesNotResurrectDeleted(t *testing.T) {
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
	add(entEvt(b, model.EntityCreated, tsx(10)))
	add(entEvt(b, model.EntityDeleted, tsx(20)))

	g := projection.New()
	if e := g.Replay(s); e != nil {
		t.Fatalf("replay: %v", e)
	}
	if e := s.WriteSnapshot(s.Sequence(), g.SnapshotEvents(tsx(30)), nil); e != nil {
		t.Fatalf("write snapshot: %v", e)
	}

	restored := projection.New()
	_, evs, _, ok, rerr := s.ReadSnapshot()
	if rerr != nil || !ok {
		t.Fatalf("ReadSnapshot ok=%v err=%v", ok, rerr)
	}
	for i := range evs {
		restored.Apply(evs[i])
	}

	if restored.EntityCount() != 1 {
		t.Errorf("EntityCount = %d after restore, want 1 (b stays deleted)", restored.EntityCount())
	}
	if _, ok := restored.MatchIdentity(model.TypeHost,
		[]model.KeyValue{{Key: "host.id", Value: model.StringValue("b")}}); ok {
		t.Error("deleted entity b is matchable after restore: resurrected")
	}
	if _, found, _ := restored.GetEntity(b); found {
		t.Error("deleted entity b present after restore: the snapshot must omit it")
	}
}

// TestSnapshotLivenessSection pins the #139 format extension: the opaque
// liveness blob rides the snapshot and round-trips; snapshots written without
// one (the pre-#139 format) read back with no liveness and no error.
func TestSnapshotLivenessSection(t *testing.T) {
	s := mustOpenStore(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	a := model.EntityID("a")
	if aerr := s.Append(entEvt(a, model.EntityCreated, tsx(0))); aerr != nil {
		t.Fatal(aerr)
	}
	g := projection.New()
	if rerr := g.Replay(s); rerr != nil {
		t.Fatal(rerr)
	}

	blob := []byte(`{"refs":{"a":{"p1":"2026-06-11T00:00:00Z"}}}`)
	if werr := s.WriteSnapshot(s.Sequence(), g.SnapshotEvents(tsx(10)), blob); werr != nil {
		t.Fatal(werr)
	}
	_, evs, got, ok, err := s.ReadSnapshot()
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	if string(got) != string(blob) {
		t.Fatalf("liveness round-trip = %q, want %q", got, blob)
	}

	// Old format: no liveness section at all.
	if werr := s.WriteSnapshot(s.Sequence(), g.SnapshotEvents(tsx(10)), nil); werr != nil {
		t.Fatal(werr)
	}
	_, _, got, ok, err = s.ReadSnapshot()
	if err != nil || !ok || got != nil {
		t.Fatalf("pre-section snapshot: liveness=%v ok=%v err=%v, want nil/true/nil", got, ok, err)
	}
}
