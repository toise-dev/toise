package store_test

import (
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
