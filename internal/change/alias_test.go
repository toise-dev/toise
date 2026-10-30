package change

import (
	"sync"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

type aliasClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *aliasClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *aliasClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func aliasTestEngine(t *testing.T, opts ...Option) (*Engine, *projection.Graph, *[]record) {
	t.Helper()
	g := projection.New()
	base := []Option{WithClock(fixedNow), WithRelationBuffer(time.Minute)}
	e := New(g, &fakeAppender{}, append(base, opts...)...)
	var recs []record
	e.Subscribe(func(ev model.Event, hp bool) { recs = append(recs, record{ev, hp}) })
	return e, g, &recs
}

func aliasDeviceIdentity(id string) []model.KeyValue {
	return []model.KeyValue{kv("network.device.id", id)}
}

func aliasInterfaceIdentity(deviceID, name string) []model.KeyValue {
	return []model.KeyValue{kv("network.device.id", deviceID), kv("network.interface.name", name)}
}

func aliasSameAs(fromID, toID string, attrs ...model.KeyValue) RelationObservation {
	return RelationObservation{
		Type:       model.RelSameAs,
		From:       EndpointRef{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity(fromID)},
		To:         EndpointRef{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity(toID)},
		Attributes: attrs,
	}
}

func aliasConfidence() model.KeyValue {
	return model.KeyValue{Key: "confidence", Value: model.DoubleValue(1.0)}
}

func aliasConnectedTo(fromDev, fromPort, toDev, toPort string) RelationObservation {
	return RelationObservation{
		Type: model.RelConnectedTo,
		From: EndpointRef{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity(fromDev, fromPort)},
		To:   EndpointRef{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity(toDev, toPort)},
	}
}

// aliasSpeaker publishes the port the asserting agent posts adjacency FROM. It
// must belong to another device: an edge from and to the same interface is
// reflexive whatever the alias does, and would pass for a resolution failure.
func aliasObserveSpeaker(t *testing.T, e *Engine) {
	t.Helper()
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:S")}); err != nil {
		t.Fatalf("speaker device: %v", err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:S", "eth9")}); err != nil {
		t.Fatalf("speaker interface: %v", err)
	}
}

func aliasRelations(g *projection.Graph, relType string) []model.Relation {
	return g.ListRelations(relType, "", "")
}

func TestAliasResolvesAdjacencyEndpoint(t *testing.T) {
	e, g, _ := aliasTestEngine(t)
	aliasObserveSpeaker(t, e)

	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	ifaceEv, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")})
	if err != nil {
		t.Fatalf("interface: %v", err)
	}
	if _, _, aliasErr := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence(), model.KeyValue{Key: "basis", Value: model.StringValue("lldp_chassis")})); aliasErr != nil {
		t.Fatalf("same_as: %v", aliasErr)
	}
	_, emitted, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0"))
	if err != nil {
		t.Fatalf("connected_to: %v", err)
	}
	if !emitted {
		t.Fatal("connected_to was not committed")
	}
	rels := aliasRelations(g, model.RelConnectedTo)
	if len(rels) != 1 {
		t.Fatalf("connected_to count = %d, want 1", len(rels))
	}
	if rels[0].To != ifaceEv.Entity.Entity.ID {
		t.Fatalf("connected_to to = %s, want interface of serial:A (%s)", rels[0].To, ifaceEv.Entity.Entity.ID)
	}
}

