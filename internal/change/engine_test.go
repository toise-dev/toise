package change

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

func kv(k, v string) model.KeyValue { return model.KeyValue{Key: k, Value: model.StringValue(v)} }

var t0 = time.Unix(1_700_000_000, 0).UTC()

func fixedNow() time.Time { return time.Unix(1_700_000_100, 0).UTC() }

type fakeAppender struct{ n int }

func (f *fakeAppender) Append(evs ...model.Event) error { f.n += len(evs); return nil }

type record struct {
	ev model.Event
	hp bool
}

func newEngine(t *testing.T) (*Engine, *projection.Graph, *[]record) {
	t.Helper()
	g := projection.New()
	e := New(g, &fakeAppender{}, WithClock(fixedNow))
	var recs []record
	e.Subscribe(func(ev model.Event, hp bool) { recs = append(recs, record{ev, hp}) })
	return e, g, &recs
}

func TestObserveEntityClassification(t *testing.T) {
	e, _, _ := newEngine(t)
	ident := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-1")}

	ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: ident, Attributes: []model.KeyValue{kv("status", "up")}, EventTime: t0})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ev.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("first observation = %s, want entity.created", ev.Entity.ChangeType)
	}
	id := ev.Entity.Entity.ID
	if ev.Entity.RecordedAt != fixedNow() {
		t.Error("recorded_at not stamped from clock")
	}

	// same -> unchanged
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: ident, Attributes: []model.KeyValue{kv("status", "up")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityUnchanged {
		t.Errorf("repeat = %s, want entity.unchanged", ev.Entity.ChangeType)
	}

	// status flip -> state_changed
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: ident, Attributes: []model.KeyValue{kv("status", "down")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityStateChanged {
		t.Errorf("status flip = %s, want entity.state_changed", ev.Entity.ChangeType)
	}
	if len(ev.Entity.ChangedKeys) != 1 || ev.Entity.ChangedKeys[0] != "status" {
		t.Errorf("changed keys = %v, want [status]", ev.Entity.ChangedKeys)
	}

	// add descriptive attribute -> attribute_updated
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: ident, Attributes: []model.KeyValue{kv("status", "down"), kv("os", "linux")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityAttributeUpdated {
		t.Errorf("attr add = %s, want entity.attribute_updated", ev.Entity.ChangeType)
	}

	// one identifying value changes -> identity_changed, same logical ID
	tol := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-2")}
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: tol, Attributes: []model.KeyValue{kv("status", "down"), kv("os", "linux")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityIdentityChanged {
		t.Errorf("identity change = %s, want entity.identity_changed", ev.Entity.ChangeType)
	}
	if ev.Entity.Entity.ID != id {
		t.Errorf("logical ID changed across identity_changed: %s != %s", ev.Entity.Entity.ID, id)
	}
}

func TestObserveRelationLifecycle(t *testing.T) {
	e, _, recs := newEngine(t)
	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}

	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeProcess, Identity: procRef.Identity, EventTime: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0}); err != nil {
		t.Fatal(err)
	}

	rel := RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}
	ev, emitted, err := e.ObserveRelation(rel)
	if err != nil || !emitted {
		t.Fatalf("relation add: emitted=%v err=%v", emitted, err)
	}
	if ev.Relation.ChangeType != model.RelationAdded {
		t.Errorf("relation = %s, want relation.added", ev.Relation.ChangeType)
	}
	// structural add -> high-priority signal
	last := (*recs)[len(*recs)-1]
	if !last.hp {
		t.Error("structural relation.added should be high-priority")
	}

	// same again -> no event
	if _, em, _ := e.ObserveRelation(rel); em {
		t.Error("unchanged relation should not emit")
	}

	// attribute change
	rel2 := rel
	rel2.Attributes = []model.KeyValue{kv("weight", "1")}
	if ev2, em, _ := e.ObserveRelation(rel2); !em || ev2.Relation.ChangeType != model.RelationAttributeChanged {
		t.Errorf("attr change = %s emitted=%v", ev2.Relation.ChangeType, em)
	}

	// remove -> high-priority
	ev, emitted, err = e.RemoveRelation(rel)
	if err != nil || !emitted || ev.Relation.ChangeType != model.RelationRemoved {
		t.Fatalf("remove: %s emitted=%v err=%v", ev.Relation.ChangeType, emitted, err)
	}
	if !(*recs)[len(*recs)-1].hp {
		t.Error("structural relation.removed should be high-priority")
	}
	// removing again -> no event
	if _, emitted, _ := e.RemoveRelation(rel); emitted {
		t.Error("removing absent relation should not emit")
	}
}

func TestObserveRelationUnknownEndpoint(t *testing.T) {
	e, _, _ := newEngine(t)
	rel := RelationObservation{
		Type:      model.RelRunsOn,
		From:      EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "1")}},
		To:        EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "x")}},
		EventTime: t0,
	}
	if _, _, err := e.ObserveRelation(rel); err == nil {
		t.Error("expected error for unknown endpoint")
	}
}

func TestEngineWithStoreAndReplay(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	g := projection.New()
	e := New(g, st, WithClock(fixedNow))

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	mustObs(t, e, EntityObservation{Type: model.TypeProcess, Identity: procRef.Identity, EventTime: t0})
	mustObs(t, e, EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, Attributes: []model.KeyValue{kv("status", "up")}, EventTime: t0})
	if _, _, err := e.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}); err != nil {
		t.Fatal(err)
	}

	// Rebuild a fresh graph by replaying the persisted log; it must match.
	g2 := projection.New()
	if err := g2.Replay(st); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if g2.EntityCount() != g.EntityCount() || g2.RelationCount() != g.RelationCount() {
		t.Errorf("replayed graph differs: entities %d/%d relations %d/%d",
			g2.EntityCount(), g.EntityCount(), g2.RelationCount(), g.RelationCount())
	}
	if g2.EntityCount() != 2 || g2.RelationCount() != 1 {
		t.Errorf("replayed graph: %d entities, %d relations", g2.EntityCount(), g2.RelationCount())
	}
}

func mustObs(t *testing.T, e *Engine, obs EntityObservation) {
	t.Helper()
	if _, err := e.ObserveEntity(obs); err != nil {
		t.Fatalf("observe: %v", err)
	}
}

func TestDiffAttributes(t *testing.T) {
	changed, state := diffAttributes([]model.KeyValue{kv("status", "up"), kv("os", "linux")}, []model.KeyValue{kv("status", "down"), kv("os", "linux")})
	if len(changed) != 1 || changed[0] != "status" || !state {
		t.Errorf("diff = %v state=%v, want [status] true", changed, state)
	}
	changed, state = diffAttributes([]model.KeyValue{kv("os", "linux")}, []model.KeyValue{kv("os", "bsd")})
	if len(changed) != 1 || state {
		t.Errorf("diff = %v state=%v, want [os] false", changed, state)
	}
	if changed, _ := diffAttributes(nil, nil); len(changed) != 0 {
		t.Errorf("empty diff = %v", changed)
	}
}
