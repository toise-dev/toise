package change

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
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

func TestStateKeyClassification(t *testing.T) {
	e, _, _ := newEngine(t)
	ident := []model.KeyValue{kv("db.instance.id", "pg-1")}

	if ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeDatabase, Identity: ident,
		Attributes: []model.KeyValue{kv("replication.role", "primary"), kv("read_only", "false")}, EventTime: t0}); err != nil || ev.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("create = %v, %s", err, ev.Entity.ChangeType)
	}

	// failover: replication.role primary -> replica is a STATE change (AT4)
	ev, _ := e.ObserveEntity(EntityObservation{Type: model.TypeDatabase, Identity: ident,
		Attributes: []model.KeyValue{kv("replication.role", "replica"), kv("read_only", "false")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityStateChanged {
		t.Errorf("replication.role flip = %s, want entity.state_changed", ev.Entity.ChangeType)
	}
	if len(ev.Entity.ChangedKeys) != 1 || ev.Entity.ChangedKeys[0] != "replication.role" {
		t.Errorf("changed keys = %v, want [replication.role]", ev.Entity.ChangedKeys)
	}

	// read_only flip is also a STATE change
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeDatabase, Identity: ident,
		Attributes: []model.KeyValue{kv("replication.role", "replica"), kv("read_only", "true")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityStateChanged {
		t.Errorf("read_only flip = %s, want entity.state_changed", ev.Entity.ChangeType)
	}

	// a plain descriptive change (version, AT6) stays attribute_updated, not state
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeDatabase, Identity: ident,
		Attributes: []model.KeyValue{kv("replication.role", "replica"), kv("read_only", "true"), kv("db.system.version", "16.2")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityAttributeUpdated {
		t.Errorf("version add = %s, want entity.attribute_updated", ev.Entity.ChangeType)
	}
}

// TestStateKeysSet pins the membership of stateKeys against the producer contract.
// The contract declared hw.state state-bearing while the set did not contain it, so
// a failed power supply was indistinguishable from an edited description. A test on
// one key would not have caught that; only enumerating the set does.
// contractPath is the producer contract, relative to this package. The test below
// reads it, so moving or renaming the document means updating this constant — and
// the failure says exactly that rather than reporting a missing file.
const contractPath = "../../docs/data-model/senhub-agent-contract.md"

// The contract introduces its own list of state-bearing keys as the authoritative
// one ("a fixed recognized set with exact spellings"), so that list is the half the
// engine has to agree with. The keys are read from between that sentence's colon and
// the parenthesis that follows them.
var (
	stateBearingSentenceRe = regexp.MustCompile(`(?s)State-bearing keys are a fixed recognized set[^:]*:(.*?)\(note the`)
	backtickedRe           = regexp.MustCompile("`([^`]+)`")
)

// TestStateKeysMatchTheContract asserts the agreement TestStateKeysSet only asks
// for. That test pins the Go set and tells a human to go and update the document;
// nothing enforced the second half, and the two drifted apart in the very commit
// that added hw.state (#560) — the contract kept calling its enumeration the fixed
// recognized set while leaving the new key out of it.
func TestStateKeysMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("cannot read the producer contract at %s: %v\n"+
			"This test asserts that the contract's enumeration of state-bearing keys and\n"+
			"stateKeys in engine.go say the same thing. If the contract moved or was renamed,\n"+
			"point contractPath in this file at its new location. Do not delete the test: the\n"+
			"two halves have already drifted apart once without anyone noticing.", contractPath, err)
	}
	text := string(raw)
	loc := stateBearingSentenceRe.FindStringSubmatchIndex(text)
	if loc == nil {
		t.Fatalf("the state-bearing enumeration was not found in %s.\n"+
			"This test looks for the sentence starting \"State-bearing keys are a fixed\n"+
			"recognized set\" and reads the keys quoted between its colon and \"(note the\".\n"+
			"If that wording changed, update stateBearingSentenceRe in this file so the two\n"+
			"lists keep being compared. The comparison is what matters, not the wording.", contractPath)
	}
	line := 1 + strings.Count(text[:loc[0]], "\n")

	fromContract := make([]string, 0, len(stateKeys))
	for _, m := range backtickedRe.FindAllStringSubmatch(text[loc[2]:loc[3]], -1) {
		fromContract = append(fromContract, m[1])
	}
	sort.Strings(fromContract)

	fromEngine := make([]string, 0, len(stateKeys))
	for k := range stateKeys {
		fromEngine = append(fromEngine, k)
	}
	sort.Strings(fromEngine)

	if slices.Equal(fromEngine, fromContract) {
		return
	}
	t.Errorf("the contract and the change engine disagree on the state-bearing keys.\n"+
		"  engine   (internal/change/engine.go, stateKeys): %v\n"+
		"  contract (%s:%d): %v\n"+
		"  only in the engine:   %v\n"+
		"  only in the contract: %v\n"+
		"Fix whichever side is wrong, in the same commit as the change that caused it. A key\n"+
		"the engine matches and the contract omits hands a producer an incomplete set from the\n"+
		"document that calls itself authoritative; a key the contract lists and the engine does\n"+
		"not match never fires the entity.state_changed the contract promises.",
		fromEngine, contractPath, line, fromContract,
		missing(fromEngine, fromContract), missing(fromContract, fromEngine))
}

// missing returns the elements of a that b does not contain.
func missing(a, b []string) []string {
	out := []string{}
	for _, v := range a {
		if !slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}

func TestStateKeysSet(t *testing.T) {
	want := []string{"admin_state", "ha.role", "hw.state", "oper_state", "read_only", "replication.role", "status"}
	got := make([]string, 0, len(stateKeys))
	for k := range stateKeys {
		got = append(got, k)
	}
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("stateKeys = %v, want %v — a key added or removed here must also be declared\n"+
			"state-bearing in docs/data-model/senhub-agent-contract.md, or the contract and the\n"+
			"engine disagree in silence", got, want)
	}
}

// hw.state is the hardware health key of the OTel hw.* namespace. A component going
// ok -> failed is the event an operator is paged for; before it joined stateKeys it
// classified as entity.attribute_updated, the same class as a cosmetic edit.
func TestHARoleIsAStateChange(t *testing.T) {
	e, _, _ := newEngine(t)
	ident := []model.KeyValue{kv("service.instance.id", "keepalived:lb-1")}

	if ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeServiceInstance, Identity: ident,
		Attributes: []model.KeyValue{kv("ha.role", "active")}, EventTime: t0}); err != nil || ev.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("create = %v, %s", err, ev.Entity.ChangeType)
	}

	ev, _ := e.ObserveEntity(EntityObservation{Type: model.TypeServiceInstance, Identity: ident,
		Attributes: []model.KeyValue{kv("ha.role", "standby")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityStateChanged {
		t.Errorf("ha.role active -> standby = %s, want entity.state_changed", ev.Entity.ChangeType)
	}

	// ha.role and replication.role are two facts: a node can hold both, and a
	// change to one must not be read as a change to the other.
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeServiceInstance, Identity: ident,
		Attributes: []model.KeyValue{kv("ha.role", "standby"), kv("replication.role", "replica")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityStateChanged {
		t.Errorf("replication.role add = %s, want entity.state_changed", ev.Entity.ChangeType)
	}
	if len(ev.Entity.ChangedKeys) != 1 || ev.Entity.ChangedKeys[0] != "replication.role" {
		t.Errorf("changed keys = %v, want [replication.role] alone", ev.Entity.ChangedKeys)
	}
}

func TestHardwareStateIsAStateChange(t *testing.T) {
	e, _, _ := newEngine(t)
	ident := []model.KeyValue{kv("service.instance.id", "redfish:SN-1")}

	if ev, err := e.ObserveEntity(EntityObservation{Type: model.TypeServiceInstance, Identity: ident,
		Attributes: []model.KeyValue{kv("hw.state", "ok")}, EventTime: t0}); err != nil || ev.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("create = %v, %s", err, ev.Entity.ChangeType)
	}

	ev, _ := e.ObserveEntity(EntityObservation{Type: model.TypeServiceInstance, Identity: ident,
		Attributes: []model.KeyValue{kv("hw.state", "failed")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityStateChanged {
		t.Errorf("hw.state ok -> failed = %s, want entity.state_changed", ev.Entity.ChangeType)
	}
	if len(ev.Entity.ChangedKeys) != 1 || ev.Entity.ChangedKeys[0] != "hw.state" {
		t.Errorf("changed keys = %v, want [hw.state]", ev.Entity.ChangedKeys)
	}

	// a descriptive hw.* key stays an attribute update: only health is state.
	ev, _ = e.ObserveEntity(EntityObservation{Type: model.TypeServiceInstance, Identity: ident,
		Attributes: []model.KeyValue{kv("hw.state", "failed"), kv("hw.firmware_version", "2.1")}, EventTime: t0})
	if ev.Entity.ChangeType != model.EntityAttributeUpdated {
		t.Errorf("hw.firmware_version add = %s, want entity.attribute_updated", ev.Entity.ChangeType)
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
	mustObserve(t, e, model.TypeHost, []model.KeyValue{kv("host.id", "h2")}) // unrelated to the ghost edge
	// Its endpoint shows up after the TTL: the edge is past its deadline, so the
	// flush this triggers drops it rather than reconciling it.
	mustObserve(t, e, model.TypeProcess, []model.KeyValue{kv("process.executable.name", "ghost")})
	if g.RelationCount() != 1 {
		t.Errorf("expired edge must not reconcile later; count=%d, want 1", g.RelationCount())
	}
}

// A parked edge is retried only when an entity it actually waits on arrives;
// unrelated observations in between neither reconcile nor drop it, and the edge
// still reconciles once its own endpoints show up (#166 P2 — flushPending
// indexed by waited-on endpoint hash).
func TestRelationBufferIgnoresUnrelatedEntity(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithRelationBuffer(time.Minute),
		WithLogger(slog.New(slog.DiscardHandler)))

	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "nginx")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	edge := RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}
	if _, _, err := e.ObserveRelation(edge); err != nil {
		t.Fatal(err)
	}

	// Unrelated entities the edge does not wait on: still parked, not applied.
	mustObserve(t, e, model.TypeHost, []model.KeyValue{kv("host.id", "other")})
	mustObserve(t, e, model.TypeProcess, []model.KeyValue{kv("process.executable.name", "other")})
	if g.RelationCount() != 0 {
		t.Fatalf("edge should still be parked after unrelated observations; count=%d", g.RelationCount())
	}

	// Its own endpoints arrive within the TTL: it reconciles.
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	mustObserve(t, e, model.TypeHost, hostRef.Identity)
	if g.RelationCount() != 1 {
		t.Fatalf("edge should reconcile once its endpoints exist; count=%d", g.RelationCount())
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
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Fatalf("premature expiry: swept %d", n.Total())
	}

	// a fresh heartbeat resets the deadline
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host1, Interval: time.Minute, EventTime: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second) // 75s since first sight, but only 45s since the heartbeat
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Fatalf("heartbeat should have reset the deadline; swept %d", n.Total())
	}

	// let it lapse: the stale entity expires, the interval-less one survives
	now = now.Add(time.Minute)
	if n, _ := e.Sweep(); n.Total() != 1 {
		t.Fatalf("stale entity should expire; swept %d, want 1", n.Total())
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
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Errorf("re-sweep should be a no-op; swept %d", n.Total())
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
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Fatalf("db must survive while B's heartbeat is fresh; swept %d", n.Total())
	}
	if _, found := g.MatchIdentity(model.TypeDatabase, ident); !found {
		t.Error("db must survive A's lapse while B heartbeats")
	}
	// past B's deadline too: the db expires
	now = now.Add(40 * time.Second) // t0+130s, past B's t0+110
	if n, _ := e.Sweep(); n.Total() != 1 {
		t.Fatalf("db must expire once all producers lapse; swept %d, want 1", n.Total())
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
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Fatalf("premature edge expiry: swept %d", n.Total())
	}
	// re-asserting (even unchanged) resets the deadline
	edge.EventTime = now
	if _, _, err := e.ObserveRelation(edge); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second)
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Fatalf("heartbeat should reset the edge deadline; swept %d", n.Total())
	}
	now = now.Add(time.Minute)
	if n, _ := e.Sweep(); n.Total() != 1 {
		t.Fatalf("stale edge should expire; swept %d, want 1", n.Total())
	}
	if g.RelationCount() != 0 {
		t.Errorf("expired edge should be removed; count=%d", g.RelationCount())
	}
	if n, _ := e.Sweep(); n.Total() != 0 {
		t.Errorf("re-sweep should be a no-op; swept %d", n.Total())
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

// TestSweepExpiresInOneAppend pins C1/C5: a mass expiry (many entities and their
// edges lapse in the same sweep) commits through a single durable batch — one
// fsync — instead of one append per expired record, so the sweep can't stall
// ingestion in proportion to how much died at once.
func TestSweepExpiresInOneAppend(t *testing.T) {
	g := projection.New()
	ap := &countingAppender{}
	now := t0
	e := New(g, ap, WithClock(func() time.Time { return now }), WithLogger(slog.New(slog.DiscardHandler)))

	const hosts = 5
	for i := 0; i < hosts; i++ {
		hid := fmt.Sprintf("h%d", i)
		hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", hid)}}
		procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("host.id", hid), kv("process.pid", "1")}}
		if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, Interval: time.Minute, EventTime: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeProcess, Identity: procRef.Identity, Interval: time.Minute, EventTime: now}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: now}); err != nil {
			t.Fatal(err)
		}
	}

	// All liveness intervals lapse together; the whole graph expires in one sweep.
	now = now.Add(2 * time.Minute)
	ap.calls, ap.events = 0, 0
	n, err := e.Sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ap.calls != 1 {
		t.Errorf("Append calls = %d, want 1 (one fsync for the whole mass expiry)", ap.calls)
	}
	// 2*hosts entity.deleted + hosts relation.removed.
	if want := 3 * hosts; ap.events != want {
		t.Errorf("events appended = %d, want %d", ap.events, want)
	}
	if n.Total() != 3*hosts {
		t.Errorf("expired count = %d, want %d", n, 3*hosts)
	}
	if g.EntityCount() != 0 || g.RelationCount() != 0 {
		t.Errorf("graph after sweep = %d entities, %d relations, want 0 and 0", g.EntityCount(), g.RelationCount())
	}
}

