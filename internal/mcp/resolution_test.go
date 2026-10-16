package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// fakeCadence answers a fixed interval for the entities it knows.
type fakeCadence map[model.EntityID]time.Duration

func (f fakeCadence) ObservationInterval(id model.EntityID) (time.Duration, bool) {
	d, ok := f[id]
	return d, ok && d > 0
}

// fakeCadence carries no provenance: the tests that use it are about the
// resolution block, and an empty list is the honest answer for a fake that
// knows nothing about producers.
func (f fakeCadence) AssertingProducers(model.EntityID) []string { return nil }

func (f fakeCadence) AssertingScopes(model.EntityID) []string { return nil }

func (f fakeCadence) RelationAssertingProducers(model.RelationID) []string { return nil }

func (f fakeCadence) RelationAssertingScopes(model.RelationID) []string { return nil }

// An answer states the resolution of its own timestamps, so a consumer knows how
// finely it may read them without having to know the producer's configuration.
func TestGetEntityCarriesResolution(t *testing.T) {
	s := newTestServer().SetCadence(fakeCadence{"01HOST_WEB": 30 * time.Second})

	_, out, err := s.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out.Resolution == nil {
		t.Fatal("no resolution block on the answer")
	}
	if out.Resolution.ObservationInterval != "30s" {
		t.Errorf("observation_interval = %q, want 30s", out.Resolution.ObservationInterval)
	}
	// The number alone invites the misreading; the sentence is the point.
	for _, want := range []string{"OBSERVED", "no guaranteed ordering", "upper bound", "30s"} {
		if !strings.Contains(out.Resolution.Meaning, want) {
			t.Errorf("meaning does not mention %q: %s", want, out.Resolution.Meaning)
		}
	}
}

func TestEntityHistoryCarriesResolution(t *testing.T) {
	s := newTestServer().SetCadence(fakeCadence{"01HOST_WEB": time.Minute})

	_, out, err := s.entityHistory(context.Background(), nil, EntityHistoryInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("entityHistory: %v", err)
	}
	if out.Resolution == nil || out.Resolution.ObservationInterval != "1m0s" {
		t.Fatalf("resolution = %+v, want 1m0s", out.Resolution)
	}
}

// No declared cadence is STATED, not left silent (#363): such an entity never
// expires, and a reader cannot tell it apart from one asserted a minute ago
// unless the answer admits it. No bound is invented — that part is unchanged. A
// server with no cadence source at all still answers nothing, because there the
// question cannot be asked rather than answered in the negative.
func TestResolutionAbsentWhenUnknown(t *testing.T) {
	ctx := context.Background()

	withCadence := newTestServer().SetCadence(fakeCadence{})
	if _, out, err := withCadence.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"}); err != nil {
		t.Fatal(err)
	} else if out.Resolution == nil {
		t.Error("an entity no producer promised to refresh got no resolution block: the absence is the thing a reader needs told")
	} else {
		if out.Resolution.ObservationInterval != "none" {
			t.Errorf("observation_interval = %q, want \"none\"", out.Resolution.ObservationInterval)
		}
		if !strings.Contains(out.Resolution.Meaning, "will NOT expire") {
			t.Errorf("the meaning does not state the consequence: %q", out.Resolution.Meaning)
		}
	}

	none := newTestServer()
	if _, out, err := none.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"}); err != nil {
		t.Fatal(err)
	} else if out.Resolution != nil {
		t.Errorf("resolution present with no cadence source: %+v", out.Resolution)
	}
}

