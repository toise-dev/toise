package ingest

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
)

type fakeEngine struct {
	entities, deletes, relAdds, relRemoves int
	lastEntity                             change.EntityObservation
	lastRelation                           change.RelationObservation
}

func (f *fakeEngine) ObserveEntity(o change.EntityObservation) (model.Event, error) {
	f.entities++
	f.lastEntity = o
	return model.Event{}, nil
}

func (f *fakeEngine) DeleteEntity(o change.EntityObservation) (model.Event, bool, error) {
	f.deletes++
	f.lastEntity = o
	return model.Event{}, true, nil
}

func (f *fakeEngine) ObserveRelation(o change.RelationObservation) (model.Event, bool, error) {
	f.relAdds++
	f.lastRelation = o
	return model.Event{}, true, nil
}

func (f *fakeEngine) RemoveRelation(o change.RelationObservation) (model.Event, bool, error) {
	f.relRemoves++
	f.lastRelation = o
	return model.Event{}, true, nil
}

func newRecord(eventType string) plog.LogRecord {
	lr := plog.NewLogRecord()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(1_700_000_000, 0)))
	if eventType != "" {
		lr.Attributes().PutStr(attrEventType, eventType)
	}
	return lr
}

func TestRouteEntityState(t *testing.T) {
	lr := newRecord(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	id := a.PutEmptyMap(attrEntityID)
	id.PutStr("host.id", "h1")
	attrs := a.PutEmptyMap(attrEntityAttrs)
	attrs.PutInt("cpu.count", 8)
	attrs.PutBool("up", true)
	attrs.PutDouble("load", 0.5)

	f := &fakeEngine{}
	handled, _, err := routeRecord(f, lr, "")
	if !handled || err != nil {
		t.Fatalf("routeRecord handled=%v err=%v", handled, err)
	}
	if f.entities != 1 {
		t.Fatalf("entities = %d, want 1", f.entities)
	}
	if f.lastEntity.Type != model.TypeHost || len(f.lastEntity.Identity) != 1 {
		t.Errorf("observation = %+v", f.lastEntity)
	}
	// typed values survive conversion
	kinds := map[string]model.ValueKind{}
	for _, kv := range f.lastEntity.Attributes {
		kinds[kv.Key] = kv.Value.Kind()
	}
	if kinds["cpu.count"] != model.KindInt || kinds["up"] != model.KindBool || kinds["load"] != model.KindDouble {
		t.Errorf("attribute kinds = %+v", kinds)
	}
}

func TestRouteIgnoresNonEntity(t *testing.T) {
	lr := newRecord("") // no otel.entity.event.type
	lr.Body().SetStr("plain log")
	f := &fakeEngine{}
	handled, _, err := routeRecord(f, lr, "")
	if handled || err != nil {
		t.Errorf("non-entity record: handled=%v err=%v, want false,nil", handled, err)
	}

	// unknown event type is also ignored
	if handled, _, err := routeRecord(f, newRecord("something_else"), ""); handled || err != nil {
		t.Errorf("unknown event type: handled=%v err=%v", handled, err)
	}
}

func TestRouteSurfacesDroppedNonScalar(t *testing.T) {
	lr := newRecord(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", "h1")
	attrs := a.PutEmptyMap(attrEntityAttrs)
	attrs.PutStr("os.type", "linux") // scalar: kept
	attrs.PutEmptyMap("nested")      // non-scalar: dropped and reported
	attrs.PutEmptySlice("tags")      // non-scalar: dropped and reported
	f := &fakeEngine{}
	handled, dropped, err := routeRecord(f, lr, "")
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	got := map[string]bool{}
	for _, k := range dropped {
		got[k] = true
	}
	if !got["otel.entity.attributes.nested"] || !got["otel.entity.attributes.tags"] {
		t.Errorf("dropped = %v, want the nested map and slice keys surfaced", dropped)
	}
	if len(f.lastEntity.Attributes) != 1 || f.lastEntity.Attributes[0].Key != "os.type" {
		t.Errorf("kept attributes = %+v, want just the scalar os.type", f.lastEntity.Attributes)
	}
}

func TestRouteParsesInterval(t *testing.T) {
	lr := newRecord(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", "h1")
	a.PutInt(attrEntityInterval, 60_000) // 60s in milliseconds
	f := &fakeEngine{}
	if _, _, err := routeRecord(f, lr, ""); err != nil {
		t.Fatal(err)
	}
	if f.lastEntity.Interval != time.Minute {
		t.Errorf("interval = %v, want 1m", f.lastEntity.Interval)
	}
}

func TestRouteSetsProducer(t *testing.T) {
	lr := newRecord(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", "h1")
	f := &fakeEngine{}
	if _, _, err := routeRecord(f, lr, "agent-7f3a"); err != nil {
		t.Fatal(err)
	}
	if f.lastEntity.Producer != "agent-7f3a" {
		t.Errorf("producer = %q, want agent-7f3a", f.lastEntity.Producer)
	}
}

func TestRouteEntityMissingID(t *testing.T) {
	lr := newRecord(evEntityState)
	lr.Attributes().PutStr(attrEntityType, model.TypeHost)
	// no otel.entity.id
	f := &fakeEngine{}
	handled, _, err := routeRecord(f, lr, "")
	if !handled || err == nil {
		t.Errorf("missing id: handled=%v err=%v, want true,err", handled, err)
	}
}

func TestRouteEntityDelete(t *testing.T) {
	lr := newRecord(evEntityDelete)
	a := lr.Attributes()
	a.PutStr(attrEntityType, model.TypeHost)
	a.PutEmptyMap(attrEntityID).PutStr("host.id", "h1")
	f := &fakeEngine{}
	if handled, _, err := routeRecord(f, lr, ""); !handled || err != nil || f.deletes != 1 {
		t.Errorf("delete: handled=%v err=%v deletes=%d", handled, err, f.deletes)
	}
}