func TestAliasResolvesParkedEdgeAfterAliasAndTargetArrive(t *testing.T) {
	e, g, _ := aliasTestEngine(t)
	aliasObserveSpeaker(t, e)

	if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil {
		t.Fatalf("connected_to: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
		t.Fatalf("connected_to committed too early: %d", n)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
		t.Fatalf("connected_to committed before its interface arrived: %d", n)
	}
	ifaceEv, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")})
	if err != nil {
		t.Fatalf("interface: %v", err)
	}
	rels := aliasRelations(g, model.RelConnectedTo)
	if len(rels) != 1 {
		t.Fatalf("connected_to count = %d, want 1", len(rels))
	}
	if rels[0].To != ifaceEv.Entity.Entity.ID {
		t.Fatalf("connected_to to = %s, want %s", rels[0].To, ifaceEv.Entity.Entity.ID)
	}
}

func TestAliasConflictIsAmbiguousAndReportedOnce(t *testing.T) {
	var calls [][2]string
	e, g, _ := aliasTestEngine(t, WithIdentityAliasConflictReporter(func(attributeKey, aliasValue string) {
		calls = append(calls, [2]string{attributeKey, aliasValue})
	}))
	aliasObserveSpeaker(t, e)

	for _, id := range []string{"serial:A", "serial:B"} {
		if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity(id)}); err != nil {
			t.Fatalf("device %s: %v", id, err)
		}
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
		t.Fatalf("interface: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as A: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:B", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as B: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as A again: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("conflict reporter calls = %d, want 1", len(calls))
	}
	if calls[0] != [2]string{"network.device.id", "mac:Y"} {
		t.Fatalf("conflict reporter args = %v", calls[0])
	}
	if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil {
		t.Fatalf("connected_to: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
		t.Fatalf("ambiguous alias resolved an edge: %d connected_to", n)
	}
}

func TestAliasRequiresReadableConfidenceAtThreshold(t *testing.T) {
	cases := map[string][]model.KeyValue{
		"absent":    nil,
		"string":    {model.KeyValue{Key: "confidence", Value: model.StringValue("1.0")}},
		"above one": {model.KeyValue{Key: "confidence", Value: model.DoubleValue(1.5)}},
		"negative":  {model.KeyValue{Key: "confidence", Value: model.DoubleValue(-0.1)}},
		"below":     {model.KeyValue{Key: "confidence", Value: model.DoubleValue(0.5)}},
		"bool":      {model.KeyValue{Key: "confidence", Value: model.BoolValue(true)}},
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			e, g, _ := aliasTestEngine(t)
			aliasObserveSpeaker(t, e)
			if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
				t.Fatalf("device: %v", err)
			}
			if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
				t.Fatalf("interface: %v", err)
			}
			if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", attrs...)); err != nil {
				t.Fatalf("same_as: %v", err)
			}
			if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil {
				t.Fatalf("connected_to: %v", err)
			}
			if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
				t.Fatalf("unbelievable same_as still resolved an edge: %d", n)
			}
		})
	}
}

func TestAliasSubstitutionIsExactValueOnly(t *testing.T) {
	lookalikes := []string{"MAC:Y", "mac-Y", " mac:Y", "mac", "mac:Y "}
	e, g, _ := aliasTestEngine(t)
	aliasObserveSpeaker(t, e)
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
		t.Fatalf("interface: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	for _, lookalike := range lookalikes {
		if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", lookalike, "eth0")); err != nil {
			t.Fatalf("connected_to %q: %v", lookalike, err)
		}
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
		t.Fatalf("a lookalike value resolved an edge: %d", n)
	}
}

func TestAliasSameAsDoesNotBecomeReflexive(t *testing.T) {
	e, g, _ := aliasTestEngine(t)
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	if n := len(aliasRelations(g, model.RelSameAs)); n != 0 {
		t.Fatalf("same_as was committed (reflexively): %d", n)
	}
	for _, r := range aliasRelations(g, "") {
		if r.From == r.To {
			t.Fatalf("reflexive relation committed: %s %s", r.Type, r.ID)
		}
	}
}

func TestAliasSameAsBetweenExistingEntitiesStaysAnEdge(t *testing.T) {
	e, g, _ := aliasTestEngine(t)
	aEv, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")})
	if err != nil {
		t.Fatalf("device A: %v", err)
	}
	yEv, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("mac:Y")})
	if err != nil {
		t.Fatalf("device Y: %v", err)
	}
	_, emitted, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence()))
	if err != nil {
		t.Fatalf("same_as: %v", err)
	}
	if !emitted {
		t.Fatal("same_as between two existing entities was not committed")
	}
	rels := aliasRelations(g, model.RelSameAs)
	if len(rels) != 1 {
		t.Fatalf("same_as count = %d, want 1", len(rels))
	}
	if rels[0].From != aEv.Entity.Entity.ID || rels[0].To != yEv.Entity.Entity.ID {
		t.Fatalf("same_as endpoints = %s -> %s, want %s -> %s", rels[0].From, rels[0].To, aEv.Entity.Entity.ID, yEv.Entity.Entity.ID)
	}
}