// A timeline that opens on a creation says what it does NOT cover. History is
// keyed by the node-local id, so an entity that flapped shows a short, calm
// timeline — which read as "nothing ever happened" for three days.
func TestEntityHistoryDeclaresItsScope(t *testing.T) {
	s := newTestServer()

	_, out, err := s.entityHistory(context.Background(), nil, EntityHistoryInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("entityHistory: %v", err)
	}
	if len(out.Changes) == 0 {
		t.Skip("the fixture has no history for this entity")
	}
	opensOnCreation := out.Changes[0].ChangeType == model.EntityCreated.String()
	switch {
	case opensOnCreation && out.TimelineScope == "":
		t.Error("timeline opens on a creation but says nothing about earlier incarnations")
	case !opensOnCreation && out.TimelineScope != "":
		t.Errorf("timeline does not open on a creation yet carries a scope note: %s", out.TimelineScope)
	}
	if opensOnCreation {
		for _, want := range []string{"ONE incarnation", "NOT here", "recent_changes"} {
			if !strings.Contains(out.TimelineScope, want) {
				t.Errorf("scope note does not mention %q: %s", want, out.TimelineScope)
			}
		}
	}
}

// badPromiseCadence knows nothing about cadence and everything about one
// refused promise, which is the production shape: the figure was refused, so no
// interval is reportable, and the refusal is what a reader must be told.
type badPromiseCadence struct {
	fakeCadence
	declared time.Duration
	producer string
}

func (b badPromiseCadence) ImplausiblePromise(model.EntityID) (time.Duration, string, bool) {
	return b.declared, b.producer, true
}

// TestResolutionSeparatesRefusedFromAbsentPromise guards the distinction that
// let a production host sit for 56 days reporting a 153-year bound: "no
// cadence" has two causes and they must not read alike. Nobody promised one is
// an ordinary fact about an entity; somebody promised an impossible one is a
// PRODUCER DEFECT, and reporting it as the first hides it behind an answer that
// looks legitimate.
func TestResolutionSeparatesRefusedFromAbsentPromise(t *testing.T) {
	const declared = 1347804 * time.Hour

	refused := newTestServer().SetCadence(badPromiseCadence{
		fakeCadence: fakeCadence{}, declared: declared, producer: "",
	})
	_, out, err := refused.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out.Resolution == nil {
		t.Fatal("no resolution block at all; a reader then assumes nothing is wrong")
	}
	if out.Resolution.ObservationInterval != "none" {
		t.Errorf("interval = %q, want \"none\": the refused figure must never be quoted as a bound", out.Resolution.ObservationInterval)
	}
	if !strings.Contains(out.Resolution.Meaning, declared.String()) {
		t.Errorf("the refused figure is absent from the sentence, so nobody can report the defect: %q", out.Resolution.Meaning)
	}
	if !strings.Contains(out.Resolution.Meaning, "PRODUCER DEFECT") {
		t.Errorf("the sentence does not name this as a producer defect: %q", out.Resolution.Meaning)
	}
	if !strings.Contains(out.Resolution.Meaning, "unidentified producer") {
		t.Errorf("an empty producer must be named as unidentified rather than left blank: %q", out.Resolution.Meaning)
	}

	// The honest absence keeps its own wording, and must NOT read as a defect.
	absent := newTestServer().SetCadence(fakeCadence{})
	_, out2, err := absent.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out2.Resolution == nil || out2.Resolution.ObservationInterval != "none" {
		t.Fatalf("absent-promise resolution = %+v, want interval \"none\"", out2.Resolution)
	}
	if strings.Contains(out2.Resolution.Meaning, "PRODUCER DEFECT") {
		t.Errorf("an entity nobody promised a cadence for is accused of a producer defect: %q", out2.Resolution.Meaning)
	}
}

// anonCadence reports a given number of anonymous references and no named
// producer — the production shape: something asserts the entity and sent no
// identity, so AssertingProducers is rightly empty.
type anonCadence struct {
	fakeCadence
	anon int
}

func (a anonCadence) AnonymousReferences(model.EntityID) int { return a.anon }

