package projection

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func entCreatedEvent(id string, v string, t time.Time) model.Event {
	return model.Event{Entity: &model.EntityEvent{
		EventID:    model.NewEventID(),
		ChangeType: model.EntityCreated,
		Entity: model.Entity{
			ID:       model.EntityID(id),
			Type:     model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(v)}},
		},
		EventTime:     t,
		RecordedAt:    t,
		SchemaVersion: model.SchemaVersion,
	}}
}

func entDeletedEvent(id string, v string, t time.Time) model.Event {
	return model.Event{Entity: &model.EntityEvent{
		EventID:    model.NewEventID(),
		ChangeType: model.EntityDeleted,
		Entity: model.Entity{
			ID:       model.EntityID(id),
			Type:     model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(v)}},
		},
		EventTime:     t,
		RecordedAt:    t,
		SchemaVersion: model.SchemaVersion,
	}}
}

func e1Identity() []model.KeyValue {
	return []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}
}

func e2Identity() []model.KeyValue {
	return []model.KeyValue{{Key: "host.id", Value: model.StringValue("h2")}}
}

func e3Identity() []model.KeyValue {
	return []model.KeyValue{{Key: "host.id", Value: model.StringValue("h3")}}
}

func e4Identity() []model.KeyValue {
	return []model.KeyValue{{Key: "host.id", Value: model.StringValue("h4")}}
}

func TestSnapshotKeepsTombstone(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	snapAt := t0.Add(10 * time.Minute)

	a := NewWithTombstoneCap(10)
	a.SetTombstoneTTL(time.Hour)
	a.SetClock(func() time.Time { return t0 })
	a.Apply(entCreatedEvent("e1", "h1", t0))
	a.Apply(entCreatedEvent("e2", "h2", t0))
	a.Apply(entDeletedEvent("e1", "h1", t0))

	b := NewWithTombstoneCap(10)
	b.SetTombstoneTTL(time.Hour)
	b.SetClock(func() time.Time { return snapAt })
	for _, ev := range a.SnapshotEvents(snapAt) {
		b.Apply(ev)
	}

	id, ok := b.MatchTombstone(model.TypeHost, e1Identity())
	if !ok || id != model.EntityID("e1") {
		t.Fatalf("MatchTombstone = (%v, %v), want (e1, true)", id, ok)
	}
	if _, _, deleted := b.GetEntity(model.EntityID("e1")); !deleted {
		t.Fatalf("e1 must not be live in the restored graph (#106)")
	}
	if got, want := b.EntityCount(), a.EntityCount(); got != want {
		t.Fatalf("live entity count = %d, want %d", got, want)
	}
	if _, _, deleted := b.GetEntity(model.EntityID("e2")); deleted {
		t.Fatalf("e2 must be live in the restored graph")
	}
}

func TestSnapshotTombstoneKeepsGraceWindow(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	snapAt := t0.Add(10 * time.Minute)

	a := NewWithTombstoneCap(10)
	a.SetTombstoneTTL(time.Hour)
	a.SetClock(func() time.Time { return t0 })
	a.Apply(entCreatedEvent("e1", "h1", t0))
	a.Apply(entCreatedEvent("e2", "h2", t0))
	a.Apply(entDeletedEvent("e1", "h1", t0))

	b := NewWithTombstoneCap(10)
	b.SetTombstoneTTL(time.Hour)
	clock := snapAt
	b.SetClock(func() time.Time { return clock })
	for _, ev := range a.SnapshotEvents(snapAt) {
		b.Apply(ev)
	}

	// The window is anchored on the original deletion (t0), not the restore.
	clock = t0.Add(59 * time.Minute)
	if id, ok := b.MatchTombstone(model.TypeHost, e1Identity()); !ok || id != model.EntityID("e1") {
		t.Fatalf("MatchTombstone at T+59m = (%v, %v), want (e1, true)", id, ok)
	}
	clock = t0.Add(61 * time.Minute)
	if _, ok := b.MatchTombstone(model.TypeHost, e1Identity()); ok {
		t.Fatalf("MatchTombstone at T+61m should not find e1")
	}
}

func TestSnapshotTombstonesKeepDeletionOrder(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	snapAt := t0.Add(10 * time.Minute)

	a := NewWithTombstoneCap(2)
	a.SetClock(func() time.Time { return t0 })
	a.Apply(entCreatedEvent("e1", "h1", t0))
	a.Apply(entCreatedEvent("e2", "h2", t0))
	a.Apply(entCreatedEvent("e3", "h3", t0))
	a.Apply(entDeletedEvent("e1", "h1", t0))
	a.Apply(entDeletedEvent("e2", "h2", t0))
	a.Apply(entDeletedEvent("e3", "h3", t0))

	b := NewWithTombstoneCap(2)
	b.SetClock(func() time.Time { return snapAt })
	for _, ev := range a.SnapshotEvents(snapAt) {
		b.Apply(ev)
	}

	if _, ok := b.MatchTombstone(model.TypeHost, e1Identity()); ok {
		t.Fatalf("e1 should have been evicted before the snapshot")
	}
	if id, ok := b.MatchTombstone(model.TypeHost, e2Identity()); !ok || id != model.EntityID("e2") {
		t.Fatalf("MatchTombstone e2 = (%v, %v), want (e2, true)", id, ok)
	}
	if id, ok := b.MatchTombstone(model.TypeHost, e3Identity()); !ok || id != model.EntityID("e3") {
		t.Fatalf("MatchTombstone e3 = (%v, %v), want (e3, true)", id, ok)
	}

	// A new deletion on the restored graph evicts in deletion order: e2 goes.
	b.Apply(entCreatedEvent("e4", "h4", snapAt))
	b.Apply(entDeletedEvent("e4", "h4", snapAt))
	if _, ok := b.MatchTombstone(model.TypeHost, e2Identity()); ok {
		t.Fatalf("e2 should be evicted after e4's deletion")
	}
	if _, ok := b.MatchTombstone(model.TypeHost, e3Identity()); !ok {
		t.Fatalf("e3 should survive e4's deletion")
	}
	if _, ok := b.MatchTombstone(model.TypeHost, e4Identity()); !ok {
		t.Fatalf("e4 should be tombstoned")
	}
}
