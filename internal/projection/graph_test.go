package projection

import (
	"fmt"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

func kv(k, v string) model.KeyValue { return model.KeyValue{Key: k, Value: model.StringValue(v)} }

func entityCreated(id model.EntityID, typ string, ident ...model.KeyValue) model.Event {
	return model.Event{Entity: &model.EntityEvent{
		ChangeType: model.EntityCreated,
		Entity:     model.Entity{ID: id, Type: typ, Identity: ident},
	}}
}

type evScanner struct{ events []model.Event }

func (s evScanner) Scan(fn func(uint64, model.Event) error) error {
	for i, ev := range s.events {
		if err := fn(uint64(i+1), ev); err != nil {
			return err
		}
	}
	return nil
}

func TestApplyEntityAndCounts(t *testing.T) {
	g := New()
	a := model.NewEntityID()
	g.Apply(entityCreated(a, model.TypeHost, kv("host.id", "h1")))
	if got, ok, del := g.GetEntity(a); !ok || del || got.Type != model.TypeHost {
		t.Fatalf("GetEntity = %+v ok=%v del=%v", got, ok, del)
	}
	if g.EntityCount() != 1 {
		t.Errorf("EntityCount = %d, want 1", g.EntityCount())
	}
	if g.CountByType()[model.TypeHost] != 1 {
		t.Errorf("CountByType[host] = %d, want 1", g.CountByType()[model.TypeHost])
	}

	// soft delete
	g.Apply(model.Event{Entity: &model.EntityEvent{ChangeType: model.EntityDeleted,
		Entity: model.Entity{ID: a, Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}}})
	if _, _, del := g.GetEntity(a); !del {
		t.Error("entity should be soft-deleted")
	}
	if g.EntityCount() != 0 {
		t.Errorf("EntityCount after delete = %d, want 0", g.EntityCount())
	}
}

func TestRelationsAndNeighbors(t *testing.T) {
	g := New()
	p, h := model.NewEntityID(), model.NewEntityID()
	g.Apply(entityCreated(p, model.TypeProcess, kv("pid", "100")))
	g.Apply(entityCreated(h, model.TypeHost, kv("host.id", "h1")))
	rel := model.NewRelation(model.RelRunsOn, p, h)
	g.Apply(model.Event{Relation: &model.RelationEvent{ChangeType: model.RelationAdded, Relation: rel}})

	if g.RelationCount() != 1 {
		t.Fatalf("RelationCount = %d, want 1", g.RelationCount())
	}
	// neighbors of p at depth 1 -> h
	nb := g.Neighbors(p, "", 1)
	if len(nb) != 1 || nb[0].ID != h {
		t.Errorf("Neighbors(p) = %+v, want [h]", nb)
	}
	// neighbors of h (incoming) -> p
	if nb := g.Neighbors(h, "", 1); len(nb) != 1 || nb[0].ID != p {
		t.Errorf("Neighbors(h) = %+v, want [p]", nb)
	}
	// relType filter excludes
	if nb := g.Neighbors(p, model.RelHasInterface, 1); len(nb) != 0 {
		t.Errorf("Neighbors with wrong relType = %+v, want empty", nb)
	}
	// depth 0 -> none
	if nb := g.Neighbors(p, "", 0); nb != nil {
		t.Errorf("Neighbors depth 0 = %+v, want nil", nb)
	}

	// remove relation
	g.Apply(model.Event{Relation: &model.RelationEvent{ChangeType: model.RelationRemoved, Relation: rel}})
	if g.RelationCount() != 0 {
		t.Errorf("RelationCount after remove = %d, want 0", g.RelationCount())
	}
	if nb := g.Neighbors(p, "", 1); len(nb) != 0 {
		t.Errorf("Neighbors after remove = %+v, want empty", nb)
	}
}

func TestRelationsTouching(t *testing.T) {
	g := New()
	a, b, c := model.NewEntityID(), model.NewEntityID(), model.NewEntityID()
	g.Apply(entityCreated(a, model.TypeProcess, kv("pid", "1")))
	g.Apply(entityCreated(b, model.TypeHost, kv("host.id", "h1")))
	g.Apply(entityCreated(c, model.TypeHost, kv("host.id", "h2")))
	runsOn := model.NewRelation(model.RelRunsOn, a, b)     // a -> b
	monitors := model.NewRelation(model.RelMonitors, c, a) // c -> a
	for _, r := range []model.Relation{runsOn, monitors} {
		g.Apply(model.Event{Relation: &model.RelationEvent{ChangeType: model.RelationAdded, Relation: r}})
	}

	// a is touched by both edges (one outgoing, one incoming).
	if got := g.RelationsTouching(a, ""); len(got) != 2 {
		t.Fatalf("RelationsTouching(a) = %d, want 2", len(got))
	}
	// b only by runs_on.
	if got := g.RelationsTouching(b, ""); len(got) != 1 || got[0].ID != runsOn.ID {
		t.Fatalf("RelationsTouching(b) = %+v, want [runs_on]", got)
	}
	// type filter.
	if got := g.RelationsTouching(a, model.RelMonitors); len(got) != 1 || got[0].ID != monitors.ID {
		t.Fatalf("RelationsTouching(a, monitors) = %+v, want [monitors]", got)
	}
	// a self-loop appears exactly once (it is in both the out and in index).
	loop := model.NewRelation(model.RelConnectedTo, b, b)
	g.Apply(model.Event{Relation: &model.RelationEvent{ChangeType: model.RelationAdded, Relation: loop}})
	if got := g.RelationsTouching(b, model.RelConnectedTo); len(got) != 1 || got[0].ID != loop.ID {
		t.Fatalf("self-loop touching = %+v, want one entry", got)
	}
}

func TestMatchIdentityExact(t *testing.T) {
	g := New()
	a := model.NewEntityID()
	ident := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-1")}
	g.Apply(entityCreated(a, model.TypeHost, ident...))

	// exact match
	if id, found := g.MatchIdentity(model.TypeHost, ident); !found || id != a {
		t.Errorf("exact match = (%s,%v)", id, found)
	}
	// any difference in an identifying value is a different entity (no tolerance)
	diff := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-2")}
	if _, found := g.MatchIdentity(model.TypeHost, diff); found {
		t.Error("a differing identity must not match (exact matching, ADR 0018)")
	}

	// The engine no longer emits entity.identity_changed (ADR 0018), but the
	// projection must still replay it from historical logs: applying one rehashes
	// the index so the new identity matches and the old no longer does.
	g.Apply(model.Event{Entity: &model.EntityEvent{ChangeType: model.EntityIdentityChanged,
		Entity: model.Entity{ID: a, Type: model.TypeHost, Identity: diff}}})
	if _, found := g.MatchIdentity(model.TypeHost, diff); !found {
		t.Error("new identity should match exactly after rehash")
	}
	if _, found := g.MatchIdentity(model.TypeHost, ident); found {
		t.Error("old identity should no longer match exactly")
	}
}

func TestReplay(t *testing.T) {
	a, h := model.NewEntityID(), model.NewEntityID()
	rel := model.NewRelation(model.RelRunsOn, a, h)
	sc := evScanner{events: []model.Event{
		entityCreated(a, model.TypeProcess, kv("pid", "1")),
		entityCreated(h, model.TypeHost, kv("host.id", "h1")),
		{Relation: &model.RelationEvent{ChangeType: model.RelationAdded, Relation: rel}},
	}}
	g := New()
	if err := g.Replay(sc); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if g.EntityCount() != 2 || g.RelationCount() != 1 {
		t.Errorf("after replay: %d entities, %d relations", g.EntityCount(), g.RelationCount())
	}
}

// TestApplyIndexesWhenUpdateIsFirstEvent pins the #107 invariant at the
// projection: after retention pruning, an entity's (or relation's) first
// surviving event on replay can be an attribute update, and the identity, type,
// and adjacency indexes must be built from it all the same.
func TestApplyIndexesWhenUpdateIsFirstEvent(t *testing.T) {
	g := New()
	ident := []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}
	ent := model.Entity{ID: "e1", Type: model.TypeHost, Identity: ident}

	g.Apply(model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityAttributeUpdated,
		Entity: ent, SchemaVersion: model.SchemaVersion,
	}})
	if _, ok := g.MatchIdentity(model.TypeHost, ident); !ok {
		t.Error("MatchIdentity missed an entity whose first event is attribute_updated")
	}
	if n := g.CountByType()[model.TypeHost]; n != 1 {
		t.Errorf("CountByType[host] = %d, want 1", n)
	}

	g.Apply(model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityCreated,
		Entity: model.Entity{ID: "e2", Type: model.TypeProcess,
			Identity: []model.KeyValue{{Key: "pid", Value: model.StringValue("1")}}},
		SchemaVersion: model.SchemaVersion,
	}})
	rel := model.NewRelation(model.RelRunsOn, "e2", "e1")
	g.Apply(model.Event{Relation: &model.RelationEvent{
		EventID: model.NewEventID(), ChangeType: model.RelationAttributeChanged,
		Relation: rel, SchemaVersion: model.SchemaVersion,
	}})
	if n := len(g.Neighbors("e1", "", 1)); n != 1 {
		t.Errorf("Neighbors = %d via a relation whose first event is attribute_changed, want 1 (adjacency lost)", n)
	}
}