// TestProvenanceSeparatesUnclaimedFromAnonymous guards the distinction that let
// a production host sit in a customer graph for eight weeks: an empty
// asserted_by covers two situations and the worse one renders as the better.
//
// Nothing asserts the entity — it survives on a zero deadline (#363), which is
// an ordinary fact about an entity. Or something asserts it and sent no
// identity: a live claim nobody can be asked about, which no named producer can
// ever release, and which is a PRODUCER DEFECT (#507).
//
// The named case must stay silent, because a sentence that appears on healthy
// entities is one nobody reads on the unhealthy one.
func TestProvenanceSeparatesUnclaimedFromAnonymous(t *testing.T) {
	anon := newTestServer().SetCadence(anonCadence{anon: 2})
	_, out, err := anon.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if len(out.Entity.AssertedBy) != 0 {
		t.Fatalf("asserted_by = %v, want empty: the fake names no producer", out.Entity.AssertedBy)
	}
	if out.Entity.Provenance == "" {
		t.Fatal("no provenance sentence: an anonymous claim renders exactly like an unclaimed entity, which is the defect")
	}
	if !strings.Contains(out.Entity.Provenance, "2 references") {
		t.Errorf("the count is absent, so a reader cannot tell one stray claim from many: %q", out.Entity.Provenance)
	}
	if !strings.Contains(out.Entity.Provenance, "PRODUCER DEFECT") {
		t.Errorf("the sentence does not name this as a producer defect: %q", out.Entity.Provenance)
	}
	if !strings.Contains(out.Entity.Provenance, "release") {
		t.Errorf("the sentence omits the consequence that matters — nobody can release it: %q", out.Entity.Provenance)
	}

	// Nothing asserting it at all: honest, ordinary, and NOT a defect.
	unclaimed := newTestServer().SetCadence(anonCadence{anon: 0})
	_, out2, err := unclaimed.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out2.Entity.Provenance != "" {
		t.Errorf("an entity nobody asserts is accused of a producer defect: %q", out2.Entity.Provenance)
	}

	// A named producer: the lists speak for themselves, no sentence.
	named := newTestServer().SetCadence(namedCadence{})
	_, out3, err := named.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out3.Entity.Provenance != "" {
		t.Errorf("a healthy entity carries the sentence, so nobody will read it on an unhealthy one: %q", out3.Entity.Provenance)
	}
}

// namedCadence names a producer, so the provenance lists need no commentary.
type namedCadence struct{ fakeCadence }

func (namedCadence) AssertingProducers(model.EntityID) []string { return []string{"agent-a"} }
func (namedCadence) AnonymousReferences(model.EntityID) int     { return 0 }

// TestAsOfWithholdsLiveProvenance guards the defect that made a past-instant
// read answer from the present: asserted_by, collected_by and the cadence are
// live state, the fold does not rebuild them, and filling them from the live
// table states who asserts the entity TODAY beside its state THEN. That is a
// false attribution, not an approximation — and a false value stops a reader
// where an absent one would have made him look (#528).
//
// The schema promised this withholding in four places from the day the fields
// existed. The guard tested s.cadence == nil, which is never true on an as-of
// read, because the cadence is wired once per server and not per request.
func TestAsOfWithholdsLiveProvenance(t *testing.T) {
	s := newTestServer().SetCadence(namedCadence{})
	ctx := context.Background()
	const past = "2026-05-29T12:45:00Z"

	_, out, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB", AsOf: past})
	if err != nil {
		t.Fatalf("getEntity as_of: %v", err)
	}
	if out.Entity.AssertedBy != nil {
		t.Errorf("asserted_by = %v on an as-of read: that is the LIVE table answering a question about a past instant", out.Entity.AssertedBy)
	}
	if out.Entity.CollectedBy != nil {
		t.Errorf("collected_by = %v on an as-of read, same defect", out.Entity.CollectedBy)
	}
	if out.Entity.Provenance == "" {
		t.Fatal("two null fields and nothing saying why: a reader cannot tell withheld from unanswerable")
	}
	for _, want := range []string{"PAST instant", "WITHHELD", "Omit as_of"} {
		if !strings.Contains(out.Entity.Provenance, want) {
			t.Errorf("the sentence omits %q, so it does not name the consequence or the way out: %q", want, out.Entity.Provenance)
		}
	}
	if out.Resolution == nil || out.Resolution.ObservationInterval != "unknown" {
		t.Errorf("resolution = %+v, want the interval withheld as \"unknown\": a cadence declared today was not the bound that applied then", out.Resolution)
	}

	// find_entities carries the same fields and had the same hole.
	_, found, err := s.findEntities(ctx, nil, FindEntitiesInput{Type: "host", AsOf: past})
	if err != nil {
		t.Fatalf("findEntities as_of: %v", err)
	}
	if len(found.Entities) == 0 {
		t.Fatal("no entity at that instant, so the assertion below would pass vacuously")
	}
	for _, e := range found.Entities {
		if e.AssertedBy != nil || e.Provenance == "" {
			t.Errorf("find_entities as_of leaks live provenance: asserted_by=%v provenance=%q", e.AssertedBy, e.Provenance)
		}
	}

	// A LIVE read must be unchanged: this fix withholds on one path and must
	// not cost the answer that the eight-week production case needed.
	_, live, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity live: %v", err)
	}
	if len(live.Entity.AssertedBy) != 1 || live.Entity.AssertedBy[0] != "agent-a" {
		t.Errorf("a live read lost its provenance: asserted_by = %v, want [agent-a]", live.Entity.AssertedBy)
	}
	if live.Entity.Provenance != "" {
		t.Errorf("a healthy live entity now carries a sentence, so nobody will read it on an unhealthy one: %q", live.Entity.Provenance)
	}
	if live.Resolution == nil || live.Resolution.ObservationInterval == "unknown" {
		t.Errorf("a live read lost its cadence: %+v", live.Resolution)
	}

	// The anonymous case, which #524 exists for, must still speak on a live read.
	anon := newTestServer().SetCadence(anonCadence{anon: 2})
	_, out2, err := anon.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity anonymous live: %v", err)
	}
	if !strings.Contains(out2.Entity.Provenance, "PRODUCER DEFECT") {
		t.Errorf("the as-of guard swallowed the anonymous-producer sentence on a live read: %q", out2.Entity.Provenance)
	}
}

