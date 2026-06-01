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

	// The relation extension uses a vendor-neutral namespace (neither a producer
	// nor a consumer prefix) so any producer/consumer can speak it and it maps
	// 1:1 onto the future OTel relationships standard (OTEP 0256 Future Work).
	// It is explicitly transitional. See docs/data-model/otel-mapping.md.
	attrRelType     = "entity.relation.type"
	attrRelFromType = "entity.relation.from.type"
	attrRelFromID   = "entity.relation.from.id"
	attrRelToType   = "entity.relation.to.type"
	attrRelToID     = "entity.relation.to.id"
	attrRelAttrs    = "entity.relation.attributes"
)

// Event type values for the otel.entity.event.type attribute.
const (
	evEntityState    = "entity_state"
	evEntityDelete   = "entity_delete"
	evRelationState  = "relation_state"
	evRelationDelete = "relation_delete"
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
func routeRecord(e engine, lr plog.LogRecord) (handled bool, err error) {
	attrs := lr.Attributes()
	et, ok := strAttr(attrs, attrEventType)
	if !ok {
		return false, nil // not an entity event
	}
	when := eventTimeOf(lr)

	switch et {
	case evEntityState:
		obs, err := entityObs(attrs, when)
		if err != nil {
			return true, err
		}
		_, err = e.ObserveEntity(obs)
		return true, err
	case evEntityDelete:
		obs, err := entityObs(attrs, when)
		if err != nil {
			return true, err
		}
		_, _, err = e.DeleteEntity(obs)
		return true, err
	case evRelationState:
		obs, err := relationObs(attrs, when)
		if err != nil {
			return true, err
		}
		_, _, err = e.ObserveRelation(obs)
		return true, err
	case evRelationDelete:
		obs, err := relationObs(attrs, when)
		if err != nil {
			return true, err
		}
		_, _, err = e.RemoveRelation(obs)
		return true, err
	default:
		return false, nil
	}
}

func entityObs(attrs pcommon.Map, when time.Time) (change.EntityObservation, error) {
	typ, ok := strAttr(attrs, attrEntityType)
	if !ok {
		return change.EntityObservation{}, fmt.Errorf("missing %s", attrEntityType)
	}
	ident, ok := mapAttr(attrs, attrEntityID)
	if !ok || len(ident) == 0 {
		return change.EntityObservation{}, fmt.Errorf("missing or empty %s", attrEntityID)
	}
	descriptive, _ := mapAttr(attrs, attrEntityAttrs)
	return change.EntityObservation{
		Type:       typ,
		Identity:   ident,
		Attributes: descriptive,
		EventTime:  when,
	}, nil
}

func relationObs(attrs pcommon.Map, when time.Time) (change.RelationObservation, error) {
	relType, ok := strAttr(attrs, attrRelType)
	if !ok {
		return change.RelationObservation{}, fmt.Errorf("missing %s", attrRelType)
	}
	fromType, okFT := strAttr(attrs, attrRelFromType)
	fromID, okFI := mapAttr(attrs, attrRelFromID)
	toType, okTT := strAttr(attrs, attrRelToType)
	toID, okTI := mapAttr(attrs, attrRelToID)
	if !okFT || !okFI || !okTT || !okTI {
		return change.RelationObservation{}, fmt.Errorf("relation %q missing endpoint attributes", relType)
	}
	relAttrs, _ := mapAttr(attrs, attrRelAttrs)
	return change.RelationObservation{
		Type:       relType,
		From:       change.EndpointRef{Type: fromType, Identity: fromID},
		To:         change.EndpointRef{Type: toType, Identity: toID},
		Attributes: relAttrs,
		EventTime:  when,
	}, nil
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

func strAttr(attrs pcommon.Map, key string) (string, bool) {
	v, ok := attrs.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeStr {
		return "", false
	}
	return v.Str(), true
}

func mapAttr(attrs pcommon.Map, key string) ([]model.KeyValue, bool) {
	v, ok := attrs.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeMap {
		return nil, false
	}
	return kvsFromMap(v.Map()), true
}

// kvsFromMap converts a pcommon.Map of scalar values to Toise KeyValues,
// skipping non-scalar entries.
func kvsFromMap(m pcommon.Map) []model.KeyValue {
	out := make([]model.KeyValue, 0, m.Len())
	m.Range(func(k string, v pcommon.Value) bool {
		if mv, ok := valueFrom(v); ok {
			out = append(out, model.KeyValue{Key: k, Value: mv})
		}
		return true
	})
	return out
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