// TestSweepReturnsCommitError pins #166: Sweep no longer swallows a failed
// expiry — it logs and keeps going (resilient), but returns the error so the
// maintenance metric's "error" outcome is reachable instead of impossible.
func TestSweepReturnsCommitError(t *testing.T) {
	g := projection.New()
	ap := &failingAppender{}
	now := t0
	e := New(g, ap, WithClock(func() time.Time { return now }), WithLogger(slog.New(slog.DiscardHandler)))

	host := []model.KeyValue{kv("host.id", "h1")}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Interval: time.Minute, EventTime: now}); err != nil {
		t.Fatal(err)
	}
	// Past the interval, with appends now failing: the expiry commit fails.
	now = now.Add(2 * time.Minute)
	ap.fail = true
	n, err := e.Sweep()
	if err == nil {
		t.Fatal("Sweep must return the commit error, not swallow it")
	}
	if n.Total() != 0 {
		t.Errorf("n = %d, want 0 (the expiry did not commit)", n.Total())
	}
	// The failed expiry leaves the entity live so the next sweep retries it.
	if _, found := g.MatchIdentity(model.TypeHost, host); !found {
		t.Error("a failed expiry must leave the entity live to retry")
	}
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
	_, _ = e.Sweep()
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
	if n, _ := e2.Sweep(); n.Total() != 0 {
		t.Fatalf("sweep right after boot expired %d, want 0 (one-interval boot grace)", n.Total())
	}
	if g2.EntityCount() != 1 {
		t.Fatalf("EntityCount = %d after boot-time sweep, want 1", g2.EntityCount())
	}

	// Past the floored deadline (boot + the 30s interval) the dead producer's
	// entity IS reaped — the memento still closes the #139 zombie.
	now = boot.Add(31 * time.Second)
	if n, _ := e2.Sweep(); n.Total() != 1 {
		t.Fatalf("post-grace sweep expired %d, want 1 (the dead producer's entity)", n.Total())
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
	if n, _ := e3.Sweep(); n.Total() != 0 {
		t.Fatalf("control: sweep without restored liveness expired %d, want 0", n.Total())
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
	if n, _ := e2.Sweep(); n.Total() != 1 {
		t.Fatalf("sweep after old-blob restore expired %d, want 1", n.Total())
	}
	if g2.EntityCount() != 0 {
		t.Errorf("EntityCount = %d, want 0", g2.EntityCount())
	}
}

// TestDeleteSourceProvenance pins the author attribution on every
// disappearance path: an explicit producer delete, a liveness expiry, the
// cascade on incident edges, and an absence-driven relation removal. Without
// the field, an expiry and a reason-less producer delete are indistinguishable
// on every read surface (senhub-agent #454).
func TestDeleteSourceProvenance(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))
	var recs []record
	e.Subscribe(func(ev model.Event, hp bool) { recs = append(recs, record{ev, hp}) })

	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.executable.name", "nginx")}}

	// Explicit producer delete.
	mustObserve(t, e, model.TypeHost, hostRef.Identity)
	ev, emitted, err := e.DeleteEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, EventTime: t0})
	if err != nil || !emitted {
		t.Fatalf("producer delete: emitted=%v err=%v", emitted, err)
	}
	if got := ev.Entity.DeleteSource; got != model.DeleteSourceProducer {
		t.Errorf("producer delete source = %q, want producer", got)
	}

	// Liveness expiry of an entity, and the cascade on its incident edge.
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	if _, oerr := e.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: hostRef.Identity, Interval: time.Minute, EventTime: t0}); oerr != nil {
		t.Fatal(oerr)
	}
	if _, em, rerr := e.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}); rerr != nil || !em {
		t.Fatalf("relation add: emitted=%v err=%v", em, rerr)
	}
	recs = recs[:0]
	now = now.Add(2 * time.Minute)
	if n, serr := e.Sweep(); serr != nil || n.Total() != 2 {
		t.Fatalf("sweep expired %d (err=%v), want 2 (host + cascaded edge)", n, serr)
	}
	var sawExpiry, sawCascade bool
	for _, r := range recs {
		if r.ev.Entity != nil && r.ev.Entity.ChangeType == model.EntityDeleted {
			sawExpiry = true
			if got := r.ev.Entity.DeleteSource; got != model.DeleteSourceLivenessExpiry {
				t.Errorf("expired entity source = %q, want liveness_expiry", got)
			}
			if r.ev.Entity.DeleteReason != "" {
				t.Errorf("expiry must not fabricate a producer delete_reason, got %q", r.ev.Entity.DeleteReason)
			}
		}
		if r.ev.Relation != nil && r.ev.Relation.ChangeType == model.RelationRemoved {
			sawCascade = true
			if got := r.ev.Relation.DeleteSource; got != model.DeleteSourceCascade {
				t.Errorf("cascaded edge source = %q, want cascade", got)
			}
		}
	}
	if !sawExpiry || !sawCascade {
		t.Fatalf("expected an expired entity and a cascaded edge, saw expiry=%v cascade=%v", sawExpiry, sawCascade)
	}

	// Absence/explicit relation removal is producer-authored.
	mustObserve(t, e, model.TypeProcess, procRef.Identity)
	mustObserve(t, e, model.TypeHost, hostRef.Identity)
	if _, em, rerr := e.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0}); rerr != nil || !em {
		t.Fatalf("relation re-add: emitted=%v err=%v", em, rerr)
	}
	rev, emitted, err := e.RemoveRelation(RelationObservation{Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0})
	if err != nil || !emitted {
		t.Fatalf("relation remove: emitted=%v err=%v", emitted, err)
	}
	if got := rev.Relation.DeleteSource; got != model.DeleteSourceProducer {
		t.Errorf("absence removal source = %q, want producer", got)
	}
}

