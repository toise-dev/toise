package change

import (
	"log/slog"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// A shared entity is asserted by several producers, and only some of them carry
// a descriptor for a given edge. Before edges were reference-counted, any
// producer's removal deleted the edge outright, so a producer that had never
// asserted it could retract another's (toise-dev/toise#396).

func refcountEngine(t *testing.T, now *time.Time) (*Engine, *projection.Graph) {
	t.Helper()
	g := projection.New()
	e := New(g, &fakeAppender{}, WithClock(func() time.Time { return *now }),
		WithLogger(slog.New(slog.DiscardHandler)))
	return e, g
}

// bothEnds observes the two endpoints an edge needs, each asserted by both
// producers so the entities outlive either one's release.
func bothEnds(t *testing.T, e *Engine, when time.Time, interval time.Duration) (from, to EndpointRef) {
	t.Helper()
	from = EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}}
	to = EndpointRef{Type: model.TypeNetworkAddress, Identity: []model.KeyValue{kv("network.address", "10.0.0.1")}}
	for _, p := range []string{"pA", "pB"} {
		for _, ref := range []EndpointRef{from, to} {
			if _, err := e.ObserveEntity(EntityObservation{
				Type: ref.Type, Identity: ref.Identity, Producer: p, Interval: interval, EventTime: when,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return from, to
}

func relationCount(g *projection.Graph, typ string) int {
	return len(g.ListRelations(typ, "", ""))
}

func TestRelationSurvivesUntilTheLastProducerReleasesIt(t *testing.T) {
	now := t0
	e, g := refcountEngine(t, &now)
	from, to := bothEnds(t, e, now, time.Minute)

	obs := func(p string) RelationObservation {
		return RelationObservation{Type: model.RelBoundTo, From: from, To: to, Producer: p, EventTime: now}
	}
	for _, p := range []string{"pA", "pB"} {
		if _, _, err := e.ObserveRelation(obs(p)); err != nil {
			t.Fatal(err)
		}
	}
	if got := relationCount(g, model.RelBoundTo); got != 1 {
		t.Fatalf("after both producers asserted it: %d edges, want 1", got)
	}

	// pA stops listing the edge. pB still asserts it, so nothing is removed and
	// no relation.removed is emitted — this is the defect the issue describes.
	_, emitted, err := e.RemoveRelation(obs("pA"))
	if err != nil {
		t.Fatal(err)
	}
	if emitted {
		t.Error("removing one producer's reference emitted relation.removed while another producer still asserts the edge")
	}
	if got := relationCount(g, model.RelBoundTo); got != 1 {
		t.Fatalf("after pA released it: %d edges, want the edge to survive pB's assertion", got)
	}

	// pB releases it too: the last reference is gone, so now it goes.
	_, emitted, err = e.RemoveRelation(obs("pB"))
	if err != nil {
		t.Fatal(err)
	}
	if !emitted {
		t.Error("releasing the last reference emitted no relation.removed")
	}
	if got := relationCount(g, model.RelBoundTo); got != 0 {
		t.Fatalf("after the last release: %d edges, want 0", got)
	}
}

func TestRelationExpiresOnlyWhenEveryProducerIntervalLapses(t *testing.T) {
	now := t0
	e, g := refcountEngine(t, &now)
	from, to := bothEnds(t, e, now, time.Hour)

	// pA refreshes every minute, pB every ten. The edge must outlive pA's lapse.
	for p, iv := range map[string]time.Duration{"pA": time.Minute, "pB": 10 * time.Minute} {
		if _, _, err := e.ObserveRelation(RelationObservation{
			Type: model.RelBoundTo, From: from, To: to, Producer: p, Interval: iv, EventTime: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	now = now.Add(2 * time.Minute) // pA has lapsed, pB has not
	if _, err := e.Sweep(); err != nil {
		t.Fatal(err)
	}
	if got := relationCount(g, model.RelBoundTo); got != 1 {
		t.Fatalf("after the shorter interval lapsed: %d edges, want the edge to survive the longer one", got)
	}

	now = now.Add(10 * time.Minute) // pB has lapsed too
	if _, err := e.Sweep(); err != nil {
		t.Fatal(err)
	}
	if got := relationCount(g, model.RelBoundTo); got != 0 {
		t.Fatalf("after every interval lapsed: %d edges, want 0", got)
	}
}

// Reference counting must not keep an edge alive past its endpoint: a dead node
// takes every edge with it, whoever asserted them.
func TestEntityDeathCascadesEdgesOfEveryProducer(t *testing.T) {
	now := t0
	e, g := refcountEngine(t, &now)
	from, to := bothEnds(t, e, now, time.Minute)

	for _, p := range []string{"pA", "pB"} {
		if _, _, err := e.ObserveRelation(RelationObservation{
			Type: model.RelBoundTo, From: from, To: to, Producer: p, EventTime: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := relationCount(g, model.RelBoundTo); got != 1 {
		t.Fatalf("setup: %d edges, want 1", got)
	}

	for _, p := range []string{"pA", "pB"} {
		if _, _, err := e.DeleteEntity(EntityObservation{
			Type: to.Type, Identity: to.Identity, Producer: p, EventTime: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := relationCount(g, model.RelBoundTo); got != 0 {
		t.Fatalf("after the endpoint died: %d edges, want the cascade to take them all", got)
	}
}

// A snapshot written before edges were reference-counted holds one deadline per
// edge and no producer. It must restore as the anonymous producer rather than
// lose its backstop across the upgrade.
func TestRestoreLivenessAcceptsThePreRefcountSnapshotForm(t *testing.T) {
	now := t0
	e, _ := refcountEngine(t, &now)

	blob := []byte(`{"refs":{},"rel_deadlines":{"rel-1":"` +
		now.Add(time.Minute).Format(time.RFC3339Nano) + `"},"rel_intervals":{"rel-1":60000000000}}`)
	if err := e.RestoreLiveness(blob); err != nil {
		t.Fatal(err)
	}
	refs, ok := e.relRefs[model.RelationID("rel-1")]
	if !ok {
		t.Fatal("a pre-refcount snapshot's edge deadline was dropped on restore")
	}
	if _, ok := refs[""]; !ok || len(refs) != 1 {
		t.Fatalf("restored references = %v, want exactly one under the anonymous producer", refs)
	}
}