func TestAliasRetractionOnSweep(t *testing.T) {
	clock := &aliasClock{t: fixedNow()}
	e, g, _ := aliasTestEngine(t, WithClock(clock.now), WithRelationBuffer(time.Hour))
	aliasObserveSpeaker(t, e)

	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
		t.Fatalf("interface: %v", err)
	}
	sameAs := aliasSameAs("serial:A", "mac:Y", aliasConfidence())
	sameAs.Producer = "agent-a"
	sameAs.Interval = time.Hour
	if _, _, err := e.ObserveRelation(sameAs); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	if _, emitted, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil || !emitted {
		t.Fatalf("connected_to before expiry: emitted=%v err=%v", emitted, err)
	}

	clock.advance(2 * time.Hour)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth1")); err != nil {
		t.Fatalf("connected_to after expiry: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 1 {
		t.Fatalf("connected_to count after retraction = %d, want 1 (the pre-expiry one)", n)
	}

	clock2 := &aliasClock{t: fixedNow()}
	e2, g2, _ := aliasTestEngine(t, WithClock(clock2.now), WithRelationBuffer(time.Hour))
	aliasObserveSpeaker(t, e2)
	if _, err := e2.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, err := e2.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
		t.Fatalf("interface: %v", err)
	}
	a := aliasSameAs("serial:A", "mac:Y", aliasConfidence())
	a.Producer = "agent-a"
	a.Interval = time.Hour
	b := aliasSameAs("serial:A", "mac:Y", aliasConfidence())
	b.Producer = "agent-b"
	b.Interval = 2 * time.Hour
	if _, _, err := e2.ObserveRelation(a); err != nil {
		t.Fatalf("same_as a: %v", err)
	}
	if _, _, err := e2.ObserveRelation(b); err != nil {
		t.Fatalf("same_as b: %v", err)
	}
	clock2.advance(90 * time.Minute)
	if _, err := e2.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, emitted, err := e2.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil || !emitted {
		t.Fatalf("connected_to after one speaker lapsed: emitted=%v err=%v", emitted, err)
	}
	if n := len(aliasRelations(g2, model.RelConnectedTo)); n != 1 {
		t.Fatalf("connected_to count = %d, want 1", n)
	}
}

func TestAliasDeclarationDropIsNotCountedAsLostEdge(t *testing.T) {
	clock := &aliasClock{t: fixedNow()}
	var discards []string
	e, _, _ := aliasTestEngine(t,
		WithClock(clock.now),
		WithRelationBuffer(30*time.Second),
		WithDiscardReporter(func(relationType, fromType, toType string) {
			discards = append(discards, relationType)
		}),
	)
	aliasObserveSpeaker(t, e)

	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	sameAs := aliasSameAs("serial:A", "mac:Y", aliasConfidence())
	sameAs.Interval = time.Hour
	if _, _, err := e.ObserveRelation(sameAs); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil {
		t.Fatalf("connected_to: %v", err)
	}

	clock.advance(time.Minute)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(discards) != 1 || discards[0] != model.RelConnectedTo {
		t.Fatalf("discards = %v, want [connected_to]", discards)
	}
}

// An entity published under an alias identity stays its own entity. The
// observed identity is written verbatim into the event, so substituting here
// would make the stored identity alternate between the two speakers' values,
// and would have Toise decide an entity merge nobody asked for.
func TestAliasIsNotAppliedToEntityObservations(t *testing.T) {
	e, g, _ := aliasTestEngine(t)
	aEv, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")})
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, _, aliasErr := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); aliasErr != nil {
		t.Fatalf("same_as: %v", aliasErr)
	}
	yEv, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("mac:Y")})
	if err != nil {
		t.Fatalf("aliased device: %v", err)
	}
	if yEv.Entity.ChangeType != model.EntityCreated {
		t.Fatalf("change type = %s, want %s: the aliased identity was merged instead of created", yEv.Entity.ChangeType, model.EntityCreated)
	}
	if yEv.Entity.Entity.ID == aEv.Entity.Entity.ID {
		t.Fatal("the aliased identity resolved to the canonical entity's id")
	}
	if n := len(g.EntitiesOfType(model.TypeNetworkDevice)); n != 2 {
		t.Fatalf("network.device count = %d, want 2", n)
	}
}