// TestAChangeNamesTheCollectionMethodThatSawIt is #394's expensive half.
//
// The producer contract puts provenance on the instrumentation scope — one per
// collection method — and no read surface could reach it. The question that
// exposed it: 1312 liveness expirations in twelve hours, and the producer needed
// to know WHICH collection method was losing its entities in order to look in
// the right place in their code. That was unanswerable.
//
// The hard part is the expiry: Toise authors that event, so the scope has to
// survive on the liveness reference and be read back when it lapses, or the
// collector that went silent dies with the reference.
func TestAChangeNamesTheCollectionMethodThatSawIt(t *testing.T) {
	const scope = "senhub-agent/snmp-route"
	now := fixedNow()
	g := projection.New()
	e := New(g, &fakeAppender{}, WithClock(func() time.Time { return now }))
	var recs []record
	e.Subscribe(func(ev model.Event, hp bool) { recs = append(recs, record{ev, hp}) })

	if err := e.Batch(func(b *Batch) {
		b.SetScope(scope)
		if _, err := b.ObserveEntity(EntityObservation{
			Type:      model.TypeHost,
			Identity:  []model.KeyValue{kv("host.id", "h1")},
			EventTime: now,
			Producer:  "agent-a",
			Interval:  time.Minute,
		}); err != nil {
			t.Errorf("observe: %v", err)
		}
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}

	var created *model.EntityEvent
	for _, r := range recs {
		if r.ev.Entity != nil && r.ev.Entity.ChangeType == model.EntityCreated {
			created = r.ev.Entity
		}
	}
	if created == nil {
		t.Fatal("no creation event")
	}
	if created.Scope != scope {
		t.Errorf("creation scope = %q, want %q; a producer-authored change must name its collector", created.Scope, scope)
	}

	// Let the reference lapse. The expiry is authored by Toise, and it must still
	// name the collector that stopped speaking — not nothing, and not Toise.
	now = now.Add(2 * time.Minute)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	var expired *model.EntityEvent
	for _, r := range recs {
		if r.ev.Entity != nil && r.ev.Entity.DeleteSource == model.DeleteSourceLivenessExpiry {
			expired = r.ev.Entity
		}
	}
	if expired == nil {
		t.Fatal("the sweep expired nothing; setup is wrong")
	}
	if expired.Scope != scope {
		t.Errorf("expiry scope = %q, want %q — the collector that went silent is the whole point of the field", expired.Scope, scope)
	}
}

// TestAssertingScopesAnswersHowNotWho completes #394's second ask.
//
// AssertingProducers answers "who sends this", at the resource grain the
// reference counting is keyed on. A producer debugging a collector needs "how
// was it collected": an entity seen by both snmp-route and snmp-lldp is a
// different situation from one seen by snmp-route from two agents, and the two
// cases were indistinguishable.
func TestAssertingScopesAnswersHowNotWho(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
	ident := []model.KeyValue{kv("host.id", "h1")}

	var id model.EntityID
	observe := func(producer, scope string) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			ev, err := b.ObserveEntity(EntityObservation{
				Type: model.TypeHost, Identity: ident, EventTime: now,
				Producer: producer, Interval: time.Hour,
			})
			if err != nil {
				t.Errorf("observe: %v", err)
				return
			}
			id = ev.Entity.Entity.ID
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}

	// Two agents, ONE collection method: two producers, one scope.
	observe("agent-a", "senhub-agent/snmp-route")
	observe("agent-b", "senhub-agent/snmp-route")

	if got := e.AssertingProducers(id); len(got) != 2 {
		t.Errorf("producers = %v, want two: the resource grain must still see both agents", got)
	}
	if got := e.AssertingScopes(id); len(got) != 1 || got[0] != "senhub-agent/snmp-route" {
		t.Errorf("scopes = %v, want one senhub-agent/snmp-route: one method seen twice is not two methods", got)
	}

	// A second method on the same entity is the case that matters.
	observe("agent-a", "senhub-agent/snmp-lldp")
	got := e.AssertingScopes(id)
	if len(got) != 2 || got[0] != "senhub-agent/snmp-lldp" || got[1] != "senhub-agent/snmp-route" {
		t.Errorf("scopes = %v, want both methods sorted", got)
	}
	// And read why that passed: agent-b still holds snmp-route. It is NOT evidence
	// that one producer can carry two scopes — see the next test, which is the
	// shape a real multi-method agent has.
}

