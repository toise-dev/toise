package ingest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

type relDesc struct {
	relType, toType string
	toID            map[string]string
	// belief attributes for same_as edges; emitted only when set.
	confidence float64
	basis      string
}

// embeddedRecord builds an entity-state LogRecord carrying an embedded
// `entity.relationships` array (the OTel spec form). rels == nil emits an entity
// state with no relationships.
func embeddedRecord(srcType string, srcID map[string]string, rels []relDesc) plog.LogRecord {
	lr := plog.NewLogRecord()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1_700_000_100, 0)))
	lr.SetEventName(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, srcType)
	idm := a.PutEmptyMap(attrEntityID)
	for k, v := range srcID {
		idm.PutStr(k, v)
	}
	if rels != nil {
		sl := a.PutEmptySlice(attrEntityRelationships)
		for _, d := range rels {
			m := sl.AppendEmpty().SetEmptyMap()
			m.PutStr(relDescType, d.relType)
			m.PutStr(relDescEntityType, d.toType)
			tid := m.PutEmptyMap(relDescEntityID)
			for k, v := range d.toID {
				tid.PutStr(k, v)
			}
			if d.confidence != 0 {
				m.PutDouble(relDescConfidence, d.confidence)
			}
			if d.basis != "" {
				m.PutStr(relDescBasis, d.basis)
			}
		}
	}
	return lr
}

func TestEmbeddedReconcilerAddsAndRemoves(t *testing.T) {
	r := newEmbeddedReconciler()
	f := &fakeEngine{}
	svc := map[string]string{"service.instance.id": "s1"}
	runsOn := []relDesc{{relType: "runs_on", toType: "host", toID: map[string]string{"host.id": "h1"}}}

	// 1. state with runs_on -> host h1: the relation is observed.
	if _, err := r.handle(f, embeddedRecord("service.instance", svc, runsOn)); err != nil {
		t.Fatal(err)
	}
	if f.relAdds != 1 || f.relRemoves != 0 {
		t.Fatalf("after add: relAdds=%d relRemoves=%d, want 1/0", f.relAdds, f.relRemoves)
	}

	// 2. re-emit the same set: idempotent re-observe (heartbeat), no removal.
	if _, err := r.handle(f, embeddedRecord("service.instance", svc, runsOn)); err != nil {
		t.Fatal(err)
	}
	if f.relAdds != 2 || f.relRemoves != 0 {
		t.Fatalf("after re-emit: relAdds=%d relRemoves=%d, want 2/0", f.relAdds, f.relRemoves)
	}

	// 3. re-emit WITHOUT the relationship: removal inferred by absence.
	if _, err := r.handle(f, embeddedRecord("service.instance", svc, nil)); err != nil {
		t.Fatal(err)
	}
	if f.relRemoves != 1 {
		t.Fatalf("after drop: relRemoves=%d, want 1", f.relRemoves)
	}
	if g := f.lastRelation; g.Type != "runs_on" || g.From.Type != "service.instance" || g.To.Type != "host" {
		t.Errorf("removed relation = %+v, want runs_on service.instance->host", g)
	}
}

// TestEmbeddedReconcilerCarriesSameAsBelief proves confidence and basis reach the
// relation observation for a same_as edge — the input the canonical overlay (ADR
// 0020) needs. Without this the overlay was inert: no producer could feed a belief.
func TestEmbeddedReconcilerCarriesSameAsBelief(t *testing.T) {
	r := newEmbeddedReconciler()
	f := &fakeEngine{}
	dev := map[string]string{"name": "sw1"}
	sameAs := []relDesc{{
		relType: model.RelSameAs, toType: "network.device",
		toID:       map[string]string{"mac": "00:11:22:33:44:55"},
		confidence: 0.95, basis: "ifPhysAddress",
	}}
	if _, err := r.handle(f, embeddedRecord("network.device", dev, sameAs)); err != nil {
		t.Fatal(err)
	}
	attrs := map[string]model.Value{}
	for _, kv := range f.lastRelation.Attributes {
		attrs[kv.Key] = kv.Value
	}
	if c, ok := attrs["confidence"]; !ok || c.Kind() != model.KindDouble || c.Double() != 0.95 {
		t.Errorf("confidence attribute = %v, want double 0.95", attrs["confidence"])
	}
	if b, ok := attrs["basis"]; !ok || b.Display() != "ifPhysAddress" {
		t.Errorf("basis attribute = %v, want ifPhysAddress", attrs["basis"])
	}
}

