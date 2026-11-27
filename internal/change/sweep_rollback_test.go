package change

import (
	"log/slog"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

func TestSweepFlushFailureRetriesEntityExpiry(t *testing.T) {
	now := t0
	ap := &failingAppender{}
	g := projection.New()
	e := New(g, ap,
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	host := []model.KeyValue{kv("host.name", "h-1")}
	if _, err := e.ObserveEntity(EntityObservation{
		Type:      model.TypeHost,
		Identity:  host,
		Interval:  time.Minute,
		EventTime: now,
	}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)

	ap.fail = true
	res, err := e.Sweep()
	if err == nil {
		t.Fatal("expected sweep to fail when the flush fails")
	}
	if res.Total() != 0 {
		t.Fatalf("failed sweep must report zero, got %d", res.Total())
	}
	if _, found := g.MatchIdentity(model.TypeHost, host); !found {
		t.Fatal("host must remain live after a failed flush")
	}

	ap.fail = false
	res, err = e.Sweep()
	if err != nil {
		t.Fatalf("retry sweep: %v", err)
	}
	if res.Entities != 1 || res.Total() != 1 {
		t.Fatalf("retry sweep must expire the host, got entities=%d relations=%d", res.Entities, res.Relations)
	}
	if _, found := g.MatchIdentity(model.TypeHost, host); found {
		t.Fatal("host must not match after a successful expiry sweep")
	}
}

func TestSweepFlushFailureRetriesRelationExpiry(t *testing.T) {
	now := t0
	ap := &failingAppender{}
	g := projection.New()
	e := New(g, ap,
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	host := []model.KeyValue{kv("host.name", "h-1")}
	proc := []model.KeyValue{kv("process.name", "p-1")}
	if _, err := e.ObserveEntity(EntityObservation{
		Type:      model.TypeHost,
		Identity:  host,
		Interval:  time.Hour,
		EventTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ObserveEntity(EntityObservation{
		Type:      model.TypeProcess,
		Identity:  proc,
		Interval:  time.Hour,
		EventTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.ObserveRelation(RelationObservation{
		Type:      model.RelRunsOn,
		From:      EndpointRef{Type: model.TypeProcess, Identity: proc},
		To:        EndpointRef{Type: model.TypeHost, Identity: host},
		Interval:  time.Minute,
		EventTime: now,
	}); err != nil {
		t.Fatalf("observe relation: %v", err)
	}

	now = now.Add(2 * time.Minute)

	ap.fail = true
	res, err := e.Sweep()
	if err == nil {
		t.Fatal("expected sweep to fail when the flush fails")
	}
	if res.Total() != 0 {
		t.Fatalf("failed sweep must report zero, got %d", res.Total())
	}
	if g.RelationCount() != 1 {
		t.Fatalf("relation must remain after a failed flush, got %d", g.RelationCount())
	}

	ap.fail = false
	res, err = e.Sweep()
	if err != nil {
		t.Fatalf("retry sweep: %v", err)
	}
	if res.Relations != 1 || res.Total() != 1 {
		t.Fatalf("retry sweep must expire the relation, got entities=%d relations=%d", res.Entities, res.Relations)
	}
	if g.RelationCount() != 0 {
		t.Fatalf("relation must be gone after a successful expiry sweep, got %d", g.RelationCount())
	}
}

func TestSweepFlushFailureRetriesCascade(t *testing.T) {
	now := t0
	ap := &failingAppender{}
	g := projection.New()
	e := New(g, ap,
		WithClock(func() time.Time { return now }),
		WithLogger(slog.New(slog.DiscardHandler)))

	host := []model.KeyValue{kv("host.name", "h-1")}
	proc := []model.KeyValue{kv("process.name", "p-1")}
	if _, err := e.ObserveEntity(EntityObservation{
		Type:      model.TypeHost,
		Identity:  host,
		Interval:  time.Minute,
		EventTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ObserveEntity(EntityObservation{
		Type:      model.TypeProcess,
		Identity:  proc,
		Interval:  time.Minute,
		EventTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.ObserveRelation(RelationObservation{
		Type:      model.RelRunsOn,
		From:      EndpointRef{Type: model.TypeProcess, Identity: proc},
		To:        EndpointRef{Type: model.TypeHost, Identity: host},
		EventTime: now,
	}); err != nil {
		t.Fatalf("observe relation: %v", err)
	}

	now = now.Add(2 * time.Minute)

	ap.fail = true
	res, err := e.Sweep()
	if err == nil {
		t.Fatal("expected sweep to fail when the flush fails")
	}
	if res.Total() != 0 {
		t.Fatalf("failed sweep must report zero, got %d", res.Total())
	}
	if g.EntityCount() != 2 {
		t.Fatalf("both entities must remain after a failed flush, got %d", g.EntityCount())
	}
	if g.RelationCount() != 1 {
		t.Fatalf("relation must remain after a failed flush, got %d", g.RelationCount())
	}

	ap.fail = false
	res, err = e.Sweep()
	if err != nil {
		t.Fatalf("retry sweep: %v", err)
	}
	if res.Entities != 2 || res.Relations != 1 {
		t.Fatalf("retry sweep must cascade, got entities=%d relations=%d", res.Entities, res.Relations)
	}
	if g.EntityCount() != 0 {
		t.Fatalf("entities must be gone after a successful expiry sweep, got %d", g.EntityCount())
	}
	if g.RelationCount() != 0 {
		t.Fatalf("relation must be gone after a successful expiry sweep, got %d", g.RelationCount())
	}
}
