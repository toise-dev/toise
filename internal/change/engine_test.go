package change

import (
	"errors"
	"fmt"
	"log/slog"
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

	// a differing identifying value is a DIFFERENT entity under exact matching
	// (ADR 0018): a new logical ID, classified as created — never a fuzzy
	// identity-change merge.
	diff := []model.KeyValue{kv("host.id", "h1"), kv("host.name", "web-2")}
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: diff, Attributes: []model.KeyValue{kv("status", "down")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityCreated {
		t.Errorf("differing identity = %s, want entity.created", ev.Entity.ChangeType)
	}
	if ev.Entity.Entity.ID == id {
		t.Error("a differing identity must get a new logical ID, not reuse the original")
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

func TestRelationBufferReconcilesAndExpires(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithRelationBuffer(30*time.Second),
		WithLogger(slog.New(slog.DiscardHandler)))

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "nginx")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	edge := RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}

	// the edge arrives before its endpoints: parked, not an error, not yet applied
	if _, emitted, err := e.ObserveRelation(edge); err != nil || emitted {
		t.Fatalf("parked edge: emitted=%v err=%v, want false,nil", emitted, err)
	}
	if g.RelationCount() != 0 {
		t.Fatalf("edge should be parked, not applied; count=%d", g.RelationCount())
	}

	// endpoints arrive -> the next observations flush and reconcile the edge
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	mustObserve(t, e, model.TypeHost, hostRef.Identity)
	if g.RelationCount() != 1 {
		t.Fatalf("edge should reconcile once endpoints exist; count=%d", g.RelationCount())
	}

	// a second edge whose endpoints never come is dropped after the TTL
	ghost := RelationObservation{Type: model.RelRunsOn,
		From:      EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "ghost")}},
		To:        hostRef,
		EventTime: t0}
	if _, _, err := e.ObserveRelation(ghost); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)                                               // past the 30s TTL
	mustObserve(t, e, model.TypeHost, []model.KeyValue{kv("host.id", "h2")}) // triggers the expiring flush
	// even if its endpoint shows up afterwards, the dropped edge must not resurface
	mustObserve(t, e, model.TypeProcess, []model.KeyValue{kv("process.executable.name", "ghost")})
	if g.RelationCount() != 1 {
		t.Errorf("expired edge must not reconcile later; count=%d, want 1", g.RelationCount())
	}
}

func TestLivenessSweepExpiresStaleEntities(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	host1 := []model.KeyValue{kv("host.id", "h1")} // 60s liveness interval
	host2 := []model.KeyValue{kv("host.id", "h2")} // no interval -> never expires
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host1, Interval: time.Minute, EventTime: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host2, EventTime: t0}); err != nil {
		t.Fatal(err)
	}

	// before the deadline nothing expires
	now = now.Add(30 * time.Second)
	if n := e.Sweep(); n != 0 {
		t.Fatalf("premature expiry: swept %d", n)
	}

	// a fresh heartbeat resets the deadline
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host1, Interval: time.Minute, EventTime: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second) // 75s since first sight, but only 45s since the heartbeat
	if n := e.Sweep(); n != 0 {
		t.Fatalf("heartbeat should have reset the deadline; swept %d", n)
	}

	// let it lapse: the stale entity expires, the interval-less one survives
	now = now.Add(time.Minute)
	if n := e.Sweep(); n != 1 {
		t.Fatalf("stale entity should expire; swept %d, want 1", n)
	}
	if _, found := g.MatchIdentity(model.TypeHost, host1); found {
		t.Error("expired entity should be soft-deleted")
	}
	if _, found := g.MatchIdentity(model.TypeHost, host2); !found {
		t.Error("interval-less entity must not be expired")
	}
	if g.EntityCount() != 1 {
		t.Errorf("EntityCount = %d, want 1", g.EntityCount())
	}
	if n := e.Sweep(); n != 0 {
		t.Errorf("re-sweep should be a no-op; swept %d", n)
	}
}

type countingAppender struct{ calls, events int }

func (c *countingAppender) Append(evs ...model.Event) error {
	c.calls++
	c.events += len(evs)
	return nil
}

