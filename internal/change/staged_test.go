package change

import (
	"fmt"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// observer is the classification surface shared by the Engine and an in-flight
// Batch, so the same sequence can be driven through both.
type observer interface {
	ObserveEntity(EntityObservation) (model.Event, error)
	ObserveRelation(RelationObservation) (model.Event, bool, error)
	RemoveRelation(RelationObservation) (model.Event, bool, error)
}

// TestStagedClassificationMatchesUnbatched is the differential pin between the
// batch's staged overlay (staged.apply) and the projection (Graph.Apply): the
// same observation sequence must classify into the same event sequence whether
// processed one-by-one against the graph or inside one Batch against the
// overlay. If staged.apply stops mirroring Graph.Apply for any ChangeType the
// engine emits — in-batch presence, identity, or relation resolution — the two
// event streams diverge and this fails (#166 P1).
func TestStagedClassificationMatchesUnbatched(t *testing.T) {
	host := []model.KeyValue{kv("host.id", "h1")}
	proc := EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "1")}}
	hostRef := EndpointRef{Type: model.TypeHost, Identity: host}

	// A sequence whose later ops depend on earlier ones being visible: the
	// unchanged/state/attr classifications need the create seen, and the edge
	// needs both endpoints resolved within the same processing unit.
	run := func(o observer) {
		_, _ = o.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Attributes: []model.KeyValue{kv("status", "up")}, EventTime: t0})
		_, _ = o.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Attributes: []model.KeyValue{kv("status", "up")}, EventTime: t0})
		_, _ = o.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Attributes: []model.KeyValue{kv("status", "down")}, EventTime: t0})
		_, _ = o.ObserveEntity(EntityObservation{Type: model.TypeHost, Identity: host, Attributes: []model.KeyValue{kv("status", "down"), kv("os", "linux")}, EventTime: t0})
		_, _ = o.ObserveEntity(EntityObservation{Type: model.TypeProcess, Identity: proc.Identity, EventTime: t0})
		_, _, _ = o.ObserveRelation(RelationObservation{Type: model.RelRunsOn, From: proc, To: hostRef, EventTime: t0})
		_, _, _ = o.RemoveRelation(RelationObservation{Type: model.RelRunsOn, From: proc, To: hostRef, EventTime: t0})
	}

	capture := func(batched bool) []model.ChangeType {
		g := projection.New()
		e := New(g, &fakeAppender{}, WithClock(fixedNow))
		var kinds []model.ChangeType
		e.Subscribe(func(ev model.Event, _ bool) {
			switch {
			case ev.Entity != nil:
				kinds = append(kinds, ev.Entity.ChangeType)
			case ev.Relation != nil:
				kinds = append(kinds, ev.Relation.ChangeType)
			}
		})
		if batched {
			if err := e.Batch(func(b *Batch) { run(b) }); err != nil {
				t.Fatalf("batch: %v", err)
			}
		} else {
			run(e)
		}
		return kinds
	}

	unbatched := capture(false)
	batched := capture(true)
	if fmt.Sprint(unbatched) != fmt.Sprint(batched) {
		t.Errorf("staged overlay diverged from the projection:\n unbatched=%v\n batched  =%v", unbatched, batched)
	}
}