// TestAsOfWithholdsEdgeProvenance covers the edge of a neighbor, which carries
// its own producer lists and had the same hole — and whose guard comment has
// claimed since the fields existed that a fold fills nothing.
//
// The guard is exercised directly rather than through getNeighbors, because the
// fixture's relation endpoints have no creation event and so no past instant
// can traverse the edge: a test built on that would assert nothing while
// looking like it asserted something.
func TestAsOfWithholdsEdgeProvenance(t *testing.T) {
	s := newTestServer().SetCadence(namedRelationCadence{})

	var past Neighbor
	s.attachEdgeProvenance(&past, "rel-runs", "2026-05-29T12:45:00Z")
	if past.ViaAssertedBy != nil || past.ViaCollectedBy != nil {
		t.Errorf("via_asserted_by=%v via_collected_by=%v on an as-of read: the live table answering about a past instant", past.ViaAssertedBy, past.ViaCollectedBy)
	}
	if past.ViaProvenance == "" {
		t.Fatal("the edge's lists are null and nothing says why")
	}
	if !strings.Contains(past.ViaProvenance, "PAST instant") || !strings.Contains(past.ViaProvenance, "WITHHELD") {
		t.Errorf("the sentence does not name what was withheld or why: %q", past.ViaProvenance)
	}

	var live Neighbor
	s.attachEdgeProvenance(&live, "rel-runs", "")
	if len(live.ViaAssertedBy) != 1 || live.ViaAssertedBy[0] != "agent-a" {
		t.Errorf("a live read lost the edge's provenance: via_asserted_by = %v, want [agent-a]", live.ViaAssertedBy)
	}
	if live.ViaProvenance != "" {
		t.Errorf("a live edge carries the withheld sentence, so nobody will read it where it matters: %q", live.ViaProvenance)
	}
}

// namedRelationCadence names a producer on the EDGE, which fakeCadence does not.
type namedRelationCadence struct{ fakeCadence }

func (namedRelationCadence) RelationAssertingProducers(model.RelationID) []string {
	return []string{"agent-a"}
}
func (namedRelationCadence) RelationAssertingScopes(model.RelationID) []string {
	return []string{"senhub-agent/systemd"}
}
