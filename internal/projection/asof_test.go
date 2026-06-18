package projection

import (
	"context"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

type timedEvent struct {
	at time.Time
	ev model.Event
}

// fakeTRReader streams its events whose time is in [start, end), like the store's
// ScanByTimeRange, and reports a fixed horizon.
type fakeTRReader struct {
	events  []timedEvent
	horizon time.Time
}

func (r fakeTRReader) PruneHorizon() time.Time { return r.horizon }

func (r fakeTRReader) ScanByTimeRange(_ context.Context, start, end time.Time, fn func(model.Event) error) error {
	for _, te := range r.events {
		if !te.at.Before(start) && te.at.Before(end) {
			if err := fn(te.ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func createdAt(at time.Time, id model.EntityID, hostID string) timedEvent {
	return timedEvent{at: at, ev: model.Event{Entity: &model.EntityEvent{
		ChangeType: model.EntityCreated,
		Entity:     model.Entity{ID: id, Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", hostID)}},
		EventTime:  at,
	}}}
}

// At folds only events at or before the instant.
func TestAtFoldsUpToInstant(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	r := fakeTRReader{events: []timedEvent{
		createdAt(base, "a", "h-a"),
		createdAt(base.Add(time.Hour), "b", "h-b"),
	}}

	g, err := At(context.Background(), r, base) // at the first event only
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.MatchIdentity(model.TypeHost, []model.KeyValue{kv("host.id", "h-a")}); !ok {
		t.Error("h-a should be present at the first instant")
	}
	if _, ok := g.MatchIdentity(model.TypeHost, []model.KeyValue{kv("host.id", "h-b")}); ok {
		t.Error("h-b is in the future of the requested instant; must be absent")
	}

	g, err = At(context.Background(), r, base.Add(2*time.Hour)) // past both
	if err != nil {
		t.Fatal(err)
	}
	if g.EntityCount() != 2 {
		t.Errorf("EntityCount = %d, want 2 (both folded)", g.EntityCount())
	}
}

// At refuses an instant before the retention horizon rather than returning a
// silently partial graph.
func TestAtRefusesBeforeHorizon(t *testing.T) {
	horizon := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	r := fakeTRReader{horizon: horizon}
	if _, err := At(context.Background(), r, horizon.Add(-time.Hour)); err == nil {
		t.Fatal("At before the horizon must error")
	}
	// At or after the horizon is fine.
	if _, err := At(context.Background(), r, horizon); err != nil {
		t.Errorf("At at the horizon must succeed: %v", err)
	}
}
