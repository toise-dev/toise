package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func noHost(model.Event) (string, string) { return "", "" }

var (
	hostAID = model.EntityID("host-a")
	hostBID = model.EntityID("host-b")
	procID  = model.EntityID("proc-1")
	procBID = model.EntityID("proc-b")
	svcID   = model.EntityID("svc-1")
	ifaceID = model.EntityID("iface-1")
	lstID   = model.EntityID("listener-1")
	dualID  = model.EntityID("dual-1")
)

func hostEntity(id model.EntityID, hostID, hostName string) model.Entity {
	e := model.Entity{
		ID:       id,
		Type:     "host",
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(hostID)}},
	}
	if hostName != "" {
		e.Attributes = []model.KeyValue{{Key: "host.name", Value: model.StringValue(hostName)}}
	}
	return e
}

func newHostTestGraph() *fakeGraph {
	return &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			hostAID: hostEntity(hostAID, "a1", "alpha"),
			hostBID: hostEntity(hostBID, "b1", ""),
			procID:  {ID: procID, Type: "process", Identity: []model.KeyValue{{Key: "pid", Value: model.StringValue("123")}}},
			procBID: {ID: procBID, Type: "process", Identity: []model.KeyValue{{Key: "pid", Value: model.StringValue("456")}}},
			svcID:   {ID: svcID, Type: "service", Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("a1")}}},
			ifaceID: {ID: ifaceID, Type: "network.interface"},
			lstID:   {ID: lstID, Type: "service.listener"},
			dualID:  {ID: dualID, Type: "process"},
		},
		deleted: map[model.EntityID]bool{lstID: true},
		relations: []model.Relation{
			{ID: "r1", Type: "runs_on", From: procID, To: hostAID},
			{ID: "r2", Type: "has_interface", From: hostAID, To: ifaceID},
			{ID: "r3", Type: "runs_on", From: dualID, To: hostAID},
			{ID: "r4", Type: "runs_on", From: dualID, To: hostBID},
			{ID: "r5", Type: "runs_on", From: procBID, To: hostBID},
		},
	}
}

func TestHostOfViaRelation(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ref := hr.hostOf(procID, g.entities[procID])
	if ref.name != "alpha" || ref.fingerprint != g.entities[hostAID].IdentityHash() {
		t.Fatalf("got %+v", ref)
	}
}

func TestHostOfViaIdentity(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ref := hr.hostOf(svcID, g.entities[svcID])
	if ref.name != "alpha" || ref.fingerprint != g.entities[hostAID].IdentityHash() {
		t.Fatalf("got %+v", ref)
	}
}

func TestHostOfDeletedEntityViaEventEdge(t *testing.T) {
	g := newHostTestGraph()
	events := []model.Event{{
		Relation: &model.RelationEvent{
			Relation:   model.Relation{ID: "r9", Type: "listens_on", From: lstID, To: ifaceID},
			ChangeType: model.RelationRemoved,
			EventTime:  time.Now(),
		},
	}}
	hr := newHostResolver(g, events)
	ref := hr.hostOf(lstID, g.entities[lstID])
	if ref.name != "alpha" || ref.fingerprint != g.entities[hostAID].IdentityHash() {
		t.Fatalf("got %+v", ref)
	}
}

func TestHostOfAmbiguous(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ref := hr.hostOf(dualID, g.entities[dualID])
	if ref.name != "" || ref.fingerprint != "" || !ref.ambiguous {
		t.Fatalf("expected ambiguous empty ref, got %+v", ref)
	}
}

func TestHostOfHostItself(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	if ref := hr.hostOf(hostAID, g.entities[hostAID]); ref.name != "" || ref.fingerprint != "" {
		t.Fatalf("expected empty ref, got %+v", ref)
	}
}

func TestHostOfNameFallsBackToHostID(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ref := hr.hostOf(procBID, g.entities[procBID])
	if ref.name != "b1" || ref.fingerprint != g.entities[hostBID].IdentityHash() {
		t.Fatalf("got %+v", ref)
	}
}

