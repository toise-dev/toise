package change

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// The cadence is the resolution of every timestamp Toise reports about an
// entity. It exists in the engine already; these pin what it answers.
func TestObservationInterval(t *testing.T) {
	e, _, _ := newEngine(t)
	ident := []model.KeyValue{kv("host.id", "h1")}

	ev, err := e.ObserveEntity(EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: t0,
		Producer: "agent-a", Interval: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	id := ev.Entity.Entity.ID

	got, ok := e.ObservationInterval(id)
	if !ok || got != 30*time.Second {
		t.Fatalf("one producer: got %v, %v; want 30s, true", got, ok)
	}

	// A SECOND producer reporting faster must not make the slower one's
	// observations finer: the bound a consumer may rely on is the coarsest.
	if _, err := e.ObserveEntity(EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: t0,
		Producer: "agent-b", Interval: 5 * time.Second,
	}); err != nil {
		t.Fatalf("second producer: %v", err)
	}
	if got, ok := e.ObservationInterval(id); !ok || got != 30*time.Second {
		t.Errorf("two producers: got %v, %v; want the coarsest 30s, true", got, ok)
	}

	// A third, coarser one raises the bound.
	if _, err := e.ObserveEntity(EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: t0,
		Producer: "agent-c", Interval: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("third producer: %v", err)
	}
	if got, ok := e.ObservationInterval(id); !ok || got != 2*time.Minute {
		t.Errorf("three producers: got %v, %v; want 2m0s, true", got, ok)
	}
}

// No declared interval means no known resolution. Saying nothing is the honest
// answer; inventing a bound would be worse than the silence.
func TestObservationIntervalUnknown(t *testing.T) {
	e, _, _ := newEngine(t)

	ev, err := e.ObserveEntity(EntityObservation{
		Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")},
		EventTime: t0, Producer: "agent-a", // no Interval
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got, ok := e.ObservationInterval(ev.Entity.Entity.ID); ok {
		t.Errorf("no interval declared, got %v, true; want false", got)
	}
	if _, ok := e.ObservationInterval(model.EntityID("does-not-exist")); ok {
		t.Error("an unknown entity reported a cadence")
	}
}