// A same_as whose alias target is its own source can never resolve, so it must
// not be parked. Parking it fills the reconciliation buffer with edges no
// arrival can complete, and the full-buffer eviction then drops legitimate
// edges and counts them lost.
func TestAliasCollapsedEdgeIsNotParked(t *testing.T) {
	var discards []string
	e, _, _ := aliasTestEngine(t, WithDiscardReporter(func(relationType, fromType, toType string) {
		discards = append(discards, relationType)
	}))
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
			t.Fatalf("same_as %d: %v", i, err)
		}
	}
	if len(e.pending) != 0 {
		t.Fatalf("parked edges = %d, want 0: a collapsed edge was parked", len(e.pending))
	}
	if len(discards) != 0 {
		t.Fatalf("discards = %v, want none: the alias belief was retained", discards)
	}
}

// The entity that completes a parked edge may arrive BEFORE the equivalence
// that makes it reachable. Its observation has then been and gone, and no
// later arrival would trigger the replay, so recording an equivalence must
// retry the buffer itself.
func TestAliasParkedEdgeResolvesWhenTargetArrivedBeforeTheAlias(t *testing.T) {
	e, g, _ := aliasTestEngine(t)
	aliasObserveSpeaker(t, e)

	if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil {
		t.Fatalf("connected_to: %v", err)
	}
	if len(e.pending) != 1 {
		t.Fatalf("parked = %d, want 1", len(e.pending))
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
		t.Fatalf("interface: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
		t.Fatalf("committed before any equivalence existed: %d", n)
	}
	if _, _, err := e.ObserveRelation(aliasSameAs("serial:A", "mac:Y", aliasConfidence())); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 1 {
		t.Fatalf("connected_to = %d, want 1: the alias arrived last and nothing retried the buffer", n)
	}
}

// Replaying a parked edge must not re-assert its speaker's equivalence. The
// deadline belongs to the speaker's own re-emission; refreshing it on a replay
// lets an equivalence outlive the silence that was meant to retract it.
func TestAliasReplayDoesNotRefreshTheSpeakersDeadline(t *testing.T) {
	clock := &aliasClock{t: fixedNow()}
	e, g, _ := aliasTestEngine(t, WithClock(clock.now), WithRelationBuffer(time.Hour))
	aliasObserveSpeaker(t, e)

	sameAs := aliasSameAs("serial:A", "mac:Y", aliasConfidence())
	sameAs.Producer = "agent-a"
	sameAs.Interval = 10 * time.Minute
	if _, _, err := e.ObserveRelation(sameAs); err != nil {
		t.Fatalf("same_as: %v", err)
	}
	// An edge parks on the still-absent target, and the speaker's own port
	// heartbeats repeatedly — each heartbeat replays the buffer.
	if _, _, err := e.ObserveRelation(aliasConnectedTo("serial:S", "eth9", "mac:Y", "eth0")); err != nil {
		t.Fatalf("connected_to: %v", err)
	}
	for i := 0; i < 4; i++ {
		clock.advance(5 * time.Minute)
		aliasObserveSpeaker(t, e)
	}
	// Twenty minutes of silence from agent-a, against a ten-minute promise.
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkDevice, Identity: aliasDeviceIdentity("serial:A")}); err != nil {
		t.Fatalf("device: %v", err)
	}
	if _, err := e.ObserveEntity(EntityObservation{Type: model.TypeNetworkInterface, Identity: aliasInterfaceIdentity("serial:A", "eth0")}); err != nil {
		t.Fatalf("interface: %v", err)
	}
	if n := len(aliasRelations(g, model.RelConnectedTo)); n != 0 {
		t.Fatalf("connected_to = %d, want 0: an equivalence its speaker stopped asserting still resolved an edge", n)
	}
}
