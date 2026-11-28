package change

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

const (
	prodP = "11111111-2222-4333-8444-555555555555"
	prodQ = "99999999-2222-4333-8444-555555555555"
	prodZ = "77777777-2222-4333-8444-555555555555"
)

func seenMap(t *testing.T, e *Engine) map[string]time.Time {
	t.Helper()
	m := make(map[string]time.Time)
	for _, s := range e.ProducerLastSeen() {
		m[s.Producer+"|"+s.Scope] = s.LastSeen
	}
	return m
}

func TestProducerLastSeenUpdatedByObservation(t *testing.T) {
	clock := &contestedClock{t: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	e := New(projection.New(), &fakeAppender{}, WithClock(clock.now))

	obs := EntityObservation{
		Type:      model.TypeHost,
		Identity:  []model.KeyValue{kv("host.id", "host-1")},
		Producer:  prodP,
		Interval:  time.Minute,
		EventTime: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	err := e.Batch(func(b *Batch) {
		b.SetScope("test/method-a")
		if _, oerr := b.ObserveEntity(obs); oerr != nil {
			t.Fatalf("observe: %v", oerr)
		}
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	m := seenMap(t, e)
	if len(m) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m))
	}
	if got, ok := m[prodP+"|test/method-a"]; !ok {
		t.Fatalf("missing entry for producer P in scope test/method-a: %v", m)
	} else if !got.Equal(clock.t) {
		t.Fatalf("LastSeen = %v, want %v (engine clock, not EventTime)", got, clock.t)
	}

	clock.advance(10 * time.Second)
	err = e.Batch(func(b *Batch) {
		b.SetScope("test/method-a")
		if _, oerr := b.ObserveEntity(obs); oerr != nil {
			t.Fatalf("observe: %v", oerr)
		}
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	m = seenMap(t, e)
	if len(m) != 1 {
		t.Fatalf("expected 1 entry after re-observe, got %d", len(m))
	}
	if got := m[prodP+"|test/method-a"]; !got.Equal(clock.t) {
		t.Fatalf("LastSeen = %v, want %v", got, clock.t)
	}
}

func TestProducerLastSeenOneSeriesPerMethod(t *testing.T) {
	clock := &contestedClock{t: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	e := New(projection.New(), &fakeAppender{}, WithClock(clock.now))

	obsA := EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{kv("host.id", "host-1")},
		Producer: prodP,
		Interval: time.Minute,
	}
	obsB := EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{kv("host.id", "host-2")},
		Producer: prodP,
		Interval: time.Minute,
	}
	err := e.Batch(func(b *Batch) {
		b.SetScope("test/method-a")
		if _, err := b.ObserveEntity(obsA); err != nil {
			t.Fatalf("observe A: %v", err)
		}
		b.SetScope("test/method-b")
		if _, err := b.ObserveEntity(obsB); err != nil {
			t.Fatalf("observe B: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	m := seenMap(t, e)
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(m), m)
	}
	for _, key := range []string{prodP + "|test/method-a", prodP + "|test/method-b"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("missing entry %q: %v", key, m)
		}
	}
}

func TestProducerLastSeenLeavesWithItsLastReference(t *testing.T) {
	clock := &contestedClock{t: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	e := New(projection.New(), &fakeAppender{}, WithClock(clock.now))

	err := e.Batch(func(b *Batch) {
		b.SetScope("test/method-a")
		if _, err := b.ObserveEntity(EntityObservation{
			Type:     model.TypeHost,
			Identity: []model.KeyValue{kv("host.id", "host-1")},
			Producer: prodP,
			Interval: time.Minute,
		}); err != nil {
			t.Fatalf("observe P: %v", err)
		}
		if _, err := b.ObserveEntity(EntityObservation{
			Type:     model.TypeHost,
			Identity: []model.KeyValue{kv("host.id", "host-2")},
			Producer: prodQ,
			Interval: time.Hour,
		}); err != nil {
			t.Fatalf("observe Q: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	m := seenMap(t, e)
	if len(m) != 2 {
		t.Fatalf("expected 2 entries before sweep, got %d: %v", len(m), m)
	}

	clock.advance(5 * time.Minute)
	if _, err := e.Sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	m = seenMap(t, e)
	if _, ok := m[prodP+"|test/method-a"]; ok {
		t.Fatalf("entry for producer P should be gone after sweep: %v", m)
	}
	if _, ok := m[prodQ+"|test/method-a"]; !ok {
		t.Fatalf("entry for producer Q should remain after sweep: %v", m)
	}
}

func TestProducerLastSeenReseededByRestoreLiveness(t *testing.T) {
	clock := &contestedClock{t: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	e := New(projection.New(), &fakeAppender{}, WithClock(clock.now))

	observedAt := clock.t
	err := e.Batch(func(b *Batch) {
		b.SetScope("test/method-a")
		if _, err := b.ObserveEntity(EntityObservation{
			Type:     model.TypeHost,
			Identity: []model.KeyValue{kv("host.id", "host-1")},
			Producer: prodP,
			Interval: time.Minute,
		}); err != nil {
			t.Fatalf("observe P: %v", err)
		}
		if _, err := b.ObserveEntity(EntityObservation{
			Type:     model.TypeHost,
			Identity: []model.KeyValue{kv("host.id", "host-2")},
			Producer: prodZ,
			Interval: 0,
		}); err != nil {
			t.Fatalf("observe Z: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}

	blob, err := e.LivenessBlob()
	if err != nil {
		t.Fatalf("LivenessBlob: %v", err)
	}

	clock2 := &contestedClock{t: observedAt}
	e2 := New(projection.New(), &fakeAppender{}, WithClock(clock2.now))
	if err := e2.RestoreLiveness(blob); err != nil {
		t.Fatalf("RestoreLiveness: %v", err)
	}

	m := seenMap(t, e2)
	got, ok := m[prodP+"|test/method-a"]
	if !ok {
		t.Fatalf("restored engine missing entry for producer P: %v", m)
	}
	if !got.Equal(observedAt) {
		t.Fatalf("LastSeen = %v, want %v", got, observedAt)
	}
	if _, ok := m[prodZ+"|test/method-a"]; ok {
		t.Fatalf("zero-interval producer Z should not seed an entry: %v", m)
	}
}
