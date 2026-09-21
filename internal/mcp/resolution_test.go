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
	for _, want := range []string{"OBSERVED", "cannot be ordered", "30s"} {
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

// Unknown cadence says nothing rather than inventing a bound, and a server with
// no cadence source at all behaves exactly as before.
func TestResolutionAbsentWhenUnknown(t *testing.T) {
	ctx := context.Background()

	withCadence := newTestServer().SetCadence(fakeCadence{})
	if _, out, err := withCadence.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"}); err != nil {
		t.Fatal(err)
	} else if out.Resolution != nil {
		t.Errorf("resolution invented for an entity with no declared cadence: %+v", out.Resolution)
	}

	none := newTestServer()
	if _, out, err := none.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"}); err != nil {
		t.Fatal(err)
	} else if out.Resolution != nil {
		t.Errorf("resolution present with no cadence source: %+v", out.Resolution)
	}
}
