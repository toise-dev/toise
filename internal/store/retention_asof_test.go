package store

import (
	"context"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// TestPruneBaselineKeepsAsOfVisible is the as-of keep-set regression: an entity
// created before the cutoff and still live (its only surviving event a recent
// heartbeat, later than the horizon) must remain visible to an as-of fold for
// any instant at or after the horizon. Before horizon baselines, pruning deleted
// its pre-cutoff events and the fold silently dropped it. A sibling created
// AFTER the cutoff must NOT be back-dated: it correctly stays absent until its
// real birth.
func TestPruneBaselineKeepsAsOfVisible(t *testing.T) {
	s := newTestStore(t)
	h, g, n := model.EntityID("h"), model.EntityID("g"), model.EntityID("n")

	mustAppend := func(evs ...model.Event) {
		t.Helper()
		if err := s.Append(evs...); err != nil {
			t.Fatal(err)
		}
	}
	// h and g: born well before the cutoff, only a recent heartbeat survives.
	mustAppend(mkEntityEvent(h, model.EntityCreated, ts(0)))
	mustAppend(mkEntityEvent(g, model.EntityCreated, ts(0)))
	mustAppend(mkRelationEvent(h, g, ts(0)))
	// n: born AFTER the cutoff.
	mustAppend(mkEntityEvent(n, model.EntityCreated, ts(800)))
	// recent heartbeats for the pre-cutoff trio (event_time > cutoff).
	mustAppend(mkEntityEvent(h, model.EntityUnchanged, ts(1000)))
	mustAppend(mkEntityEvent(g, model.EntityUnchanged, ts(1000)))
	mustAppend(mkRelationEvent(h, g, ts(1000)))

	if _, _, err := s.PruneOlderThan(ts(500)); err != nil {
		t.Fatal(err)
	}
	if hz := s.PruneHorizon(); !hz.Equal(ts(500)) {
		t.Fatalf("horizon = %v, want ts(500)", hz)
	}

	present := func(t *testing.T, when int64, id model.EntityID) bool {
		t.Helper()
		gr, err := projection.At(context.Background(), s, ts(when))
		if err != nil {
			t.Fatalf("At(ts(%d)): %v", when, err)
		}
		_, ok, deleted := gr.GetEntity(id)
		return ok && !deleted
	}

	// At the horizon and between it and the latest heartbeat, h and g are visible
	// (via their baselines) while n — born at ts(800) — is not yet.
	for _, when := range []int64{500, 600} {
		if !present(t, when, h) || !present(t, when, g) {
			t.Errorf("At(ts(%d)): h/g missing — the as-of keep-set hole regressed", when)
		}
		if present(t, when, n) {
			t.Errorf("At(ts(%d)): n present, but it was born at ts(800) — baseline must not back-date", when)
		}
	}
	// The relation baseline: the live edge is visible at the horizon too.
	gr, err := projection.At(context.Background(), s, ts(500))
	if err != nil {
		t.Fatal(err)
	}
	if gr.RelationCount() != 1 {
		t.Errorf("At(ts(500)): relations = %d, want 1 (the h->g baseline)", gr.RelationCount())
	}

	// Once past n's birth, all three entities are present.
	if !present(t, 900, n) || !present(t, 900, h) || !present(t, 900, g) {
		t.Error("At(ts(900)): expected h, g and n all present")
	}

	// Before the horizon is still refused outright — the baseline restores the
	// horizon boundary, it does not reach further back.
	if _, err := projection.At(context.Background(), s, ts(499)); err == nil {
		t.Error("At(ts(499)) before the horizon must be refused")
	}
}

// TestPruneBaselineSelfCleans proves horizon baselines do not accumulate: a
// superseded baseline falls out of the keep set and is pruned in the next round,
// leaving exactly one baseline per live entity.
func TestPruneBaselineSelfCleans(t *testing.T) {
	s := newTestStore(t)
	h := model.EntityID("h")
	mustAppend := func(ev model.Event) {
		t.Helper()
		if err := s.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend(mkEntityEvent(h, model.EntityCreated, ts(0)))
	mustAppend(mkEntityEvent(h, model.EntityUnchanged, ts(1000)))

	if _, _, err := s.PruneOlderThan(ts(500)); err != nil { // mints baseline h@500
		t.Fatal(err)
	}
	// A newer heartbeat supersedes both h@1000 and the baseline.
	mustAppend(mkEntityEvent(h, model.EntityUnchanged, ts(2000)))
	if _, _, err := s.PruneOlderThan(ts(1500)); err != nil { // mints h@1500, drops h@500 and h@1000
		t.Fatal(err)
	}

	var eventTimes []int64
	for _, e := range scanAll(t, s) {
		if e.Entity != nil && e.Entity.Entity.ID == h {
			eventTimes = append(eventTimes, e.Entity.EventTime.Unix()-1_700_000_000)
		}
	}
	// Exactly the recent heartbeat and one fresh baseline — the old baseline@500
	// and the superseded heartbeat@1000 are gone.
	if len(eventTimes) != 2 {
		t.Fatalf("h events after two prunes = %v, want exactly 2 (h@2000 + baseline@1500)", eventTimes)
	}
	for _, et := range eventTimes {
		if et == 500 {
			t.Error("the round-1 baseline@500 must have been pruned in round 2 (unbounded growth)")
		}
	}
}
