package ingest

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
)

// LogRecord attribute keys for the phase-1 entity-event convention. See ADR
// 0009 and docs/data-model/otel-mapping.md.
const (
	attrEventType   = "otel.entity.event.type"
	attrEntityType  = "otel.entity.type"
	attrEntityID    = "otel.entity.id"
	attrEntityAttrs = "otel.entity.attributes"
	// interval is the producer's heartbeat cadence in milliseconds; it is a
	// liveness backstop (a stale entity is expired), not a primary delete signal.
	attrEntityInterval = "otel.entity.interval"

	// resAttrProducer is the OTLP Resource attribute identifying the producing
	// agent; liveness is reference-counted per producer (ADR 0019).
	resAttrProducer = "service.instance.id"

	// Embedded relationships (OTel entity-events spec, PR #4836): an entity *state*
	// event MAY carry an `entity.relationships` array; each descriptor is a map with
	// the relationship `type` and the target's `entity.type` + `entity.id` (map).
	// This is the sole on-wire relationship form (ADR 0022): the source is implicit
	// (the entity carrying the array) and removal is by absence on re-emit (no
	// explicit relation-delete). The ingest boundary translates each descriptor into
	// the engine's first-class relation events. See docs/data-model/otel-mapping.md.
	attrEntityRelationships = "entity.relationships"
	relDescType             = "type"
	relDescEntityType       = "entity.type"
	relDescEntityID         = "entity.id"
)

// Lifecycle values for otel.entity.event.type.
const (
	evEntityState  = "entity_state"
	evEntityDelete = "entity_delete"
)

// engine is the subset of *change.Engine the receiver routes to.
type engine interface {
	ObserveEntity(change.EntityObservation) (model.Event, error)
	DeleteEntity(change.EntityObservation) (model.Event, bool, error)
	ObserveRelation(change.RelationObservation) (model.Event, bool, error)
	RemoveRelation(change.RelationObservation) (model.Event, bool, error)
}

// routeRecord converts an entity-event LogRecord and routes it to the engine.
// handled is false for LogRecords that are not Toise entity events (ignored).
// routeRecord returns the keys of any non-scalar attribute values it dropped, so
// the caller can surface the loss rather than discard data silently (the
// producer's contract is flat scalar maps; a nested value is a producer bug worth
// seeing).
func routeRecord(e engine, lr plog.LogRecord, producer string) (handled bool, dropped []string, err error) {
	attrs := lr.Attributes()
	when := eventTimeOf(lr)

	et, ok := strAttr(attrs, attrEventType)
	if !ok {
		return false, nil, nil // not an entity event (relations ride embedded on entity events)
	}
	switch et {
	case evEntityState:
		obs, drop, oerr := entityObs(attrs, when)
		if oerr != nil {
			return true, drop, oerr
		}
		obs.Producer = producer
		_, oerr = e.ObserveEntity(obs)
		return true, drop, oerr
	case evEntityDelete:
		obs, drop, oerr := entityObs(attrs, when)
		if oerr != nil {
			return true, drop, oerr
		}
		obs.Producer = producer
		_, _, oerr = e.DeleteEntity(obs)
		return true, drop, oerr
	default:
		return false, nil, nil
	}
}

func entityObs(attrs pcommon.Map, when time.Time) (change.EntityObservation, []string, error) {
	typ, ok := strAttr(attrs, attrEntityType)
	if !ok {
		return change.EntityObservation{}, nil, fmt.Errorf("missing %s", attrEntityType)
	}
	ident, identDropped, ok := mapAttr(attrs, attrEntityID)
	if !ok || len(ident) == 0 {
		return change.EntityObservation{}, identDropped, fmt.Errorf("missing or empty %s", attrEntityID)
	}
	descriptive, descDropped, _ := mapAttr(attrs, attrEntityAttrs)
	dropped := make([]string, 0, len(identDropped)+len(descDropped))
	dropped = append(dropped, identDropped...)
	dropped = append(dropped, descDropped...)
	var interval time.Duration
	if ms, ok := intAttr(attrs, attrEntityInterval); ok && ms > 0 {
		interval = time.Duration(ms) * time.Millisecond
	}
	return change.EntityObservation{
		Type:       typ,
		Identity:   ident,
		Attributes: descriptive,
		Interval:   interval,
		EventTime:  when,
	}, dropped, nil
}

func eventTimeOf(lr plog.LogRecord) time.Time {
	if ts := lr.Timestamp(); ts != 0 {
		return ts.AsTime()
	}
	if ts := lr.ObservedTimestamp(); ts != 0 {
		return ts.AsTime()
	}
	return time.Now()
}

func intAttr(attrs pcommon.Map, key string) (int64, bool) {
	v, ok := attrs.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeInt {
		return 0, false
	}
	return v.Int(), true
}

func strAttr(attrs pcommon.Map, key string) (string, bool) {
	v, ok := attrs.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeStr {
		return "", false
	}
	return v.Str(), true
}

func mapAttr(attrs pcommon.Map, key string) ([]model.KeyValue, []string, bool) {
	v, ok := attrs.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeMap {
		return nil, nil, false
	}
	kvs, dropped := kvsFromMap(v.Map(), key)
	return kvs, dropped, true
}

// kvsFromMap converts a pcommon.Map of scalar values to Toise KeyValues. It
// returns the dotted keys (prefixed with the map's attribute name) of any
// non-scalar entries it dropped, so the caller can surface the loss.
func kvsFromMap(m pcommon.Map, prefix string) (kvs []model.KeyValue, dropped []string) {
	kvs = make([]model.KeyValue, 0, m.Len())
	m.Range(func(k string, v pcommon.Value) bool {
		if mv, ok := valueFrom(v); ok {
			kvs = append(kvs, model.KeyValue{Key: k, Value: mv})
		} else {
			dropped = append(dropped, prefix+"."+k)
		}
		return true
	})
	return kvs, dropped
}

func valueFrom(v pcommon.Value) (model.Value, bool) {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return model.StringValue(v.Str()), true
	case pcommon.ValueTypeInt:
		return model.IntValue(v.Int()), true
	case pcommon.ValueTypeDouble:
		return model.DoubleValue(v.Double()), true
	case pcommon.ValueTypeBool:
		return model.BoolValue(v.Bool()), true
	default:
		return model.Value{}, false
	}
}