func TestForEventRelationUsesFrom(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ev := model.Event{Relation: &model.RelationEvent{
		Relation:  model.Relation{ID: "r1", Type: "runs_on", From: procID, To: hostAID},
		EventTime: time.Now(),
	}}
	name, fp := hr.forEvent(ev)
	if name != "alpha" || fp != g.entities[hostAID].IdentityHash() {
		t.Fatalf("got %q %q", name, fp)
	}
}

func TestHostAttachmentWhitelist(t *testing.T) {
	ifA := model.EntityID("if-a")
	ifB := model.EntityID("if-b")
	addrID := model.EntityID("addr-1")
	ghostID := model.EntityID("ghost-1")
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			hostAID: hostEntity(hostAID, "a1", "alpha"),
			hostBID: hostEntity(hostBID, "b1", "beta"),
			ifA:     {ID: ifA, Type: "network.interface"},
			ifB:     {ID: ifB, Type: "network.interface"},
			addrID:  {ID: addrID, Type: "network.address"},
			ghostID: {ID: ghostID, Type: "network.address"},
		},
		relations: []model.Relation{
			{ID: "w1", Type: "has_interface", From: hostAID, To: ifA},
			{ID: "w2", Type: "has_interface", From: hostBID, To: ifB},
			{ID: "w3", Type: "connected_to", From: ifA, To: ifB},
			{ID: "w4", Type: "bound_to", From: addrID, To: ifA},
			{ID: "w5", Type: "connected_to", From: ghostID, To: ifB},
			{ID: "w6", Type: "same_as", From: ghostID, To: hostBID},
		},
	}
	hr := newHostResolver(g, nil)
	// bound_to ifA -> has_interface -> host A; connected_to ifB is never
	// followed, so host B is unreachable.
	ref := hr.hostOf(addrID, g.entities[addrID])
	if ref.name != "alpha" || ref.fingerprint != g.entities[hostAID].IdentityHash() {
		t.Fatalf("addr should resolve to host A, got %+v", ref)
	}
	// Only connected_to/same_as links to B: no attachment path at all.
	if ref := hr.hostOf(ghostID, g.entities[ghostID]); ref.name != "" || ref.fingerprint != "" || ref.ambiguous {
		t.Fatalf("ghost must stay unresolved, got %+v", ref)
	}
}

func TestForEventAmbiguousFromYieldsEmpty(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ev := model.Event{Relation: &model.RelationEvent{
		Relation:  model.Relation{ID: "r10", Type: "depends_on", From: dualID, To: procBID},
		EventTime: time.Now(),
	}}
	name, fp := hr.forEvent(ev)
	if name != "" || fp != "" {
		t.Fatalf("ambiguous From must yield empty, got %q %q", name, fp)
	}
}

func TestForEventFallsBackToTo(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	orphanID := model.EntityID("orphan-1")
	g.entities[orphanID] = model.Entity{ID: orphanID, Type: "process"}
	ev := model.Event{Relation: &model.RelationEvent{
		Relation:  model.Relation{ID: "r11", Type: "depends_on", From: orphanID, To: procID},
		EventTime: time.Now(),
	}}
	name, fp := hr.forEvent(ev)
	if name != "alpha" || fp != g.entities[hostAID].IdentityHash() {
		t.Fatalf("got %q %q", name, fp)
	}
}

func TestHostIDShortcutWithSpecialChars(t *testing.T) {
	g := newHostTestGraph()
	oddHost := model.EntityID("host-odd")
	g.entities[oddHost] = model.Entity{ID: oddHost, Type: "host",
		Identity:   []model.KeyValue{{Key: "host.id", Value: model.StringValue("a,1=x")}},
		Attributes: []model.KeyValue{{Key: "host.name", Value: model.StringValue("odd")}}}
	weird := model.EntityID("svc-weird")
	g.entities[weird] = model.Entity{ID: weird, Type: "service", Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("a,1=x")}}}
	hr := newHostResolver(g, nil)
	ref := hr.hostOf(weird, g.entities[weird])
	if ref.name != "odd" || ref.fingerprint != g.entities[oddHost].IdentityHash() {
		t.Fatalf("got %+v", ref)
	}
}