func TestBatchAppendsOnce(t *testing.T) {
	g := projection.New()
	ap := &countingAppender{}
	e := New(g, ap, WithClock(fixedNow))

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "nginx")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	err := e.Batch(func(b *Batch) {
		if _, err := b.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ObserveEntity(EntityObservation{Type: model.TypeProcess, Identity: procRef.Identity, EventTime: t0}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	// three qualified events, but exactly ONE durable append (the batch flush)
	if ap.calls != 1 {
		t.Errorf("Append calls = %d, want 1 (batched, one fsync)", ap.calls)
	}
	if ap.events != 3 {
		t.Errorf("events appended = %d, want 3", ap.events)
	}
	if g.EntityCount() != 2 || g.RelationCount() != 1 {
		t.Errorf("graph = %d entities, %d relations, want 2 and 1", g.EntityCount(), g.RelationCount())
	}
}

func TestMultiProducerRefCounting(t *testing.T) {
	e, g, _ := newEngine(t)
	ident := []model.KeyValue{kv("db.instance.id", "pg:1")}
	obs := func(producer string) EntityObservation {
		return EntityObservation{Type: model.TypeDatabase, Identity: ident, Producer: producer, EventTime: t0}
	}
	// two agents observe the same db
	if _, err := e.ObserveEntity(obs("agent-A")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ObserveEntity(obs("agent-B")); err != nil {
		t.Fatal(err)
	}
	if g.EntityCount() != 1 {
		t.Fatalf("the shared db should be one entity; count=%d", g.EntityCount())
	}

	// agent A releases its view — the db survives (B still references it), no event
	if _, em, err := e.DeleteEntity(obs("agent-A")); err != nil || em {
		t.Fatalf("A delete: emitted=%v err=%v, want false (not the last reference)", em, err)
	}
	if _, found := g.MatchIdentity(model.TypeDatabase, ident); !found {
		t.Error("db must survive while agent B still references it")
	}

	// agent B releases — the last reference is gone, the db is actually deleted
	if _, em, err := e.DeleteEntity(obs("agent-B")); err != nil || !em {
		t.Fatalf("B delete: emitted=%v err=%v, want true (the last reference)", em, err)
	}
	if _, found := g.MatchIdentity(model.TypeDatabase, ident); found {
		t.Error("db must be deleted once the last producer releases it")
	}
}

func TestMultiProducerIntervalExpiry(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))
	ident := []model.KeyValue{kv("db.instance.id", "pg:1")}
	obs := func(producer string) EntityObservation {
		return EntityObservation{Type: model.TypeDatabase, Identity: ident, Producer: producer, Interval: time.Minute, EventTime: now}
	}
	if _, err := e.ObserveEntity(obs("A")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ObserveEntity(obs("B")); err != nil {
		t.Fatal(err)
	}

	// B heartbeats again (extends its deadline); A goes quiet
	now = now.Add(50 * time.Second)
	if _, err := e.ObserveEntity(obs("B")); err != nil {
		t.Fatal(err)
	}
	// past A's deadline (t0+60) but not B's (t0+110): A's ref lapses, db survives
	now = now.Add(40 * time.Second) // t0+90s
	if n := e.Sweep(); n != 0 {
		t.Fatalf("db must survive while B's heartbeat is fresh; swept %d", n)
	}
	if _, found := g.MatchIdentity(model.TypeDatabase, ident); !found {
		t.Error("db must survive A's lapse while B heartbeats")
	}
	// past B's deadline too: the db expires
	now = now.Add(40 * time.Second) // t0+130s, past B's t0+110
	if n := e.Sweep(); n != 1 {
		t.Fatalf("db must expire once all producers lapse; swept %d, want 1", n)
	}
	if _, found := g.MatchIdentity(model.TypeDatabase, ident); found {
		t.Error("db must be deleted once the last producer's interval lapses")
	}
}

func TestDeleteCascadesIncidentRelations(t *testing.T) {
	e, g, _ := newEngine(t)
	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "nginx")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	mustObserve(t, e, model.TypeHost, hostRef.Identity)
	if _, em, err := e.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}); err != nil || !em {
		t.Fatalf("relation add: emitted=%v err=%v", em, err)
	}
	if g.RelationCount() != 1 {
		t.Fatalf("relation should exist; count=%d", g.RelationCount())
	}

	// deleting an endpoint removes its incident edge — no explicit unrelate needed.
	if _, _, err := e.DeleteEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0}); err != nil {
		t.Fatal(err)
	}
	if g.RelationCount() != 0 {
		t.Errorf("incident edge should be removed with its endpoint; count=%d", g.RelationCount())
	}
}