// TestOneProducerNamesEveryMethodAsserting is the test that replaced the one
// pinning the opposite, and the old one's failure message asked for exactly this.
//
// The reference key was the producer, so a producer asserting one entity through
// two collection methods overwrote its own reference: AssertingScopes reported
// whichever method spoke last and flipped at the cadence of the two. An agent
// running six collectors — the shape senhub-agent 0.6.4 has — got a single
// rotating value where the consumer needs the set.
//
// The key is now the speaker, so each method holds its own reference. Both
// halves are asserted here, because reporting the set of methods while
// reporting the producer twice would trade one wrong answer for another.
func TestOneProducerNamesEveryMethodAsserting(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
	ident := []model.KeyValue{kv("host.id", "h-multi")}

	var id model.EntityID
	observe := func(scope string) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			ev, err := b.ObserveEntity(EntityObservation{
				Type: model.TypeHost, Identity: ident, EventTime: now,
				Producer: "agent-a", Interval: time.Hour,
			})
			if err != nil {
				t.Errorf("observe: %v", err)
				return
			}
			id = ev.Entity.Entity.ID
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}

	observe("senhub-agent/host-arp")
	observe("senhub-agent/snmp-lldp")

	if got := e.AssertingProducers(id); len(got) != 1 || got[0] != "agent-a" {
		t.Errorf("producers = %v, want the one agent named once — several methods of "+
			"one producer are several references but one producer", got)
	}
	got := e.AssertingScopes(id)
	if len(got) != 2 || got[0] != "senhub-agent/host-arp" || got[1] != "senhub-agent/snmp-lldp" {
		t.Errorf("scopes = %v, want both methods sorted; one value here means the "+
			"methods are overwriting each other's reference again", got)
	}
}

// TestOneMethodRetractionLeavesAnotherMethodsEdge is the measurement that decided
// the key, reproduced as a control.
//
// Scoping the reconciler's diff state per collection method (#457) fixed the case
// where two methods see DIFFERENT things: each now diffs against its own last
// assertion, so one method's complete set no longer retracts another's edges. It
// left the case where they see the SAME thing. The engine's reference key was
// still the producer, so one method losing sight of an edge released the whole
// producer's reference and deleted an edge another method was still asserting —
// which re-created it on its next cycle. The flap moved rather than went away.
//
// With six collectors whose coverage overlaps by construction (host ARP and SNMP
// ARP see the same neighbors, LLDP and CDP the same links), an edge asserted by
// two methods of one agent is the normal case and not the corner.
func TestOneMethodRetractionLeavesAnotherMethodsEdge(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))

	left := EndpointRef{Type: model.TypeNetworkInterface, Identity: []model.KeyValue{kv("network.interface.name", "eth0")}}
	right := EndpointRef{Type: model.TypeNetworkInterface, Identity: []model.KeyValue{kv("network.interface.name", "Gi1/0/1")}}

	var relID model.RelationID
	assertEdge := func(scope string) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			for _, ep := range []EndpointRef{left, right} {
				if _, err := b.ObserveEntity(EntityObservation{
					Type: ep.Type, Identity: ep.Identity, EventTime: now,
					Producer: "agent-a", Interval: time.Hour,
				}); err != nil {
					t.Errorf("observe %s: %v", ep.Type, err)
					return
				}
			}
			ev, _, err := b.ObserveRelation(RelationObservation{
				Type: model.RelConnectedTo, From: left, To: right, EventTime: now,
				Producer: "agent-a", Interval: time.Hour,
			})
			if err != nil {
				t.Errorf("observe relation: %v", err)
				return
			}
			if ev.Relation != nil {
				relID = ev.Relation.Relation.ID
			}
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}

	// The same link, stated by an adjacency protocol and inferred from a
	// forwarding table, on one agent.
	assertEdge("senhub-agent/snmp-lldp")
	assertEdge("senhub-agent/snmp-fdb")
	if relID == "" {
		t.Fatal("no relation id captured")
	}
	if got := e.RelationAssertingScopes(relID); len(got) != 2 {
		t.Fatalf("edge scopes = %v, want both methods — the rest of this test means "+
			"nothing if they are not both holding a reference", got)
	}

	// LLDP stops seeing the link. The reconciler, scoped per method, emits a
	// removal for LLDP alone.
	retract := func(scope string) (emitted bool) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			_, ok, err := b.RemoveRelation(RelationObservation{
				Type: model.RelConnectedTo, From: left, To: right,
				EventTime: now, Producer: "agent-a",
			})
			if err != nil {
				t.Errorf("remove: %v", err)
				return
			}
			emitted = ok
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
		return emitted
	}

	if retract("senhub-agent/snmp-lldp") {
		t.Error("one method's retraction emitted relation.removed while another method still asserts the edge")
	}
	if _, ok := e.getRelation(relID); !ok {
		t.Fatal("the edge is gone although snmp-fdb still asserts it: the reference key is not the speaker")
	}
	if got := e.RelationAssertingScopes(relID); len(got) != 1 || got[0] != "senhub-agent/snmp-fdb" {
		t.Errorf("edge scopes after the retraction = %v, want only the method that still asserts it", got)
	}

	// The negative control, without which the above would pass on an engine that
	// simply never retracts: the last speaker's retraction does remove the edge.
	if !retract("senhub-agent/snmp-fdb") {
		t.Error("the last method's retraction emitted nothing: absence within a method must still retract")
	}
	if _, ok := e.getRelation(relID); ok {
		t.Error("the edge survived its last speaker's retraction")
	}
}

// TestOneMethodRetractionLeavesTheEntity is the entity-side half of the same
// rule, and it is not implied by the edge one: the two release paths are
// different code.
func TestOneMethodRetractionLeavesTheEntity(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
	ident := []model.KeyValue{kv("host.id", "h-two-methods")}

	var id model.EntityID
	observe := func(scope string) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			ev, err := b.ObserveEntity(EntityObservation{
				Type: model.TypeHost, Identity: ident, EventTime: now,
				Producer: "agent-a", Interval: time.Hour,
			})
			if err != nil {
				t.Errorf("observe: %v", err)
				return
			}
			id = ev.Entity.Entity.ID
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}
	remove := func(scope string) (emitted bool) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			_, ok, err := b.DeleteEntity(EntityObservation{
				Type: model.TypeHost, Identity: ident, EventTime: now, Producer: "agent-a",
			})
			if err != nil {
				t.Errorf("delete: %v", err)
				return
			}
			emitted = ok
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
		return emitted
	}

	observe("senhub-agent/host-arp")
	observe("senhub-agent/snmp-arp")

	if remove("senhub-agent/host-arp") {
		t.Error("one method's delete emitted entity.deleted while another method still asserts the entity")
	}
	if _, ok, deleted := e.getEntity(id); !ok || deleted {
		t.Fatal("the entity is gone although snmp-arp still asserts it")
	}
	if got := e.AssertingScopes(id); len(got) != 1 || got[0] != "senhub-agent/snmp-arp" {
		t.Errorf("scopes after the delete = %v, want only the method that still asserts it", got)
	}
	if !remove("senhub-agent/snmp-arp") {
		t.Error("the last method's delete emitted nothing: a producer must still be able to retract")
	}
}

// TestScopelessSpeakerKeysOnTheProducerAlone pins the encoding that makes this
// change migration-free, and it is the reason there is no migration code to
// review.
//
// A speaker that names no collection method keys on the producer alone, so the
// key is byte-identical to what the per-producer form wrote. A liveness blob from
// an earlier build therefore restores with no special case, and a producer that
// sends no instrumentation scope behaves exactly as it did before. If that ever
// stops holding, every stored deadline silently moves to a key nothing refreshes
// and a fleet's entities expire one interval after an upgrade.
func TestScopelessSpeakerKeysOnTheProducerAlone(t *testing.T) {
	if got := refKey("agent-a", ""); got != "agent-a" {
		t.Errorf("refKey with no method = %q, want the bare producer", got)
	}
	if got := refKey("", ""); got != "" {
		t.Errorf("refKey of the anonymous producer = %q, want empty", got)
	}
	p, s := splitRefKey("agent-a")
	if p != "agent-a" || s != "" {
		t.Errorf("splitting a pre-existing key = (%q, %q), want the producer and no method", p, s)
	}
	p, s = splitRefKey(refKey("agent-a", "senhub-agent/snmp-lldp"))
	if p != "agent-a" || s != "senhub-agent/snmp-lldp" {
		t.Errorf("round trip = (%q, %q)", p, s)
	}
	// A producer id may not forge another speaker's key. Both halves are strings a
	// producer chooses, so this is the property that has to hold by construction
	// rather than by assuming neither contains the separator.
	for _, forged := range []string{"agent-a\x00x", "\x007\x00agent-ax", "\x00", "\x00abc\x00agent-a", "\x001\x00a"} {
		if refKey(forged, "") == refKey("agent-a", "x") {
			t.Errorf("producer id %q keys identically to (agent-a, x)", forged)
		}
		if p, sc := splitRefKey(refKey(forged, "")); p != forged || sc != "" {
			t.Errorf("round trip of producer id %q = (%q, %q)", forged, p, sc)
		}
	}
	// And the method side of the same question: two different speakers never
	// share a key, whatever the split between the two halves.
	seen := map[string]string{}
	for _, sp := range [][2]string{
		{"agent-a", "x"}, {"agent-ax", ""}, {"agent", "ax"}, {"agent-a", ""},
		{"", "x"}, {"", ""}, {"\x00a", ""}, {"\x00a", "x"}, {"a", "\x00x"},
	} {
		k := refKey(sp[0], sp[1])
		if prev, dup := seen[k]; dup {
			t.Errorf("speakers %v and %s share the key %q", sp, prev, k)
		}
		seen[k] = fmt.Sprintf("%v", sp)
		if p, sc := splitRefKey(k); p != sp[0] || sc != sp[1] {
			t.Errorf("round trip of %v = (%q, %q)", sp, p, sc)
		}
	}
}

