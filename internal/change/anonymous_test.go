package change

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

func anonEngine(t *testing.T) *Engine {
	t.Helper()
	return New(projection.New(), &fakeAppender{}, WithClock(fixedNow))
}

func anonObserve(t *testing.T, e *Engine, id, producer string) model.EntityID {
	t.Helper()
	ev, err := e.ObserveEntity(EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}},
		Producer: producer,
	})
	if err != nil {
		t.Fatalf("ObserveEntity(%s): %v", id, err)
	}
	return ev.Entity.Entity.ID
}

// An entity asserted by something that sent no producer identity is a live
// claim nobody can be asked about, and no named producer can ever release it.
// It reads exactly like an entity nobody asserts, so the count is the only way
// to see it from inside the graph.
func TestAnonymousReferenceEntities(t *testing.T) {
	t.Run("no entity", func(t *testing.T) {
		if got := anonEngine(t).AnonymousReferenceEntities(); got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})

	t.Run("a named producer", func(t *testing.T) {
		e := anonEngine(t)
		anonObserve(t, e, "h1", "agent-a")
		if got := e.AnonymousReferenceEntities(); got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})

	t.Run("no producer identity", func(t *testing.T) {
		e := anonEngine(t)
		anonObserve(t, e, "h1", "")
		if got := e.AnonymousReferenceEntities(); got != 1 {
			t.Fatalf("got %d, want 1", got)
		}
	})

	t.Run("one named and one anonymous count the entity once", func(t *testing.T) {
		e := anonEngine(t)
		anonObserve(t, e, "h1", "agent-a")
		anonObserve(t, e, "h1", "")
		if got := e.AnonymousReferenceEntities(); got != 1 {
			t.Fatalf("got %d, want 1", got)
		}
	})

	t.Run("two anonymous entities", func(t *testing.T) {
		e := anonEngine(t)
		anonObserve(t, e, "h1", "")
		anonObserve(t, e, "h2", "")
		if got := e.AnonymousReferenceEntities(); got != 2 {
			t.Fatalf("got %d, want 2", got)
		}
	})

	// Liveness, not only an explicit delete: an anonymous producer that declared
	// an interval and then went silent must stop being counted, because its
	// reference lapses like any other. The anonymous case is the one where this
	// is easy to get wrong, since no named speaker can release it.
	t.Run("the count falls when an anonymous reference lapses", func(t *testing.T) {
		clock := &contestedClock{t: fixedNow()}
		e := New(projection.New(), &fakeAppender{}, WithClock(clock.now))
		if _, err := e.ObserveEntity(EntityObservation{
			Type:     model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}},
			Interval: time.Minute,
		}); err != nil {
			t.Fatalf("ObserveEntity: %v", err)
		}
		if got := e.AnonymousReferenceEntities(); got != 1 {
			t.Fatalf("before the sweep: got %d, want 1", got)
		}
		clock.advance(5 * time.Minute)
		if _, err := e.Sweep(); err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if got := e.AnonymousReferenceEntities(); got != 0 {
			t.Fatalf("after the sweep: got %d, want 0 — a lapsed anonymous reference is still counted", got)
		}
	})

	// The number has to fall again, or an operator who fixed the producer never
	// sees the result of having fixed it.
	t.Run("the count falls when the entity goes", func(t *testing.T) {
		e := anonEngine(t)
		anonObserve(t, e, "h1", "")
		if _, _, err := e.DeleteEntity(EntityObservation{
			Type:     model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}},
		}); err != nil {
			t.Fatalf("DeleteEntity: %v", err)
		}
		if got := e.AnonymousReferenceEntities(); got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})
}