func TestLivenessSweepExpiresStaleRelations(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "nginx")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	mustObserve(t, e, model.TypeHost, hostRef.Identity)

	edge := RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, Interval: time.Minute, EventTime: t0}
	if _, em, err := e.ObserveRelation(edge); err != nil || !em {
		t.Fatalf("relation add: emitted=%v err=%v", em, err)
	}
	if g.RelationCount() != 1 {
		t.Fatalf("relation should exist; count=%d", g.RelationCount())
	}

	now = now.Add(30 * time.Second)
	if n := e.Sweep(); n != 0 {
		t.Fatalf("premature edge expiry: swept %d", n)
	}
	// re-asserting (even unchanged) resets the deadline
	edge.EventTime = now
	if _, _, err := e.ObserveRelation(edge); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second)
	if n := e.Sweep(); n != 0 {
		t.Fatalf("heartbeat should reset the edge deadline; swept %d", n)
	}
	now = now.Add(time.Minute)
	if n := e.Sweep(); n != 1 {
		t.Fatalf("stale edge should expire; swept %d, want 1", n)
	}
	if g.RelationCount() != 0 {
		t.Errorf("expired edge should be removed; count=%d", g.RelationCount())
	}
	if n := e.Sweep(); n != 0 {
		t.Errorf("re-sweep should be a no-op; swept %d", n)
	}
}

func mustObserve(t *testing.T, e *Engine, typ string, ident []model.KeyValue) {
	t.Helper()
	if _, err := e.ObserveEntity(EntityObservation{Type: typ, Identity: ident, EventTime: t0}); err != nil {
		t.Fatal(err)
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

func TestRemoveRelationMissingEndpointIsNoOp(t *testing.T) {
	e, _, recs := newEngine(t)
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}}

	// Removing a relation whose endpoints never existed is nothing-to-remove,
	// not an error: removal-by-absence reconciliation hits this whenever the
	// target was already deleted and the cascade took the edge with it (#110).
	ev, emitted, err := e.RemoveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0})
	if err != nil {
		t.Fatalf("remove with missing endpoints: %v, want nil", err)
	}
	if emitted || ev.Relation != nil {
		t.Errorf("remove with missing endpoints emitted an event: %+v", ev)
	}

	// Same when only the target is missing: the source resolves, the target is gone.
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	ev, emitted, err = e.RemoveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0})
	if err != nil {
		t.Fatalf("remove with missing target: %v, want nil", err)
	}
	if emitted || ev.Relation != nil {
		t.Errorf("remove with missing target emitted an event: %+v", ev)
	}
	for _, r := range *recs {
		if r.ev.Relation != nil {
			t.Errorf("no relation event should reach subscribers, got %+v", r.ev)
		}
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

type failingAppender struct {
	fail   bool
	events int
}

func (f *failingAppender) Append(evs ...model.Event) error {
	if f.fail {
		return errors.New("append failed (injected)")
	}
	f.events += len(evs)
	return nil
}

// TestBatchFlushFailureKeepsProjectionAndSubscribersClean pins the staged-commit
// contract (#108): when the durable append fails, the projection must not have
// moved, no subscriber must have been notified, and the producer's retry must
// re-classify against durable state — regenerating the relation that the
// pre-fix ordering lost forever.
func TestBatchFlushFailureKeepsProjectionAndSubscribersClean(t *testing.T) {
	g := projection.New()
	ap := &failingAppender{fail: true}
	e := New(g, ap, WithClock(fixedNow))
	var recs []record
	e.Subscribe(func(ev model.Event, hp bool) { recs = append(recs, record{ev, hp}) })

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	observeAll := func(b *Batch) (entityTypes []model.ChangeType, relAdded bool) {
		ev, err := b.ObserveEntity(EntityObservation{Type: model.TypeProcess, Identity: procRef.Identity, EventTime: t0})
		if err != nil {
			t.Fatal(err)
		}
		entityTypes = append(entityTypes, ev.Entity.ChangeType)
		ev, err = b.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0})
		if err != nil {
			t.Fatal(err)
		}
		entityTypes = append(entityTypes, ev.Entity.ChangeType)
		rev, emitted, err := b.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0})
		if err != nil {
			t.Fatal(err)
		}
		return entityTypes, emitted && rev.Relation.ChangeType == model.RelationAdded
	}

	if err := e.Batch(func(b *Batch) { observeAll(b) }); err == nil {
		t.Fatal("batch must surface the flush failure")
	}
	if g.EntityCount() != 0 || g.RelationCount() != 0 {
		t.Fatalf("projection ran ahead of the durable log: %d entities, %d relations", g.EntityCount(), g.RelationCount())
	}
	if len(recs) != 0 {
		t.Fatalf("subscribers saw %d events that were never persisted", len(recs))
	}

	// Retry against a healed appender: everything re-classifies as new.
	ap.fail = false
	err := e.Batch(func(b *Batch) {
		types, relAdded := observeAll(b)
		for _, ct := range types {
			if ct != model.EntityCreated {
				t.Errorf("retried entity classified as %s, want entity.created", ct)
			}
		}
		if !relAdded {
			t.Error("retried relation not regenerated as relation.added")
		}
	})
	if err != nil {
		t.Fatalf("retry batch: %v", err)
	}
	if g.EntityCount() != 2 || g.RelationCount() != 1 {
		t.Errorf("after retry: %d entities, %d relations, want 2 and 1", g.EntityCount(), g.RelationCount())
	}
	if ap.events != 3 {
		t.Errorf("durably appended %d events, want 3", ap.events)
	}
	if len(recs) != 3 {
		t.Errorf("subscribers saw %d events, want 3", len(recs))
	}
}

