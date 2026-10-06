package change

import (
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// TestAssertingProducersIsReachable is the cheap half of #394: per-producer
// reference counting (ADR 0019) has always held which scopes assert an entity,
// and no read surface returned it. A consumer looking at a suspect value could
// not tell whether one producer said it or three agreed, nor which one to go
// and ask.
func TestAssertingProducersIsReachable(t *testing.T) {
	e, _, _ := newEngine(t)
	ident := []model.KeyValue{kv("host.id", "h-shared")}

	if _, err := e.ObserveEntity(EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: t0, Producer: "agent-b",
	}); err != nil {
		t.Fatalf("observe b: %v", err)
	}
	ev, err := e.ObserveEntity(EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: t0, Producer: "agent-a",
	})
	if err != nil {
		t.Fatalf("observe a: %v", err)
	}
	id := ev.Entity.Entity.ID

	got := e.AssertingProducers(id)
	if len(got) != 2 || got[0] != "agent-a" || got[1] != "agent-b" {
		t.Fatalf("AssertingProducers = %v, want [agent-a agent-b] sorted", got)
	}

	// A producer that releases its reference is gone from the list: the question
	// is who asserts the entity NOW, not who ever did.
	if _, _, err := e.DeleteEntity(EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: t0, Producer: "agent-b",
	}); err != nil {
		t.Fatalf("release b: %v", err)
	}
	if got := e.AssertingProducers(id); len(got) != 1 || got[0] != "agent-a" {
		t.Errorf("after one producer released: %v, want [agent-a]", got)
	}

	// An unknown entity has no producers rather than a nil-vs-empty surprise.
	if got := e.AssertingProducers("no-such-id"); len(got) != 0 {
		t.Errorf("unknown entity: %v, want empty", got)
	}
}
