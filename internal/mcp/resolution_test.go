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
