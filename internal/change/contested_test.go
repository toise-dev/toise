package change

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

type contestedClock struct{ t time.Time }

func (c *contestedClock) now() time.Time          { return c.t }
func (c *contestedClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func contestedEngine(t *testing.T) (*Engine, *contestedClock) {
	t.Helper()
	clock := &contestedClock{t: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	return New(projection.New(), &fakeAppender{}, WithClock(clock.now)), clock
}

func contestedObserve(t *testing.T, e *Engine, id, name string, interval time.Duration) {
	t.Helper()
	_, err := e.ObserveEntity(EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}},
		Attributes: []model.KeyValue{
			{Key: "host.name", Value: model.StringValue(name)},
		},
		Interval: interval,
	})
	if err != nil {
		t.Fatalf("ObserveEntity: %v", err)
	}
}

func TestContestedStableEntity(t *testing.T) {
	e, clock := contestedEngine(t)
	for i := 0; i < 5; i++ {
		contestedObserve(t, e, "h1", "host-a", time.Hour)
		clock.advance(10 * time.Minute)
	}
	if got := e.ContestedDisplayNames(); got != 0 {
		t.Fatalf("stable entity: got %d, want 0", got)
	}
}

func TestContestedSingleRename(t *testing.T) {
	e, clock := contestedEngine(t)
	contestedObserve(t, e, "h1", "host-a", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "host-b", time.Hour)
	if got := e.ContestedDisplayNames(); got != 0 {
		t.Fatalf("single rename: got %d, want 0", got)
	}
}

func TestContestedAlternation(t *testing.T) {
	e, clock := contestedEngine(t)
	contestedObserve(t, e, "h1", "model-a", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-b", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-a", time.Hour)
	if got := e.ContestedDisplayNames(); got != 1 {
		t.Fatalf("alternation: got %d, want 1", got)
	}
}

func TestContestedAlternationExpires(t *testing.T) {
	e, clock := contestedEngine(t)
	contestedObserve(t, e, "h1", "model-a", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-b", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-a", time.Hour)
	if got := e.ContestedDisplayNames(); got != 1 {
		t.Fatalf("before sweep: got %d, want 1", got)
	}
	clock.advance(2 * time.Hour)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := e.ContestedDisplayNames(); got != 0 {
		t.Fatalf("after sweep: got %d, want 0", got)
	}
}

func TestContestedTwoEntities(t *testing.T) {
	e, clock := contestedEngine(t)
	for _, id := range []string{"h1", "h2"} {
		contestedObserve(t, e, id, "model-a", time.Hour)
		clock.advance(10 * time.Minute)
		contestedObserve(t, e, id, "model-b", time.Hour)
		clock.advance(10 * time.Minute)
		contestedObserve(t, e, id, "model-a", time.Hour)
		clock.advance(10 * time.Minute)
	}
	if got := e.ContestedDisplayNames(); got != 2 {
		t.Fatalf("two entities: got %d, want 2", got)
	}
}

func TestContestedNoDeclaredInterval(t *testing.T) {
	e, clock := contestedEngine(t)
	contestedObserve(t, e, "h1", "model-a", 0)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-b", 0)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-a", 0)
	if got := e.ContestedDisplayNames(); got != 1 {
		t.Fatalf("no interval: got %d, want 1", got)
	}
	clock.advance(48 * time.Hour)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := e.ContestedDisplayNames(); got != 1 {
		t.Fatalf("no interval after sweep: got %d, want 1 (nothing expires without a declared window)", got)
	}
}

// The condition is a property of a live entity. An entity that is gone must
// stop being counted, or the number drifts upward for ever and an operator who
// fixed the producer never sees it fall.
func TestContestedEntityGoneStopsCounting(t *testing.T) {
	e, clock := contestedEngine(t)
	contestedObserve(t, e, "h1", "model-a", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-b", time.Hour)
	clock.advance(10 * time.Minute)
	contestedObserve(t, e, "h1", "model-a", time.Hour)
	if got := e.ContestedDisplayNames(); got != 1 {
		t.Fatalf("alternation not detected: got %d, want 1", got)
	}
	if _, _, err := e.DeleteEntity(EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}},
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if got := e.ContestedDisplayNames(); got != 0 {
		t.Fatalf("a deleted entity is still counted: got %d, want 0", got)
	}
}