func TestGroupEventsResolvesHostOnlyForKeptExamples(t *testing.T) {
	g := newHostTestGraph()
	now := time.Now()
	events := make([]model.Event, 0, 50)
	for i := 0; i < 50; i++ {
		events = append(events, model.Event{
			Entity: &model.EntityEvent{
				Entity:     g.entities[procID],
				EventTime:  now.Add(time.Duration(i) * time.Second),
				RecordedAt: now,
				ChangeType: model.EntityStateChanged,
			},
		})
	}
	calls := 0
	counting := func(model.Event) (string, string) {
		calls++
		return "alpha", g.entities[hostAID].IdentityHash()
	}
	groups, _, _, _ := groupEvents(events, now.Add(-time.Hour), now.Add(time.Hour), 10, noName, counting)
	if len(groups) != 1 || len(groups[0].Examples) != 5 {
		t.Fatalf("unexpected groups: %d, examples: %d", len(groups), len(groups[0].Examples))
	}
	if calls > 5 {
		t.Fatalf("host resolved %d times, want at most 5", calls)
	}
}

func TestGroupEventsHostFields(t *testing.T) {
	g := newHostTestGraph()
	now := time.Now()
	events := []model.Event{{
		Entity: &model.EntityEvent{
			Entity:     g.entities[procID],
			EventTime:  now,
			RecordedAt: now,
			ChangeType: model.EntityStateChanged,
		},
	}}
	hr := newHostResolver(g, events)
	groups, _, _, _ := groupEvents(events, now.Add(-time.Hour), now.Add(time.Hour), 10, noName, hr.forEvent)
	if len(groups) != 1 || len(groups[0].Examples) != 1 {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	ex := groups[0].Examples[0]
	if ex.Host != "alpha" || ex.HostFingerprint != g.entities[hostAID].IdentityHash() {
		t.Fatalf("example missing host: %+v", ex)
	}
	b, err := json.Marshal(DigestExample{Type: "process"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "host") {
		t.Fatalf("host fields must be omitted when empty: %s", b)
	}
}

func TestDigestGroupJSONHostFields(t *testing.T) {
	g := newHostTestGraph()
	now := time.Now()
	events := []model.Event{{
		Entity: &model.EntityEvent{
			Entity:     g.entities[procID],
			EventTime:  now,
			RecordedAt: now,
			ChangeType: model.EntityStateChanged,
		},
	}}
	groups, _, _, _ := groupEvents(events, now.Add(-time.Hour), now.Add(time.Hour), 10, noName, noHost)
	b, err := json.Marshal(groups[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "host") {
		t.Fatalf("host fields must be omitted when empty: %s", b)
	}
	groups, _, _, _ = groupEvents(events, now.Add(-time.Hour), now.Add(time.Hour), 10, noName, newHostResolver(g, events).forEvent)
	b, err = json.Marshal(groups[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"host":"alpha"`) || !strings.Contains(string(b), "host_fingerprint") {
		t.Fatalf("host fields must be present when set: %s", b)
	}
}

func TestForEventRelationFromHostNamesThatHost(t *testing.T) {
	g := newHostTestGraph()
	hr := newHostResolver(g, nil)
	ev := model.Event{Relation: &model.RelationEvent{
		Relation:  model.Relation{ID: "r20", Type: "has_interface", From: hostAID, To: model.EntityID("iface-elsewhere")},
		EventTime: time.Now(),
	}}
	name, fp := hr.forEvent(ev)
	if name != "alpha" || fp != g.entities[hostAID].IdentityHash() {
		t.Fatalf("a relation leaving a host must name that host, got %q %q", name, fp)
	}
}