// TestAForgedProducerIDCannotReleaseAnotherSpeakersReference states the
// consequence the key encoding exists to prevent, end to end, because the key
// comparison above would pass on any encoding that merely differs.
//
// A producer must not be able to release a reference it never declared (ADR
// 0019). The producer id and the collection method are both producer-chosen
// strings, so a key built by joining them on a separator is injective only as
// long as no producer sends that separator — and then a crafted id releases a
// chosen speaker's reference, deleting an entity that speaker still asserts.
func TestAForgedProducerIDCannotReleaseAnotherSpeakersReference(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
	ident := []model.KeyValue{kv("host.id", "h-forge")}

	var id model.EntityID
	if err := e.Batch(func(b *Batch) {
		b.SetScope("senhub-agent/snmp-lldp")
		ev, err := b.ObserveEntity(EntityObservation{
			Type: model.TypeHost, Identity: ident, EventTime: now,
			Producer: "agent-a", Interval: time.Hour,
		})
		if err != nil {
			t.Errorf("observe: %v", err)
			return
		}
		id = ev.Entity.Entity.ID
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}

	// A second producer whose id is the first producer's id with the separator and
	// the method name appended, retracting under no method of its own.
	if err := e.Batch(func(b *Batch) {
		if _, emitted, err := b.DeleteEntity(EntityObservation{
			Type: model.TypeHost, Identity: ident, EventTime: now,
			Producer: "agent-a\x00senhub-agent/snmp-lldp",
		}); err != nil {
			t.Errorf("delete: %v", err)
		} else if emitted {
			t.Error("a crafted producer id retracted an entity it never declared")
		}
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}

	if _, ok, deleted := e.getEntity(id); !ok || deleted {
		t.Fatal("the entity was deleted by a producer that never asserted it")
	}
	if got := e.AssertingScopes(id); len(got) != 1 || got[0] != "senhub-agent/snmp-lldp" {
		t.Errorf("scopes = %v, want the real speaker's method still holding its reference", got)
	}
}

// TestEdgeProvenanceIsReadable pins #455's first half: an edge now says who
// asserts it and how it was collected, which it did not before.
//
// Two edges of one type are not the same claim — an adjacency protocol states a
// link, a forwarding table lets one be inferred — and a consumer drawing a map
// has to tell a measurement from a correlation. The engine held both answers in
// its reference counts and no read surface returned either.
func TestEdgeProvenanceIsReadable(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))

	left := EndpointRef{Type: model.TypeNetworkInterface, Identity: []model.KeyValue{kv("network.interface.name", "eth0")}}
	right := EndpointRef{Type: model.TypeNetworkInterface, Identity: []model.KeyValue{kv("network.interface.name", "Gi1/0/1")}}

	var relID model.RelationID
	assert := func(producer, scope string) {
		t.Helper()
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			if _, err := b.ObserveEntity(EntityObservation{
				Type: left.Type, Identity: left.Identity, EventTime: now, Producer: producer, Interval: time.Hour,
			}); err != nil {
				t.Errorf("observe left: %v", err)
				return
			}
			if _, err := b.ObserveEntity(EntityObservation{
				Type: right.Type, Identity: right.Identity, EventTime: now, Producer: producer, Interval: time.Hour,
			}); err != nil {
				t.Errorf("observe right: %v", err)
				return
			}
			ev, _, err := b.ObserveRelation(RelationObservation{
				Type: model.RelConnectedTo, From: left, To: right, EventTime: now,
				Producer: producer, Interval: time.Hour,
			})
			if err != nil {
				t.Errorf("observe relation: %v", err)
				return
			}
			if ev.Relation != nil {
				relID = ev.Relation.Relation.ID
			}
		}); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}

	// The same link, stated by an adjacency protocol on one agent and inferred
	// from a forwarding table on another.
	assert("agent-a", "senhub-agent/snmp-lldp")
	assert("agent-b", "senhub-agent/snmp-fdb")

	if relID == "" {
		t.Fatal("no relation id captured")
	}
	if got := e.RelationAssertingProducers(relID); len(got) != 2 || got[0] != "agent-a" || got[1] != "agent-b" {
		t.Errorf("edge producers = %v, want both agents sorted", got)
	}
	got := e.RelationAssertingScopes(relID)
	if len(got) != 2 || got[0] != "senhub-agent/snmp-fdb" || got[1] != "senhub-agent/snmp-lldp" {
		t.Errorf("edge scopes = %v, want the stated and the inferred method, sorted", got)
	}
	// An edge nobody asserts answers nothing rather than guessing.
	if got := e.RelationAssertingScopes(model.RelationID("no-such-edge")); len(got) != 0 {
		t.Errorf("unknown edge = %v, want empty", got)
	}
}

// TestParkedEdgeKeepsTheScopeThatAssertedIt pins a regression the speaker key
// introduced, found by an independent review of the day it was merged.
//
// An edge whose endpoints have not arrived yet parks in the reconciliation
// buffer, and is committed later by whichever entity observation delivers the
// missing endpoint — a different ScopeLogs block, under a different collection
// method. The scope is batch state by design, so the commit used to pick up the
// FLUSHING scope.
//
// That was harmless while the liveness reference was keyed on the producer
// alone. Since the key became the speaker, the method that asserted the edge can
// no longer retract it: its removal looks for a key nobody wrote, finds nothing,
// and emits nothing — silently. And an embedded edge carries a zero deadline, so
// no sweep reclaims it; it lives as long as its endpoints do.
//
// The buffer is on by default in production with a 30 s TTL, and the producer
// emits one scope per collection method, so a route asserted by the route
// collector and completed by the interface collector is the ordinary case.
func TestParkedEdgeKeepsTheScopeThatAssertedIt(t *testing.T) {
	now := fixedNow()
	e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }),
		WithRelationBuffer(30*time.Second))

	host := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h-park")}}
	addr := EndpointRef{Type: model.TypeNetworkAddress, Identity: []model.KeyValue{kv("network.address", "10.0.0.254")}}

	// The route collector asserts the edge before the address exists: it parks.
	if err := e.Batch(func(b *Batch) {
		b.SetScope("senhub-agent/host-route")
		if _, err := b.ObserveEntity(EntityObservation{
			Type: host.Type, Identity: host.Identity, EventTime: now, Producer: "agent-a", Interval: time.Hour,
		}); err != nil {
			t.Errorf("observe host: %v", err)
			return
		}
		if _, emitted, err := b.ObserveRelation(RelationObservation{
			Type: model.RelHasInterface, From: host, To: addr, EventTime: now, Producer: "agent-a",
		}); err != nil {
			t.Errorf("observe relation: %v", err)
		} else if emitted {
			t.Error("the edge committed immediately; this test needs it to park")
		}
	}); err != nil {
		t.Fatalf("assert batch: %v", err)
	}

	// The interface collector delivers the address, which flushes the buffer.
	if err := e.Batch(func(b *Batch) {
		b.SetScope("senhub-agent/host-iface")
		if _, err := b.ObserveEntity(EntityObservation{
			Type: addr.Type, Identity: addr.Identity, EventTime: now, Producer: "agent-a", Interval: time.Hour,
		}); err != nil {
			t.Errorf("observe address: %v", err)
		}
	}); err != nil {
		t.Fatalf("flush batch: %v", err)
	}

	var relID model.RelationID
	for id := range e.relRefs {
		relID = id
	}
	if relID == "" {
		t.Fatal("the parked edge never landed")
	}

	if got := e.RelationAssertingScopes(relID); len(got) != 1 || got[0] != "senhub-agent/host-route" {
		t.Errorf("edge collected_by = %v, want the method that ASSERTED it; the flushing method "+
			"only completed its endpoints", got)
	}

	// And the consequence that makes it a defect rather than a wrong label: the
	// asserting method must be able to retract what it asserted.
	var emitted bool
	if err := e.Batch(func(b *Batch) {
		b.SetScope("senhub-agent/host-route")
		_, ok, err := b.RemoveRelation(RelationObservation{
			Type: model.RelHasInterface, From: host, To: addr, EventTime: now, Producer: "agent-a",
		})
		if err != nil {
			t.Errorf("remove: %v", err)
			return
		}
		emitted = ok
	}); err != nil {
		t.Fatalf("retract batch: %v", err)
	}
	if !emitted {
		t.Error("the asserting method's retraction emitted nothing: it cannot retract its own edge")
	}
	if _, still := e.getRelation(relID); still {
		t.Error("the edge survived its only speaker's retraction, and carries a zero deadline, so no sweep reclaims it")
	}

	// The negative control: the flushing scope must not be left set on the batch
	// afterwards, or every entity observed after a flush is mis-attributed.
	if err := e.Batch(func(b *Batch) {
		b.SetScope("senhub-agent/host-iface")
		if _, err := b.ObserveEntity(EntityObservation{
			Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h-after")},
			EventTime: now, Producer: "agent-a", Interval: time.Hour,
		}); err != nil {
			t.Errorf("observe after flush: %v", err)
		}
	}); err != nil {
		t.Fatalf("after batch: %v", err)
	}
	id, ok := e.matchIdentity(model.TypeHost, []model.KeyValue{kv("host.id", "h-after")})
	if !ok {
		t.Fatal("the host observed after the flush is missing")
	}
	if got := e.AssertingScopes(id); len(got) != 1 || got[0] != "senhub-agent/host-iface" {
		t.Errorf("a host observed after a flush has scopes %v, want its own batch scope — "+
			"the override must be restored, not left set", got)
	}
}

