package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

func tsx(sec int64) time.Time { return time.Unix(1_700_000_000+sec, 0).UTC() }

func entEvt(id model.EntityID, ct model.ChangeType, when time.Time) model.Event {
	return model.Event{Entity: &model.EntityEvent{
		EventID:    model.NewEventID(),
		ChangeType: ct,
		Entity: model.Entity{ID: id, Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(string(id))}}},
		EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
}

func relEvt(from, to model.EntityID, when time.Time) model.Event {
	return model.Event{Relation: &model.RelationEvent{
		EventID:    model.NewEventID(),
		ChangeType: model.RelationAdded,
		Relation:   model.NewRelation(model.RelRunsOn, from, to),
		EventTime:  when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
}

// TestPruneOlderThanPreservesProjection proves the acceptance directly: rebuild
// the projection before and after a prune and assert it is unchanged (#45).
func TestPruneOlderThanPreservesProjection(t *testing.T) {
	s, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	a, b, c := model.EntityID("a"), model.EntityID("b"), model.EntityID("c")
	for _, ev := range []model.Event{
		entEvt(a, model.EntityCreated, tsx(0)),
		entEvt(a, model.EntityAttributeUpdated, tsx(10)),
		entEvt(b, model.EntityCreated, tsx(5)),
		entEvt(b, model.EntityDeleted, tsx(15)),
		entEvt(c, model.EntityCreated, tsx(50)),
		relEvt(a, c, tsx(60)),
		entEvt(a, model.EntityAttributeUpdated, tsx(100)),
	} {
		if err := s.Append(ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	before := projection.New()
	if err := before.Replay(s); err != nil {
		t.Fatalf("replay before: %v", err)
	}

	if _, _, err := s.PruneOlderThan(tsx(90)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	after := projection.New()
	if err := after.Replay(s); err != nil {
		t.Fatalf("replay after: %v", err)
	}

	if before.EntityCount() != after.EntityCount() || before.RelationCount() != after.RelationCount() {
		t.Errorf("projection changed across prune: before %d/%d, after %d/%d",
			before.EntityCount(), before.RelationCount(), after.EntityCount(), after.RelationCount())
	}
	if after.EntityCount() != 2 || after.RelationCount() != 1 {
		t.Errorf("after prune+replay: %d entities / %d relations, want 2 / 1 (a and c live, a->c)",
			after.EntityCount(), after.RelationCount())
	}
	// Counts are not enough — they pass even when the replay loses the
	// identity/type indexes (#107): a's only surviving event after the prune is
	// an attribute_updated, and it must still be matchable and counted by type,
	// or the next producer observation mints a permanent duplicate.
	for _, id := range []model.EntityID{a, c} {
		ident := []model.KeyValue{{Key: "host.id", Value: model.StringValue(string(id))}}
		if _, ok := after.MatchIdentity(model.TypeHost, ident); !ok {
			t.Errorf("MatchIdentity(%s) missed after prune+replay: identity index lost", id)
		}
	}
	if n := after.CountByType()[model.TypeHost]; n != 2 {
		t.Errorf("CountByType[host] = %d after prune+replay, want 2: type index lost", n)
	}
}

// TestMaintenanceDoesNotBlockAppends pins the #115 contract: coalescing and
// pruning scan on a pebble snapshot without the append mutex, so ingestion
// proceeds while maintenance runs, and the conservative keep-set means no
// concurrently appended entity ever loses its latest event.
func TestMaintenanceDoesNotBlockAppends(t *testing.T) {
	s, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Seed history: per entity, an old created + heartbeats + a recent update.
	const entities = 50
	for i := 0; i < entities; i++ {
		id := model.EntityID(fmt.Sprintf("seed-%d", i))
		for _, ev := range []model.Event{
			entEvt(id, model.EntityCreated, tsx(0)),
			entEvt(id, model.EntityUnchanged, tsx(10)),
			entEvt(id, model.EntityUnchanged, tsx(20)),
			entEvt(id, model.EntityUnchanged, tsx(30)),
			entEvt(id, model.EntityAttributeUpdated, tsx(40)),
		} {
			if err := s.Append(ev); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Appends race the maintenance passes.
	done := make(chan error, 2)
	go func() {
		for i := 0; i < 200; i++ {
			id := model.EntityID(fmt.Sprintf("live-%d", i))
			if err := s.Append(entEvt(id, model.EntityCreated, tsx(1000+int64(i)))); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	go func() {
		for i := 0; i < 5; i++ {
			if _, err := s.CoalesceHeartbeats(); err != nil {
				done <- err
				return
			}
			if _, _, err := s.PruneOlderThan(tsx(35).Add(time.Nanosecond)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent run: %v", err)
		}
	}

	// Every entity — seeded and concurrently appended — must still be live on
	// replay: pruning may only remove superseded history, never current state.
	g := projection.New()
	if err := g.Replay(s); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := g.EntityCount(); got != entities+200 {
		t.Errorf("EntityCount after concurrent maintenance = %d, want %d", got, entities+200)
	}
}