type orderAppender struct{ log *[]string }

func (o orderAppender) Append(evs ...model.Event) error {
	*o.log = append(*o.log, "append")
	return nil
}

// TestBatchNotifiesOnlyAfterDurableAppend pins the flush ordering: one durable
// append, then the notifications.
func TestBatchNotifiesOnlyAfterDurableAppend(t *testing.T) {
	g := projection.New()
	var order []string
	e := New(g, orderAppender{&order}, WithClock(fixedNow))
	e.Subscribe(func(ev model.Event, hp bool) { order = append(order, "notify") })

	err := e.Batch(func(b *Batch) {
		mustBatchObserve(t, b, model.TypeHost, []model.KeyValue{kv("host.id", "h1")})
		mustBatchObserve(t, b, model.TypeProcess, []model.KeyValue{kv("pid", "100")})
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"append", "notify", "notify"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v (notify must follow the durable append)", order, want)
		}
	}
}

func mustBatchObserve(t *testing.T, b *Batch, typ string, ident []model.KeyValue) {
	t.Helper()
	if _, err := b.ObserveEntity(EntityObservation{Type: typ, Identity: ident, EventTime: t0}); err != nil {
		t.Fatal(err)
	}
}

// TestBatchClassificationSeesStagedState pins in-batch read consistency on the
// staging overlay: a repeat observation classifies as unchanged, and an
// in-batch delete cascades a relation that was itself staged in the same batch.
func TestBatchClassificationSeesStagedState(t *testing.T) {
	g := projection.New()
	e := New(g, &fakeAppender{}, WithClock(fixedNow))
	var relRemoved int
	e.Subscribe(func(ev model.Event, hp bool) {
		if ev.Relation != nil && ev.Relation.ChangeType == model.RelationRemoved {
			relRemoved++
		}
	})

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	err := e.Batch(func(b *Batch) {
		mustBatchObserve(t, b, model.TypeHost, hostRef.Identity)
		ev, err := b.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0})
		if err != nil {
			t.Fatal(err)
		}
		if ev.Entity.ChangeType != model.EntityUnchanged {
			t.Errorf("repeat in-batch observation = %s, want entity.unchanged", ev.Entity.ChangeType)
		}
		mustBatchObserve(t, b, model.TypeProcess, procRef.Identity)
		if _, _, err := b.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}); err != nil {
			t.Fatal(err)
		}
		// Deleting the host inside the same batch must cascade the staged edge.
		if _, _, err := b.DeleteEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0}); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.EntityCount() != 1 {
		t.Errorf("EntityCount = %d, want 1 (proc; host deleted in-batch)", g.EntityCount())
	}
	if g.RelationCount() != 0 {
		t.Errorf("RelationCount = %d, want 0 (staged edge cascaded in-batch)", g.RelationCount())
	}
	if relRemoved != 1 {
		t.Errorf("relation.removed notifications = %d, want 1", relRemoved)
	}
}

// TestPendingBufferCapAndSweepExpiry pins #115: the out-of-order buffer is
// bounded (oldest dropped at the cap) and Sweep expires parked edges on a
// quiet instance where no entity observation triggers flushPending.
func TestPendingBufferCapAndSweepExpiry(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithRelationBuffer(30*time.Second),
		WithLogger(slog.New(slog.DiscardHandler)))

	park := func(n int) {
		for i := 0; i < n; i++ {
			obs := RelationObservation{Type: model.RelRunsOn,
				From:      EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", fmt.Sprintf("p%d", len(e.pending)+i))}},
				To:        EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "missing")}},
				EventTime: t0}
			if _, _, err := e.ObserveRelation(obs); err != nil {
				t.Fatal(err)
			}
		}
	}
	park(maxPendingRelations + 5)
	if len(e.pending) != maxPendingRelations {
		t.Fatalf("pending = %d, want capped at %d", len(e.pending), maxPendingRelations)
	}

	// Sweep drops every parked edge once its TTL lapses, with no observation
	// needed to trigger the flush.
	now = now.Add(time.Minute)
	e.Sweep()
	if len(e.pending) != 0 {
		t.Errorf("pending = %d after sweep past TTL, want 0", len(e.pending))
	}
}