// TestLegacyReferenceIsReleasableAfterAnUpgrade pins the defect an independent
// review found in the speaker key, and the limit of its fix.
//
// A snapshot written before the key carried the collection method holds bare
// producer keys. After an upgrade, a producer retracting under one of its
// methods looked for a key nobody wrote: it found nothing and emitted nothing,
// silently. Every embedded edge existing at the upgrade — runs_on,
// has_interface, bound_to, listens_on, all of which carry a zero deadline — was
// therefore non-retractable by its own producer until its endpoints died.
//
// The claim "no snapshot migration" was true of the decoding and false of the
// semantics, which is why the blob now declares whether its keys carry a method:
// the keys themselves decode identically either way, so nothing else can tell a
// legacy reference from one whose producer named no method.
func TestLegacyReferenceIsReleasableAfterAnUpgrade(t *testing.T) {
	now := fixedNow()
	setup := func(t *testing.T, blob string) (*Engine, model.RelationID, EndpointRef, EndpointRef) {
		t.Helper()
		e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
		proc := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{
			kv("process.pid", "7"), kv("process.creation.time", "t0")}}
		host := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h-up")}}

		var relID model.RelationID
		if err := e.Batch(func(b *Batch) {
			for _, ep := range []EndpointRef{proc, host} {
				if _, err := b.ObserveEntity(EntityObservation{
					Type: ep.Type, Identity: ep.Identity, EventTime: now, Producer: "agent-a", Interval: time.Hour,
				}); err != nil {
					t.Errorf("observe: %v", err)
					return
				}
			}
			ev, _, err := b.ObserveRelation(RelationObservation{
				Type: model.RelRunsOn, From: proc, To: host, EventTime: now, Producer: "agent-a",
			})
			if err != nil {
				t.Errorf("observe relation: %v", err)
				return
			}
			if ev.Relation != nil {
				relID = ev.Relation.Relation.ID
			}
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// Replace the reference with what the given blob restores.
		e.relRefs[relID] = map[string]liveRef{}
		if err := e.RestoreLiveness([]byte(blob)); err != nil {
			t.Fatalf("restore: %v", err)
		}
		return e, relID, proc, host
	}

	retract := func(t *testing.T, e *Engine, proc, host EndpointRef, scope string) bool {
		t.Helper()
		var emitted bool
		if err := e.Batch(func(b *Batch) {
			b.SetScope(scope)
			_, ok, err := b.RemoveRelation(RelationObservation{
				Type: model.RelRunsOn, From: proc, To: host, EventTime: now, Producer: "agent-a",
			})
			if err != nil {
				t.Errorf("remove: %v", err)
				return
			}
			emitted = ok
		}); err != nil {
			t.Fatalf("retract: %v", err)
		}
		return emitted
	}

	t.Run("a pre-speaker-key snapshot is releasable", func(t *testing.T) {
		e, relID, proc, host := setup(t, `{"refs":{},"rel_refs":{"REL":{"agent-a":"`+
			now.Add(time.Hour).Format(time.RFC3339Nano)+`"}}}`)
		// The blob keys on the literal relation id, so write it under the real one.
		e.relRefs[relID] = map[string]liveRef{"agent-a": {legacy: true}}
		if !retract(t, e, proc, host, "senhub-agent/hostsvc") {
			t.Error("a scoped retraction released nothing: the producer cannot retract its own edge after an upgrade")
		}
		if _, still := e.getRelation(relID); still {
			t.Error("the edge survived its producer's retraction")
		}
	})

	t.Run("a method that named nothing is NOT released by another method", func(t *testing.T) {
		// The negative control, and the half that matters: a runtime reference
		// whose producer named no method is a speaker in its own right. Releasing
		// it from another method's retraction would let one speaker retract what
		// another asserted.
		e, relID, proc, host := setup(t, `{"refs":{}}`)
		e.relRefs[relID] = map[string]liveRef{"agent-a": {}} // not legacy
		if retract(t, e, proc, host, "senhub-agent/hostsvc") {
			t.Error("a scoped retraction released an unnamed method's reference: one speaker retracted another's assertion")
		}
		if _, still := e.getRelation(relID); !still {
			t.Error("the edge was removed although the unnamed method still asserts it")
		}
	})

	t.Run("the snapshot declares that its keys carry the method", func(t *testing.T) {
		e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
		blob, err := e.LivenessBlob()
		if err != nil {
			t.Fatalf("blob: %v", err)
		}
		if !strings.Contains(string(blob), `"speaker_keys":true`) {
			t.Errorf("a blob we write does not declare speaker keys, so our own snapshots would restore as legacy: %s", blob)
		}
	})
}

// TestABlobBetweenTheTwoChangesIsNotLegacy pins a defect in the fix for the
// opposite direction, found by reading the bench session's upgrade plan rather
// than by a test.
//
// The speaker key shipped before the snapshot declared it, so a build between
// the two wrote COMPOSITE keys without saying so. In such a blob a bare key
// means "this producer named no collection method" — not "the format recorded
// none". Marking those legacy would let a scoped retraction release a reference
// it never asserted: one speaker retracting another's assertion, which is the
// single thing ADR 0019's reference counting exists to prevent, and the exact
// violation the legacy fix was careful to avoid in the other direction.
//
// The bench was about to restore precisely such a blob — the same agent against
// 28626fc, then against the beta — so the confusing result would have been read
// as a beta regression.
//
// A single composite key anywhere proves the writer knew about methods.
func TestABlobBetweenTheTwoChangesIsNotLegacy(t *testing.T) {
	now := fixedNow()
	deadline := now.Add(time.Hour).Format(time.RFC3339Nano)

	for _, c := range []struct {
		name       string
		blob       string
		wantLegacy bool
	}{
		{
			name:       "no declaration and no composite key: written before the key change",
			blob:       `{"refs":{"E1":{"agent-a":"` + deadline + `"}}}`,
			wantLegacy: true,
		},
		{
			name: "no declaration but a composite key elsewhere: written between the two",
			blob: `{"refs":{"E1":{"agent-a":"` + deadline + `"},` +
				`"E2":{"\u00007\u0000agent-bsenhub-agent/snmp-lldp":"` + deadline + `"}}}`,
			wantLegacy: false,
		},
		{
			name:       "declared: written after both",
			blob:       `{"speaker_keys":true,"refs":{"E1":{"agent-a":"` + deadline + `"}}}`,
			wantLegacy: false,
		},
	} {
		e := New(projection.New(), &fakeAppender{}, WithClock(func() time.Time { return now }))
		if err := e.RestoreLiveness([]byte(c.blob)); err != nil {
			t.Fatalf("%s: restore: %v", c.name, err)
		}
		ref, ok := e.refs["E1"]["agent-a"]
		if !ok {
			t.Fatalf("%s: the bare reference was not restored", c.name)
		}
		if ref.legacy != c.wantLegacy {
			t.Errorf("%s: legacy = %v, want %v. A bare key beside a composite one is a producer "+
				"that named no method, not a format that recorded none — marking it legacy lets one "+
				"speaker release another's reference", c.name, ref.legacy, c.wantLegacy)
		}
	}
}

// TestImplausibleLivenessPromiseIsRefusedAndReported guards the production case
// that motivated the bound: a host declared 1347804h (153 years), asserted by a
// producer that sent nothing else and never spoke again. Believing it made the
// entity immortal — no deadline ever arrives — and made the read surface quote
// 153 years back as an upper bound on uncertainty, which is the most
// authoritative sentence this tool emits.
//
// The assertion is on all three consequences, because fixing one and not the
// others would still leave the defect usable: no deadline is armed, the entity
// has NO cadence rather than a false one, and the refusal is reported.
func TestImplausibleLivenessPromiseIsRefusedAndReported(t *testing.T) {
	var gotProducer, gotType string
	var gotDeclared time.Duration
	calls := 0

	g := projection.New()
	e := New(g, &fakeAppender{}, WithPromiseReporter(func(producer, entityType string, declared time.Duration) {
		calls++
		gotProducer, gotType, gotDeclared = producer, entityType, declared
	}))

	const declared = 1347804 * time.Hour // the value measured in production
	obs := EntityObservation{
		Type:      model.TypeHost,
		Identity:  []model.KeyValue{kv("host.id", "epreuve-0001")},
		EventTime: time.Now(),
		Producer:  "",
		Interval:  declared,
	}
	if _, err := e.ObserveEntity(obs); err != nil {
		t.Fatalf("observe: %v", err)
	}

	id, found := e.matchIdentity(obs.Type, obs.Identity)
	if !found {
		t.Fatal("the entity was dropped; a refused PROMISE must never cost the FACT (ADR 0022)")
	}

	if _, ok := e.ObservationInterval(id); ok {
		t.Error("the engine still reports a usable cadence, so the figure reaches the resolution block and reads as a bound")
	}
	for _, ref := range e.refs[id] {
		if !ref.deadline.IsZero() {
			t.Errorf("a deadline was armed at %v: the entity is immortal in practice, which is what the bound exists to stop", ref.deadline)
		}
	}

	declaredBack, producerBack, bad := e.ImplausiblePromise(id)
	if !bad {
		t.Fatal("the refusal is not recorded, so a read surface cannot tell this from an honest absence of promise")
	}
	if declaredBack != declared {
		t.Errorf("recorded %v, want the value as declared %v — the magnitude is the diagnosis", declaredBack, declared)
	}
	if producerBack != "" {
		t.Errorf("recorded producer %q, want it kept verbatim including empty", producerBack)
	}
	if calls != 1 {
		t.Errorf("reporter called %d times, want exactly 1: a log line wakes nobody, the report is the control", calls)
	}
	if gotDeclared != declared || gotType != model.TypeHost || gotProducer != "" {
		t.Errorf("reported (%q, %q, %v), want (%q, %q, %v)", gotProducer, gotType, gotDeclared, "", model.TypeHost, declared)
	}

	// Negative control: a plausible promise is untouched, and it clears a prior
	// refusal for the same entity rather than leaving it marked for ever.
	obs.Interval = time.Minute
	if _, err := e.ObserveEntity(obs); err != nil {
		t.Fatalf("re-observe: %v", err)
	}
	if iv, ok := e.ObservationInterval(id); !ok || iv != time.Minute {
		t.Errorf("plausible interval = (%v, %v), want (1m, true)", iv, ok)
	}
	if _, _, bad := e.ImplausiblePromise(id); bad {
		t.Error("the refusal survived a good observation, so a producer that fixes itself stays accused")
	}
	if calls != 1 {
		t.Errorf("reporter called %d times after a good observation, want still 1", calls)
	}
}

// TestAnonymousEntityIsReleasableOnlyAnonymously is the recipe for removing an
// entity nobody claims, verified rather than reasoned about, because an
// operator is going to run it against production.
//
// A production host sits in a customer graph asserted under an EMPTY producer
// id, with no liveness promise, created once in August and never spoken of
// again. Toise has no operator gesture to remove an entity — writes on the read
// surface are limited to annotations, and the only destructive server
// subcommand drops a whole tenant — so the only path is an explicit delete over
// the ingest rail.
//
// The trap this pins: a delete releases the reference of the producer that
// SENDS it. A named producer therefore leaves an anonymous reference untouched,
// the reference set stays non-empty, and the delete returns as a silent no-op —
// no event, no error, nothing in the change feed. Someone would try it three
// times and conclude the product was broken.
func TestAnonymousEntityIsReleasableOnlyAnonymously(t *testing.T) {
	g := projection.New()
	e := New(g, &fakeAppender{}, WithLogger(slog.New(slog.DiscardHandler)))

	ident := []model.KeyValue{kv("host.id", "epreuve-0001")}
	obs := EntityObservation{Type: model.TypeHost, Identity: ident, EventTime: t0, Producer: ""}
	if _, err := e.ObserveEntity(obs); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, found := e.matchIdentity(model.TypeHost, ident); !found {
		t.Fatal("the entity was not created, so the rest of this test proves nothing")
	}

	// A NAMED producer cannot release it: the key it targets does not exist.
	named := EntityObservation{Type: model.TypeHost, Identity: ident, EventTime: t0, Producer: "opspilot"}
	if _, emitted, err := e.DeleteEntity(named); err != nil || emitted {
		t.Errorf("delete from a named producer: emitted=%v err=%v; want a silent no-op", emitted, err)
	}
	if _, found := e.matchIdentity(model.TypeHost, ident); !found {
		t.Fatal("a named producer released an anonymous reference; the recipe below would be unnecessary")
	}

	// Only a delete carrying NO producer id matches the anonymous key.
	anon := EntityObservation{Type: model.TypeHost, Identity: ident, EventTime: t0, Producer: ""}
	ev, emitted, err := e.DeleteEntity(anon)
	if err != nil {
		t.Fatalf("anonymous delete: %v", err)
	}
	if !emitted {
		t.Fatal("the anonymous delete emitted nothing: an entity nobody claims would be removable by nobody at all")
	}
	if ev.Entity == nil || ev.Entity.ChangeType != model.EntityDeleted {
		t.Errorf("event = %+v, want entity.deleted", ev.Entity)
	}
	if ev.Entity != nil && ev.Entity.DeleteSource != model.DeleteSourceProducer {
		t.Errorf("delete_source = %q, want producer: a deliberate removal must not read as a producer going silent", ev.Entity.DeleteSource)
	}
	if _, found := e.matchIdentity(model.TypeHost, ident); found {
		t.Error("the entity survived its own anonymous delete")
	}
}

// TestDiscardedEdgeIsReportedWithBothEndpointTypes guards the figure that made
// this reporting necessary: a production tenant discarded 47,327 attached_to
// edges in ten days, every one of them, because the probe emitting their target
// was enabled nowhere. The graph showed nothing — no relation, no target, no
// sign a thing had been attempted — and the only trace was a WARN line found
// because someone was told which log to search.
//
// The endpoint TYPES are asserted, not just the relation type, because they are
// the diagnosis: the relation alone says an edge was lost, the pair says which
// half nothing emits.
func TestDiscardedEdgeIsReportedWithBothEndpointTypes(t *testing.T) {
	var got [][3]string
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithRelationBuffer(30*time.Second),
		WithDiscardReporter(func(relType, fromType, toType string) {
			got = append(got, [3]string{relType, fromType, toType})
		}),
		WithLogger(slog.New(slog.DiscardHandler)))

	// The shape measured in production: a container declares an attachment to a
	// segment nothing emits.
	edge := RelationObservation{
		Type:      model.RelAttachedTo,
		From:      EndpointRef{Type: model.TypeContainer, Identity: []model.KeyValue{kv("container.id", "c1")}},
		To:        EndpointRef{Type: model.TypeNetworkSegment, Identity: []model.KeyValue{kv("network.segment.id", "swarm:never-emitted")}},
		EventTime: t0,
	}
	if _, emitted, err := e.ObserveRelation(edge); err != nil || emitted {
		t.Fatalf("observe parked edge: emitted=%v err=%v; want parked silently", emitted, err)
	}
	if len(got) != 0 {
		t.Fatalf("reported %v while merely parked; a parked edge is not a lost one", got)
	}

	// Past the reconciliation window the edge is given up on.
	now = t0.Add(time.Minute)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("reported %d discards, want 1: %v", len(got), got)
	}
	want := [3]string{model.RelAttachedTo, model.TypeContainer, model.TypeNetworkSegment}
	if got[0] != want {
		t.Errorf("reported %v, want %v — without both endpoint types the figure says an edge was lost and not which half is missing", got[0], want)
	}
}

