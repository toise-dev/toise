package store

import (
	"context"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// TestCoalesceKeepsHorizonBaseline: the horizon baseline PruneOlderThan mints is
// an entity.unchanged record appended after the entity's latest heartbeat, so it
// sits inside a heartbeat run. Coalescing must not drop it as a run middle, or
// the as-of fold loses a still-live entity between the horizon and its oldest
// surviving heartbeat, and no later prune re-mints the baseline.
func TestCoalesceKeepsHorizonBaseline(t *testing.T) {
	s := newTestStore(t)
	id := model.EntityID("h")

	evs := []model.Event{
		mkEntityEvent(id, model.EntityCreated, ts(0)),
		mkEntityEvent(id, model.EntityUnchanged, ts(100)),
		mkEntityEvent(id, model.EntityUnchanged, ts(200)),
		mkEntityEvent(id, model.EntityUnchanged, ts(1000)),
	}
	if err := s.Append(evs...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, _, err := s.PruneOlderThan(ts(500)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if err := s.Append(
		mkEntityEvent(id, model.EntityUnchanged, ts(1100)),
		mkEntityEvent(id, model.EntityUnchanged, ts(1200)),
		mkEntityEvent(id, model.EntityUnchanged, ts(1300)),
	); err != nil {
		t.Fatalf("append after prune: %v", err)
	}

	removed, err := s.CoalesceHeartbeats()
	if err != nil {
		t.Fatalf("coalesce: %v", err)
	}
	if removed == 0 {
		t.Fatal("coalesce removed nothing; test did not exercise coalescing")
	}

	gr, err := projection.At(context.Background(), s, ts(600))
	if err != nil {
		t.Fatalf("as-of fold at ts(600): %v", err)
	}
	if _, ok, deleted := gr.GetEntity(id); !ok || deleted {
		t.Fatalf("entity %q missing or deleted at ts(600): ok=%v deleted=%v", id, ok, deleted)
	}

	gr, err = projection.At(context.Background(), s, ts(1250))
	if err != nil {
		t.Fatalf("as-of fold at ts(1250): %v", err)
	}
	if _, ok, deleted := gr.GetEntity(id); !ok || deleted {
		t.Fatalf("entity %q missing or deleted at ts(1250): ok=%v deleted=%v", id, ok, deleted)
	}
}

func TestCoalescedEntityKeepsBaselineAcrossPrunes(t *testing.T) {
	s := newTestStore(t)
	id := model.EntityID("h")

	evs := []model.Event{
		mkEntityEvent(id, model.EntityCreated, ts(0)),
		mkEntityEvent(id, model.EntityUnchanged, ts(100)),
		mkEntityEvent(id, model.EntityUnchanged, ts(200)),
		mkEntityEvent(id, model.EntityUnchanged, ts(1000)),
	}
	if err := s.Append(evs...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, _, err := s.PruneOlderThan(ts(500)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if err := s.Append(
		mkEntityEvent(id, model.EntityUnchanged, ts(1100)),
		mkEntityEvent(id, model.EntityUnchanged, ts(1200)),
		mkEntityEvent(id, model.EntityUnchanged, ts(1300)),
	); err != nil {
		t.Fatalf("append after prune: %v", err)
	}
	if _, err := s.CoalesceHeartbeats(); err != nil {
		t.Fatalf("coalesce: %v", err)
	}
	if gr, err := projection.At(context.Background(), s, ts(600)); err != nil {
		t.Fatalf("as-of fold at ts(600): %v", err)
	} else if _, ok, deleted := gr.GetEntity(id); !ok || deleted {
		t.Fatalf("entity %q missing or deleted at ts(600) after the first coalesce", id)
	}

	if err := s.Append(mkEntityEvent(id, model.EntityUnchanged, ts(2100))); err != nil {
		t.Fatalf("append ts(2100): %v", err)
	}
	if _, _, err := s.PruneOlderThan(ts(1500)); err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if _, err := s.CoalesceHeartbeats(); err != nil {
		t.Fatalf("second coalesce: %v", err)
	}
	if err := s.Append(
		mkEntityEvent(id, model.EntityUnchanged, ts(2200)),
		mkEntityEvent(id, model.EntityUnchanged, ts(2300)),
	); err != nil {
		t.Fatalf("append after second prune: %v", err)
	}
	if _, err := s.CoalesceHeartbeats(); err != nil {
		t.Fatalf("third coalesce: %v", err)
	}

	gr, err := projection.At(context.Background(), s, ts(1600))
	if err != nil {
		t.Fatalf("as-of fold at ts(1600): %v", err)
	}
	if _, ok, deleted := gr.GetEntity(id); !ok || deleted {
		t.Fatalf("entity %q missing or deleted at ts(1600): ok=%v deleted=%v", id, ok, deleted)
	}
}
