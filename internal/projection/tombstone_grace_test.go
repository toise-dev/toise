package projection

import (
	"fmt"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func host(i int) model.Entity {
	return model.Entity{
		ID:       model.EntityID(fmt.Sprintf("e%02d", i)),
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(fmt.Sprintf("h%02d", i))}},
	}
}

func hostIdentity(i int) []model.KeyValue {
	return []model.KeyValue{{Key: "host.id", Value: model.StringValue(fmt.Sprintf("h%02d", i))}}
}

// applyAt applies an entity event stamped with a recorded time, the field the
// grace window anchors on.
func applyAt(g *Graph, ct model.ChangeType, e model.Entity, recordedAt time.Time) {
	g.Apply(model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: ct, Entity: e,
		RecordedAt: recordedAt, SchemaVersion: model.SchemaVersion,
	}})
}

// A tombstone is resurrectable until its grace window elapses, then MatchTombstone
// refuses it and PruneTombstones drops it.
func TestMatchTombstoneWithinAndPastWindow(t *testing.T) {
	clock := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	g := New()
	g.SetClock(func() time.Time { return clock })
	g.SetTombstoneTTL(15 * time.Minute)

	applyAt(g, model.EntityCreated, host(1), clock)
	applyAt(g, model.EntityDeleted, host(1), clock)

	if id, ok := g.MatchTombstone(model.TypeHost, hostIdentity(1)); !ok || id != "e01" {
		t.Fatalf("within window: MatchTombstone = %q ok=%v, want e01 true", id, ok)
	}

	clock = clock.Add(16 * time.Minute) // past the 15-minute window
	if id, ok := g.MatchTombstone(model.TypeHost, hostIdentity(1)); ok {
		t.Fatalf("past window: MatchTombstone = %q ok=%v, want refused", id, ok)
	}
	if n := g.PruneTombstones(); n != 1 {
		t.Fatalf("PruneTombstones = %d, want 1", n)
	}
	if _, ok, _ := g.GetEntity("e01"); ok {
		t.Error("e01 should be gone after its window elapsed and a prune")
	}
}

// The grace window is anchored on the delete event's RecordedAt, not the
// apply-time clock — so an EntityDeleted replayed long after it happened (the
// boot replay path, where the clock is wall-time, not the event's) arrives with
// its window already elapsed and is not resurrectable. This is the #183 invariant
// that a stale id stays closed across a restart.
func TestTombstoneDeadlineAnchoredOnRecordedAt(t *testing.T) {
	boot := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	g := New()
	g.SetClock(func() time.Time { return boot }) // wall clock at replay/boot
	g.SetTombstoneTTL(15 * time.Minute)

	deletedLongAgo := boot.Add(-2 * time.Hour)
	applyAt(g, model.EntityCreated, host(1), deletedLongAgo)
	applyAt(g, model.EntityDeleted, host(1), deletedLongAgo)

	if id, ok := g.MatchTombstone(model.TypeHost, hostIdentity(1)); ok {
		t.Fatalf("a delete recorded 2h before boot must not be resurrectable, got %q", id)
	}
	if n := g.PruneTombstones(); n != 1 {
		t.Fatalf("PruneTombstones = %d, want 1 (the already-expired tombstone)", n)
	}
}

// A tombstone evicted by the cap (not the window) is also no longer resurrectable:
// dropTombstone must clear tombByHash, or MatchTombstone would hand back an id
// whose payload is gone.
func TestCapEvictedTombstoneNotResurrectable(t *testing.T) {
	g := NewWithTombstoneCap(4)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	g.SetClock(func() time.Time { return now })

	for i := 0; i < 12; i++ {
		applyAt(g, model.EntityCreated, host(i), now)
		applyAt(g, model.EntityDeleted, host(i), now)
	}
	// e00 is well past the cap of 4: evicted entirely, not a tombstone anymore.
	if id, ok := g.MatchTombstone(model.TypeHost, hostIdentity(0)); ok {
		t.Fatalf("cap-evicted identity resurrected to %q, want refused", id)
	}
	// A recent one is still resurrectable.
	if id, ok := g.MatchTombstone(model.TypeHost, hostIdentity(11)); !ok || id != "e11" {
		t.Fatalf("recent tombstone = %q ok=%v, want e11 true", id, ok)
	}
}

// TTL pruning must compact the deletion-order queue, not just the maps: otherwise
// the backing slice grows without bound under steady delete-then-expire churn
// even though liveTombstones stays low (#140 must hold for the queue too).
func TestPruneCompactsTombstoneQueue(t *testing.T) {
	clock := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	g := New()
	g.SetClock(func() time.Time { return clock })
	g.SetTombstoneTTL(1 * time.Minute)

	for round := 0; round < 200; round++ {
		applyAt(g, model.EntityCreated, host(round), clock)
		applyAt(g, model.EntityDeleted, host(round), clock)
		clock = clock.Add(2 * time.Minute) // each tombstone expires before the next
		g.PruneTombstones()
	}
	if g.liveTombstones != 0 {
		t.Fatalf("liveTombstones = %d, want 0 (all expired)", g.liveTombstones)
	}
	if n := len(g.tombstones); n > 4 {
		t.Fatalf("tombstone queue len = %d after 200 expired rounds, want bounded (compaction failed)", n)
	}
}