// TestBufferFullEvictionIsCounted covers the drop path that needs no TTL: with
// the parking buffer at its cap, the oldest parked edge is evicted immediately.
// It went uncounted, so the counter read zero under the one condition that
// loses edges FASTEST — a burst of out-of-order edges, which is also when an
// operator is least able to spot a WARN line in a flood.
//
// The bench asked what input makes an edge drop and could not make the counter
// move, which is how this was found.
func TestBufferFullEvictionIsCounted(t *testing.T) {
	var got [][3]string
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithRelationBuffer(time.Hour), // long, so nothing expires by TTL here
		WithDiscardReporter(func(relType, fromType, toType string) {
			got = append(got, [3]string{relType, fromType, toType})
		}),
		WithLogger(slog.New(slog.DiscardHandler)))

	park := func(n int) RelationObservation {
		return RelationObservation{
			Type:      model.RelAttachedTo,
			From:      EndpointRef{Type: model.TypeContainer, Identity: []model.KeyValue{kv("container.id", fmt.Sprintf("c%d", n))}},
			To:        EndpointRef{Type: model.TypeNetworkSegment, Identity: []model.KeyValue{kv("network.segment.id", "swarm:never-emitted")}},
			EventTime: t0,
		}
	}
	for i := 0; i < maxPendingRelations; i++ {
		if _, _, err := e.ObserveRelation(park(i)); err != nil {
			t.Fatalf("parking edge %d: %v", i, err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("reported %d discards while only filling the buffer: %v", len(got), got)
	}

	// One more than the cap evicts the oldest, with no clock movement at all.
	if _, _, err := e.ObserveRelation(park(maxPendingRelations)); err != nil {
		t.Fatalf("parking the overflowing edge: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reported %d discards after overflowing the cap, want 1: a load-induced loss is still a loss", len(got))
	}
	want := [3]string{model.RelAttachedTo, model.TypeContainer, model.TypeNetworkSegment}
	if got[0] != want {
		t.Errorf("reported %v, want %v", got[0], want)
	}
}

// TestEmbeddedEdgeInheritsItsSpeakersDeadline closes the unbounded half of #504,
// measured in production: an SNMP poller declared a shared interface gone from
// its view, the interface survived on its two other speakers, and the edge that
// poller had declared INSIDE that interface kept naming it as an asserter for as
// long as the endpoints lived. Not eight minutes — for ever, because an embedded
// edge carried a zero deadline and nothing sweeps one.
//
// The rule asserted here is the speaker's, not the entity's: an edge describes
// its source as THAT speaker observes it, so it lives exactly as long as that
// speaker's claim on the source.
func TestEmbeddedEdgeInheritsItsSpeakersDeadline(t *testing.T) {
	g := projection.New()
	now := t0
	e := New(g, &fakeAppender{},
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	hostRef := EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	procRef := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("process.pid", "7")}}

	// Two speakers assert the source process; only one declares the edge.
	for _, p := range []string{"agent-a", "agent-b"} {
		if _, err := e.ObserveEntity(EntityObservation{
			Type: procRef.Type, Identity: procRef.Identity, EventTime: t0, Producer: p, Interval: time.Minute,
		}); err != nil {
			t.Fatalf("observe source as %s: %v", p, err)
		}
	}
	if _, err := e.ObserveEntity(EntityObservation{
		Type: hostRef.Type, Identity: hostRef.Identity, EventTime: t0, Producer: "agent-a", Interval: time.Minute,
	}); err != nil {
		t.Fatalf("observe host: %v", err)
	}
	// An EMBEDDED edge: no interval of its own.
	if _, _, err := e.ObserveRelation(RelationObservation{
		Type: model.RelRunsOn, From: procRef, To: hostRef, EventTime: t0, Producer: "agent-a",
	}); err != nil {
		t.Fatalf("observe edge: %v", err)
	}

	var relID model.RelationID
	for id := range e.relRefs {
		relID = id
	}
	if relID == "" {
		t.Fatal("the edge never landed")
	}
	ref, ok := e.relRefs[relID][refKey("agent-a", "")]
	if !ok {
		t.Fatal("no reference for the speaker that declared the edge")
	}
	if ref.deadline.IsZero() {
		t.Fatal("the edge reference has a zero deadline: nothing will ever expire it, which is the unbounded half of #504")
	}
	if want := t0.Add(time.Minute); !ref.deadline.Equal(want) {
		t.Errorf("edge deadline = %v, want the speaker's own deadline on the source %v", ref.deadline, want)
	}

	// The speaker goes quiet while the OTHER keeps the source alive. The edge
	// must lapse with its speaker's claim, not outlive it.
	now = t0.Add(2 * time.Minute)
	if _, err := e.ObserveEntity(EntityObservation{
		Type: procRef.Type, Identity: procRef.Identity, EventTime: now, Producer: "agent-b", Interval: time.Minute,
	}); err != nil {
		t.Fatalf("re-observe source as agent-b: %v", err)
	}
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, still := e.relRefs[relID][refKey("agent-a", "")]; still {
		t.Error("the quiet speaker still asserts the edge, so via_asserted_by names a producer that stopped talking")
	}
	if _, alive := e.matchIdentity(procRef.Type, procRef.Identity); !alive {
		t.Error("the source died: the other speaker's claim must keep it alive")
	}
}