// TestTombstoneCacheBounded pins #140: projection memory tracks the live graph
// plus a bounded tombstone window, not cumulative churn. Recent deletions stay
// readable by id; the oldest are evicted entirely; resurrected ids do not
// count against the bound.
func TestTombstoneCacheBounded(t *testing.T) {
	g := NewWithTombstoneCap(4)
	mk := func(i int) model.Entity {
		return model.Entity{ID: model.EntityID(fmt.Sprintf("e%02d", i)), Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(fmt.Sprintf("h%02d", i))}}}
	}
	apply := func(ct model.ChangeType, e model.Entity) {
		g.Apply(model.Event{Entity: &model.EntityEvent{
			EventID: model.NewEventID(), ChangeType: ct, Entity: e, SchemaVersion: model.SchemaVersion,
		}})
	}

	// Churn 12 entities through create+delete with a cap of 4.
	for i := 0; i < 12; i++ {
		apply(model.EntityCreated, mk(i))
		apply(model.EntityDeleted, mk(i))
	}
	if g.EntityCount() != 0 {
		t.Fatalf("EntityCount = %d, want 0", g.EntityCount())
	}
	if n := len(g.entities); n != 4 {
		t.Fatalf("retained payloads = %d, want 4 (the tombstone cap, not the 12 churned)", n)
	}
	// The most recent deletions read back with deleted=true.
	if _, ok, deleted := g.GetEntity("e11"); !ok || !deleted {
		t.Errorf("e11 = ok %v deleted %v, want readable tombstone", ok, deleted)
	}
	// The oldest are evicted entirely.
	if _, ok, _ := g.GetEntity("e00"); ok {
		t.Error("e00 must be evicted past the cap")
	}

	// A replayed create for a tombstoned id (defensive: the engine never emits
	// one, but a crafted log could) un-tombstones it and frees its slot.
	apply(model.EntityCreated, mk(11))
	if _, ok, deleted := g.GetEntity("e11"); !ok || deleted {
		t.Errorf("re-created e11 = ok %v deleted %v, want live", ok, deleted)
	}
	apply(model.EntityCreated, mk(20))
	apply(model.EntityDeleted, mk(20))
	if _, ok, deleted := g.GetEntity("e20"); !ok || !deleted {
		t.Errorf("e20 tombstone = ok %v deleted %v, want readable", ok, deleted)
	}
}

// A delete whose create aged out of retention (a delete-without-create on
// replay) leaves a phantom tombstone: an id in deleted but never in entities.
// EntityCount must count live entities by membership, not len-len, or it
// undercounts — here one live entity would read as zero (#166 P1).
func TestEntityCountIgnoresPhantomTombstone(t *testing.T) {
	g := New()
	apply := func(ct model.ChangeType, e model.Entity) {
		g.Apply(model.Event{Entity: &model.EntityEvent{
			EventID: model.NewEventID(), ChangeType: ct, Entity: e, SchemaVersion: model.SchemaVersion,
		}})
	}
	apply(model.EntityCreated, model.Entity{ID: "A", Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "a")}})
	// B's create aged out of retention; only its delete is replayed.
	apply(model.EntityDeleted, model.Entity{ID: "B", Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "b")}})

	if _, ok := g.entities["B"]; ok {
		t.Fatal("B must not be in entities (its create never replayed)")
	}
	if got := g.EntityCount(); got != 1 {
		t.Fatalf("EntityCount = %d, want 1 (A is live; B is a phantom tombstone)", got)
	}
}