// TestLivenessMementoSurvivesRestart pins #139 and #164: the liveness
// bookkeeping round-trips through the snapshot so the post-restart sweeper CAN
// reap a producer that died while the server was down — but only after a
// one-interval boot grace, so a restart after long downtime does not
// mass-delete entities of producers that are alive and about to re-export.
func TestLivenessMementoSurvivesRestart(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{}, WithClock(func() time.Time { return now }))

	// A producer asserts an entity with a 30s heartbeat interval.
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost,
		Identity: []model.KeyValue{kv("host.id", "doomed")},
		Interval: 30 * time.Second, Producer: "p1", EventTime: t0}); err != nil {
		t.Fatal(err)
	}

	blob, err := e.LivenessBlob()
	if err != nil {
		t.Fatal(err)
	}

	// "Restart": a fresh graph (from the snapshot events) and a fresh engine,
	// with the wall clock now PAST the producer's deadline — the downtime ate it.
	g2 := projection.New()
	for _, ev := range g.SnapshotEvents(t0) {
		g2.Apply(ev)
	}
	now = t0.Add(5 * time.Minute)
	boot := now
	e2 := New(g2, &fakeAppender{}, WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err := e2.RestoreLiveness(blob); err != nil {
		t.Fatal(err)
	}

	// Restored deadlines are floored to boot + the producer's interval (#164):
	// the first sweep right after boot must NOT mass-delete entities of
	// producers that are alive but simply have not re-exported yet.
	now = boot.Add(time.Second)
	if n := e2.Sweep(); n != 0 {
		t.Fatalf("sweep right after boot expired %d, want 0 (one-interval boot grace)", n)
	}
	if g2.EntityCount() != 1 {
		t.Fatalf("EntityCount = %d after boot-time sweep, want 1", g2.EntityCount())
	}

	// Past the floored deadline (boot + the 30s interval) the dead producer's
	// entity IS reaped — the memento still closes the #139 zombie.
	now = boot.Add(31 * time.Second)
	if n := e2.Sweep(); n != 1 {
		t.Fatalf("post-grace sweep expired %d, want 1 (the dead producer's entity)", n)
	}
	if g2.EntityCount() != 0 {
		t.Errorf("EntityCount = %d after sweep, want 0", g2.EntityCount())
	}

	// Without the memento (pre-fix behavior): the zombie survives every sweep.
	g3 := projection.New()
	for _, ev := range g.SnapshotEvents(t0) {
		g3.Apply(ev)
	}
	e3 := New(g3, &fakeAppender{}, WithClock(func() time.Time { return now }))
	if n := e3.Sweep(); n != 0 {
		t.Fatalf("control: sweep without restored liveness expired %d, want 0", n)
	}
	if g3.EntityCount() != 1 {
		t.Error("control: the zombie must survive without the memento — that IS the bug")
	}

	// Empty blob (pre-#139 snapshot) is a clean no-op.
	if err := e2.RestoreLiveness(nil); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreLivenessOldBlobWithoutIntervals pins read-compatibility with
// blobs written before the interval maps existed: deadlines restore as stored
// (no floor is possible without the interval), so a lapsed one is swept on the
// first tick — the original #139 semantics.
func TestRestoreLivenessOldBlobWithoutIntervals(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{}, WithClock(func() time.Time { return now }))
	ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost,
		Identity: []model.KeyValue{kv("host.id", "legacy")},
		Interval: 30 * time.Second, Producer: "p1", EventTime: t0})
	if err != nil {
		t.Fatal(err)
	}

	g2 := projection.New()
	for _, sev := range g.SnapshotEvents(t0) {
		g2.Apply(sev)
	}
	now = t0.Add(5 * time.Minute)
	e2 := New(g2, &fakeAppender{}, WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))
	oldBlob := fmt.Sprintf(`{"refs":{%q:{"p1":%q}},"rel_deadlines":{}}`,
		string(ev.Entity.Entity.ID), t0.Add(30*time.Second).Format(time.RFC3339Nano))
	if err := e2.RestoreLiveness([]byte(oldBlob)); err != nil {
		t.Fatal(err)
	}
	if n := e2.Sweep(); n != 1 {
		t.Fatalf("sweep after old-blob restore expired %d, want 1", n)
	}
	if g2.EntityCount() != 0 {
		t.Errorf("EntityCount = %d, want 0", g2.EntityCount())
	}
}
