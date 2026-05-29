package projection

import (
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

func TestMatchIdentityExactTolerantAndRehash(t *testing.T) {
	g := New()
	a := model.NewEntityID()
	ident := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-1")}
	g.Apply(entityCreated(a, model.TypeHost, ident...))

	// exact
	if id, exact, found := g.MatchIdentity(model.TypeHost, ident, 1); !found || !exact || id != a {
		t.Errorf("exact match = (%s,%v,%v)", id, exact, found)
	}
	// tolerant: one identifying value differs
	tol := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-2")}
	if id, exact, found := g.MatchIdentity(model.TypeHost, tol, 1); !found || exact || id != a {
		t.Errorf("tolerant match = (%s,%v,%v), want (a,false,true)", id, exact, found)
	}
	// no tolerance -> not found
	if _, _, found := g.MatchIdentity(model.TypeHost, tol, 0); found {
		t.Error("maxDiff 0 should not tolerate a difference")
	}

	// identity_changed rehashes: old identity no longer matches, new does
	g.Apply(model.Event{Entity: &model.EntityEvent{ChangeType: model.EntityIdentityChanged,
		Entity: model.Entity{ID: a, Type: model.TypeHost, Identity: tol}}})
	if _, exact, found := g.MatchIdentity(model.TypeHost, tol, 0); !found || !exact {
		t.Error("new identity should match exactly after identity_changed")
	}
	if _, _, found := g.MatchIdentity(model.TypeHost, ident, 0); found {
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