// TestEmbeddedReconcilerBeliefOnlyOnSameAs pins that belief attributes are carried
// only on same_as edges: a runs_on descriptor that happens to carry confidence
// gets none, keeping ordinary embedded edges attribute-free (ADR 0022).
func TestEmbeddedReconcilerBeliefOnlyOnSameAs(t *testing.T) {
	r := newEmbeddedReconciler()
	f := &fakeEngine{}
	svc := map[string]string{"service.instance.id": "s1"}
	runsOn := []relDesc{{
		relType: "runs_on", toType: "host", toID: map[string]string{"host.id": "h1"},
		confidence: 0.9, basis: "nonsense",
	}}
	if _, err := r.handle(f, embeddedRecord("service.instance", svc, runsOn)); err != nil {
		t.Fatal(err)
	}
	if len(f.lastRelation.Attributes) != 0 {
		t.Errorf("runs_on carried %d belief attributes, want 0", len(f.lastRelation.Attributes))
	}
}

func TestEmbeddedReconcilerEntityDeleteForgets(t *testing.T) {
	r := newEmbeddedReconciler()
	f := &fakeEngine{}
	svc := map[string]string{"service.instance.id": "s1"}
	if _, err := r.handle(f, embeddedRecord("service.instance", svc,
		[]relDesc{{relType: "runs_on", toType: "host", toID: map[string]string{"host.id": "h1"}}})); err != nil {
		t.Fatal(err)
	}
	// Delete the source: the engine cascades its incident relations, so the
	// reconciler just forgets its bookkeeping.
	del := embeddedRecord("service.instance", svc, nil)
	del.SetEventName(evEntityDelete)
	if _, err := r.handle(f, del); err != nil {
		t.Fatal(err)
	}
	// A later re-create with no relationships must NOT try to remove the already
	// cascaded relation.
	before := f.relRemoves
	if _, err := r.handle(f, embeddedRecord("service.instance", svc, nil)); err != nil {
		t.Fatal(err)
	}
	if f.relRemoves != before {
		t.Errorf("forgotten source must not trigger a removal; relRemoves %d->%d", before, f.relRemoves)
	}
}

func TestEmbeddedReconcilerIgnoresNonEntity(t *testing.T) {
	r := newEmbeddedReconciler()
	f := &fakeEngine{}
	// A record with no EventName is not an entity event: nothing to reconcile.
	if drop, err := r.handle(f, newRecord("")); err != nil || drop != nil {
		t.Fatalf("non-entity record: drop=%v err=%v, want nil/nil", drop, err)
	}
	if f.relAdds != 0 {
		t.Errorf("non-entity record produced %d relation adds, want 0", f.relAdds)
	}
}

func TestEmbeddedReconcilerDropsMalformedDescriptor(t *testing.T) {
	r := newEmbeddedReconciler()
	f := &fakeEngine{}
	lr := embeddedRecord("host", map[string]string{"host.id": "h1"}, nil)
	// A descriptor missing entity.id is malformed.
	m := lr.Attributes().PutEmptySlice(attrEntityRelationships).AppendEmpty().SetEmptyMap()
	m.PutStr(relDescType, "runs_on")
	m.PutStr(relDescEntityType, "host")
	drop, err := r.handle(f, lr)
	if err != nil {
		t.Fatal(err)
	}
	if len(drop) == 0 {
		t.Error("expected the malformed descriptor to be reported as dropped")
	}
	if f.relAdds != 0 {
		t.Errorf("malformed descriptor produced %d adds, want 0", f.relAdds)
	}
}

