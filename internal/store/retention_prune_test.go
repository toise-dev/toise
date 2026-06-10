package store

import (
	"context"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// TestPruneOlderThanKeepsLiveTail is the core retention guarantee: events older
// than the cutoff are dropped, EXCEPT the latest event of every still-live entity
// and relation — so a replay rebuilds the same current-state graph (#45).
func TestPruneOlderThanKeepsLiveTail(t *testing.T) {
	s := newTestStore(t)
	a, b, c := model.EntityID("a"), model.EntityID("b"), model.EntityID("c")
	add := func(ev model.Event) {
		t.Helper()
		if err := s.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	add(mkEntityEvent(a, model.EntityCreated, ts(0)))            // old, superseded
	add(mkEntityEvent(a, model.EntityAttributeUpdated, ts(10)))  // old, superseded
	add(mkEntityEvent(b, model.EntityCreated, ts(5)))            // b: created...
	add(mkEntityEvent(b, model.EntityDeleted, ts(15)))           // ...then deleted (dead, all old)
	add(mkEntityEvent(c, model.EntityCreated, ts(50)))           // live, but old (never updated)
	add(mkRelationEvent(a, c, ts(60)))                           // live relation, old
	add(mkEntityEvent(a, model.EntityAttributeUpdated, ts(100))) // a's current state, recent

	ev, by, err := s.PruneOlderThan(ts(90))
	if err != nil {
		t.Fatal(err)
	}
	// Pruned: a@0, a@10, b@5, b@15 (4). Kept: c@50 + rel@60 (live tail, old) and a@100 (recent).
	if ev != 4 {
		t.Errorf("pruned events = %d, want 4", ev)
	}
	if by <= 0 {
		t.Errorf("pruned bytes = %d, want > 0", by)
	}

	got := scanAll(t, s)
	if len(got) != 3 {
		t.Fatalf("after prune: %d events, want 3", len(got))
	}
	live := map[model.EntityID]bool{}
	rels := 0
	for _, e := range got {
		switch {
		case e.Entity != nil:
			live[e.Entity.Entity.ID] = true
			if e.Entity.Entity.ID == a && !e.Entity.EventTime.Equal(ts(100)) {
				t.Errorf("surviving 'a' event at %v, want the latest ts(100)", e.Entity.EventTime)
			}
		case e.Relation != nil:
			rels++
		}
	}
	if !live[a] || !live[c] || live[b] {
		t.Errorf("surviving entities = %v, want a and c (not the deleted b)", live)
	}
	if rels != 1 {
		t.Errorf("surviving relations = %d, want 1 (the live a->c)", rels)
	}
	// b is fully gone, including its index.
	if evs, _ := s.ReadByEntity(context.Background(), b); len(evs) != 0 {
		t.Errorf("ReadByEntity(b) = %d events, want 0 (pruned)", len(evs))
	}
	if s.PrunedEvents() != 4 || s.PrunedBytes() != uint64(by) {
		t.Errorf("counters: events=%d bytes=%d, want 4/%d", s.PrunedEvents(), s.PrunedBytes(), by)
	}
}

func TestPruneOlderThanNoopWhenNothingOld(t *testing.T) {
	s := newTestStore(t)
	if err := s.Append(mkEntityEvent("a", model.EntityCreated, ts(1000))); err != nil {
		t.Fatal(err)
	}
	ev, by, err := s.PruneOlderThan(ts(10))
	if err != nil || ev != 0 || by != 0 {
		t.Errorf("noop prune: ev=%d by=%d err=%v, want 0/0/nil", ev, by, err)
	}
}
