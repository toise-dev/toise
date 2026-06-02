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

	// The relation extension uses a vendor-neutral namespace (neither a producer
	// nor a consumer prefix) so any producer/consumer can speak it and it maps
	// 1:1 onto the future OTel relationships standard (OTEP 0256 Future Work).
	// Strict purity: a relation record carries NO otel.entity.* attribute (its own
	// lifecycle key is entity.relation.event.type), so a standard OTel
	// entity-events consumer sees no malformed entity event and cleanly ignores it.
	// See docs/data-model/otel-mapping.md.
	attrRelEventType = "entity.relation.event.type"
	attrRelInterval  = "entity.relation.interval" // heartbeat cadence in ms; edge liveness backstop
	attrRelType      = "entity.relation.type"
	attrRelFromType  = "entity.relation.from.type"
	attrRelFromID    = "entity.relation.from.id"
	attrRelToType    = "entity.relation.to.type"
	attrRelToID      = "entity.relation.to.id"
	attrRelAttrs     = "entity.relation.attributes"
)

// Lifecycle values: entity events on otel.entity.event.type, relation events on
// entity.relation.event.type.
const (
	evEntityState  = "entity_state"
	evEntityDelete = "entity_delete"
	evRelState     = "state"
	evRelDelete    = "delete"
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

	if et, ok := strAttr(attrs, attrEventType); ok { // entity event (standard OTel)
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

	if rt, ok := strAttr(attrs, attrRelEventType); ok { // relation event (extension)
		switch rt {
		case evRelState:
			obs, drop, oerr := relationObs(attrs, when)
			if oerr != nil {
				return true, drop, oerr
			}
			_, _, oerr = e.ObserveRelation(obs)
			return true, drop, oerr
		case evRelDelete:
			obs, drop, oerr := relationObs(attrs, when)
			if oerr != nil {
				return true, drop, oerr
			}
			_, _, oerr = e.RemoveRelation(obs)
			return true, drop, oerr
		default:
			return false, nil, nil
		}
	}

	return false, nil, nil // neither an entity nor a relation event
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
	var dropped []string
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

func relationObs(attrs pcommon.Map, when time.Time) (change.RelationObservation, []string, error) {
	relType, ok := strAttr(attrs, attrRelType)
	if !ok {
		return change.RelationObservation{}, nil, fmt.Errorf("missing %s", attrRelType)
	}
	fromType, okFT := strAttr(attrs, attrRelFromType)
	fromID, fromDropped, okFI := mapAttr(attrs, attrRelFromID)
	toType, okTT := strAttr(attrs, attrRelToType)
	toID, toDropped, okTI := mapAttr(attrs, attrRelToID)
	var dropped []string
	dropped = append(dropped, fromDropped...)
	dropped = append(dropped, toDropped...)
	if !okFT || !okFI || !okTT || !okTI {
		return change.RelationObservation{}, dropped, fmt.Errorf("relation %q missing endpoint attributes", relType)
	}
	relAttrs, attrDropped, _ := mapAttr(attrs, attrRelAttrs)
	dropped = append(dropped, attrDropped...)
	var interval time.Duration
	if ms, ok := intAttr(attrs, attrRelInterval); ok && ms > 0 {
		interval = time.Duration(ms) * time.Millisecond
	}
	return change.RelationObservation{
		Type:       relType,
		From:       change.EndpointRef{Type: fromType, Identity: fromID},
		To:         change.EndpointRef{Type: toType, Identity: toID},
		Attributes: relAttrs,
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