// TestEmbeddedReconcilerUnknownRelationTypeVocabulary pins the #163 boundary
// check: in strict mode a descriptor with an unregistered relationship.type
// invalidates the record (errInvalidRecord, naming the type) while its valid
// sibling descriptors are still observed; with the open vocabulary (#141) the
// unknown type passes.
func TestEmbeddedReconcilerUnknownRelationTypeVocabulary(t *testing.T) {
	svc := map[string]string{"service.instance.id": "s1"}
	rels := []relDesc{
		{relType: "acme.made.up", toType: model.TypeHost, toID: map[string]string{"host.id": "h1"}},
		{relType: model.RelRunsOn, toType: model.TypeHost, toID: map[string]string{"host.id": "h1"}},
	}

	f := &fakeEngine{}
	_, err := newEmbeddedReconciler().handle(f, embeddedRecord(model.TypeServiceInstance, svc, rels))
	if !errors.Is(err, errInvalidRecord) {
		t.Fatalf("strict mode: err = %v, want errInvalidRecord", err)
	}
	if !strings.Contains(err.Error(), "acme.made.up") {
		t.Errorf("error must name the unknown type, got %q", err)
	}
	if f.relAdds != 1 || f.lastRelation.Type != model.RelRunsOn {
		t.Errorf("valid sibling descriptor: relAdds=%d last=%q, want 1 runs_on", f.relAdds, f.lastRelation.Type)
	}

	open := &fakeEngine{}
	if _, err := newEmbeddedReconciler().handleVocab(open, embeddedRecord(model.TypeServiceInstance, svc, rels), false); err != nil {
		t.Fatalf("open vocabulary: %v", err)
	}
	if open.relAdds != 2 {
		t.Errorf("open vocabulary relAdds = %d, want 2", open.relAdds)
	}
}

type failingAppender struct{ fail bool }

func (f *failingAppender) Append(...model.Event) error {
	if f.fail {
		return errors.New("append failed (injected)")
	}
	return nil
}

// TestReconcilerStateRollsBackOnFlushFailure pins the #108 retry contract for
// removal-by-absence: when the batch flush fails, the reconciler's assertion
// set must roll back with it, so the producer's retried export re-derives the
// removal. Embedded edges carry no liveness interval — a removal lost here
// would never be retried otherwise.
func TestReconcilerStateRollsBackOnFlushFailure(t *testing.T) {
	g := projection.New()
	ap := &failingAppender{}
	eng := change.New(g, ap)
	r := newEmbeddedReconciler()

	svc := map[string]string{"service.instance.id": "s1"}
	hostID := map[string]string{"host.id": "h1"}
	runsOn := []relDesc{{relType: model.RelRunsOn, toType: model.TypeHost, toID: hostID}}

	route := func(records ...plog.LogRecord) error {
		return eng.Batch(func(b *change.Batch) {
			for _, lr := range records {
				if _, _, err := routeRecord(b, lr, "p1"); err != nil {
					t.Fatalf("route: %v", err)
				}
				if _, err := r.handle(b, lr); err != nil {
					t.Fatalf("reconcile: %v", err)
				}
			}
		})
	}

	host := embeddedRecord(model.TypeHost, hostID, nil)
	if err := route(host, embeddedRecord(model.TypeServiceInstance, svc, runsOn)); err != nil {
		t.Fatalf("initial export: %v", err)
	}
	if g.RelationCount() != 1 {
		t.Fatalf("RelationCount = %d, want 1", g.RelationCount())
	}

	// s1 drops the descriptor, but the flush fails: the graph keeps the edge
	// and the reconciler state must roll back to keep asserting it.
	ap.fail = true
	if err := route(embeddedRecord(model.TypeServiceInstance, svc, nil)); err == nil {
		t.Fatal("flush failure must surface")
	}
	if g.RelationCount() != 1 {
		t.Fatalf("RelationCount = %d after failed flush, want 1 (projection must not run ahead)", g.RelationCount())
	}

	// The producer retries the same export: the removal must be re-derived
	// from the restored state and now reach the log and the graph.
	ap.fail = false
	if err := route(embeddedRecord(model.TypeServiceInstance, svc, nil)); err != nil {
		t.Fatalf("retried export: %v", err)
	}
	if g.RelationCount() != 0 {
		t.Errorf("RelationCount = %d after retry, want 0 (removal re-derived)", g.RelationCount())
	}
}