// TestRestoreRefusesAnImplausiblePromiseToo closes the half the bound missed:
// it guarded live observations, and a snapshot holding an impossible interval
// restored it intact. The entity stayed immortal and the resolution block
// quoted the figure back as a legitimate cadence, with nothing marking it as a
// defect — which is how the production host that motivated the bound survived
// restarts for 56 days.
//
// Found by thinking about the upgrade path the bench has to qualify, not by
// reading the diff.
func TestRestoreRefusesAnImplausiblePromiseToo(t *testing.T) {
	const declared = 1347804 * time.Hour

	// A blob as an earlier build would have written it: a real deadline and an
	// impossible interval beside it.
	blob, err := json.Marshal(livenessSnapshot{
		Refs:         map[string]map[string]time.Time{"01HOST": {"agent-a": t0.Add(declared)}},
		RefIntervals: map[string]map[string]time.Duration{"01HOST": {"agent-a": declared}},
		SpeakerKeys:  true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	calls := 0
	e := New(projection.New(), &fakeAppender{},
		WithClock(func() time.Time { return t0 }),
		WithPromiseReporter(func(string, string, time.Duration) { calls++ }),
		WithLogger(slog.New(slog.DiscardHandler)))
	if err := e.RestoreLiveness(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if _, ok := e.ObservationInterval("01HOST"); ok {
		t.Error("the restored figure is reported as a usable cadence, so it reaches the resolution block as a bound")
	}
	for _, ref := range e.refs["01HOST"] {
		if !ref.deadline.IsZero() {
			t.Errorf("a deadline was armed at %v from a refused promise: the entity is immortal across the restart", ref.deadline)
		}
	}
	if _, producer, bad := e.ImplausiblePromise("01HOST"); !bad || producer != "agent-a" {
		t.Errorf("the refusal is not recorded on restore (bad=%v producer=%q), so a reader cannot tell this from an honest absence of promise", bad, producer)
	}
	if calls != 1 {
		t.Errorf("reporter called %d times on restore, want 1: a restart must not silently clear the count", calls)
	}

	// The reference itself must survive — only the promise falls (ADR 0022).
	if len(e.refs["01HOST"]) != 1 {
		t.Errorf("the reference set holds %d speakers, want 1: refusing a promise must not drop the producer's claim", len(e.refs["01HOST"]))
	}
}
