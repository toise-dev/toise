package ingest

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/pkg/emit/wire"
)

// errInvalidRecord marks an entity record that violates the wire contract
// (unknown entity.type, missing identity, malformed key-values). The violation
// is permanent — a retry can never make the record valid — so the receiver
// rejects it per record via OTLP partial success instead of failing the export:
// one bad record must not block its valid siblings (#109).
var errInvalidRecord = errors.New("invalid entity record")

// The wire vocabulary is spelled once, in pkg/emit/wire, shared with the SDK
// and the conformance kit so producer and consumer cannot drift apart literal
// by literal. The local names are aliases kept for the package's readability.
//
// Embedded relationships: an entity *state* event MAY carry an
// `entity.relationships` array; each descriptor is a map with the
// `relationship.type` and the target's `entity.type` + `entity.id` (map). This
// is the sole on-wire relationship form (ADR 0022): the source is implicit (the
// entity carrying the array) and removal is by absence on re-emit (no explicit
// relation-delete). The ingest boundary translates each descriptor into the
// engine's first-class relation events. See docs/data-model/otel-mapping.md.
const (
	attrEntityType = wire.AttrEntityType
	attrEntityID   = wire.AttrEntityID
	attrEntityDesc = wire.AttrEntityDescription
	// report interval is the producer's heartbeat cadence in SECONDS; it is a
	// liveness backstop (a stale entity is expired), not a primary delete signal.
	attrEntityInterval = wire.AttrEntityReportInterval

	// resAttrProducer identifies the producing agent; liveness is
	// reference-counted per producer (ADR 0019).
	resAttrProducer = wire.ResServiceInstanceID

	// entity.delete.reason is an optional open-enum motive carried on delete
	// events; Toise keeps it verbatim and never rejects an unknown value.
	attrEntityDeleteReason = wire.AttrEntityDeleteReason

	attrEntityRelationships = wire.AttrEntityRelationships
	relDescType             = wire.RelType
	relDescEntityType       = wire.RelTargetType
	relDescEntityID         = wire.RelTargetID
)

// Entity lifecycle events are identified by the LogRecord EventName (OTel spec),
// not by an attribute.
const (
	evEntityState  = wire.EventEntityState
	evEntityDelete = wire.EventEntityDelete
)

// engine is the subset of *change.Engine the receiver routes to.
type engine interface {
	ObserveEntity(change.EntityObservation) (model.Event, error)
	DeleteEntity(change.EntityObservation) (model.Event, bool, error)
	ObserveRelation(change.RelationObservation) (model.Event, bool, error)
	RemoveRelation(change.RelationObservation) (model.Event, bool, error)
	// OnRollback registers an undo to run if the surrounding batch fails to
	// flush durably; outside a batch it is a no-op (commits are already durable).
	OnRollback(func())
}

// routeRecord converts an entity-event LogRecord and routes it to the engine.
// handled is false for LogRecords that are not Toise entity events (ignored).
// routeRecord returns the keys of any non-scalar attribute values it dropped, so
// the caller can surface the loss rather than discard data silently (the
// producer's contract is flat scalar maps; a nested value is a producer bug worth
// seeing).
func routeRecord(e engine, lr plog.LogRecord, producer string) (handled bool, dropped []string, err error) {
	return routeRecordVocab(e, lr, producer, true)
}

// routeRecordVocab is routeRecord with the vocabulary check selectable: with
// strictVocab false (accept_unknown_types, #141) an unknown entity.type passes
// as long as the record's shape is sound.
func routeRecordVocab(e engine, lr plog.LogRecord, producer string, strictVocab bool) (handled bool, dropped []string, err error) {
	attrs := lr.Attributes()
	when := eventTimeOf(lr)

	switch lr.EventName() {
	case evEntityState:
		obs, drop, oerr := entityObs(attrs, when, strictVocab)
		if oerr != nil {
			return true, drop, oerr
		}
		obs.Producer = producer
		_, oerr = e.ObserveEntity(obs)
		return true, drop, oerr
	case evEntityDelete:
		obs, drop, oerr := entityObs(attrs, when, strictVocab)
		if oerr != nil {
			return true, drop, oerr
		}
		obs.Producer = producer
		// entity.delete.reason is optional and an open enum: carry whatever the
		// producer sent, never validate it against a closed set.
		if reason, ok := strAttr(attrs, attrEntityDeleteReason); ok {
			obs.DeleteReason = reason
		}
		_, _, oerr = e.DeleteEntity(obs)
		return true, drop, oerr
	default:
		return false, nil, nil
	}
}

func entityObs(attrs pcommon.Map, when time.Time, strictVocab bool) (change.EntityObservation, []string, error) {
	typ, ok := strAttr(attrs, attrEntityType)
	if !ok {
		return change.EntityObservation{}, nil, fmt.Errorf("%w: missing %s", errInvalidRecord, attrEntityType)
	}
	ident, identDropped, ok := mapAttr(attrs, attrEntityID)
	if !ok || len(ident) == 0 {
		return change.EntityObservation{}, identDropped, fmt.Errorf("%w: missing or empty %s", errInvalidRecord, attrEntityID)
	}
	descriptive, descDropped, _ := mapAttr(attrs, attrEntityDesc)
	dropped := make([]string, 0, len(identDropped)+len(descDropped))
	dropped = append(dropped, identDropped...)
	dropped = append(dropped, descDropped...)
	// The wire contract requires well-formed key-values and — unless the
	// deployment opted into an open vocabulary (#141) — a registered
	// entity.type. Validating per record here, before classification, bounds
	// the blast radius: the store would otherwise reject the whole batch for
	// one bad record, after it was already applied and broadcast (#109).
	ent := model.Entity{Type: typ, Identity: ident, Attributes: descriptive}
	verr := ent.Validate()
	if !strictVocab {
		verr = ent.ValidateShape()
	}
	if verr != nil {
		return change.EntityObservation{}, dropped, fmt.Errorf("%w: %w", errInvalidRecord, verr)
	}
	var interval time.Duration
	if v, present := attrs.Get(attrEntityInterval); present {
		if v.Type() == pcommon.ValueTypeInt {
			if secs := v.Int(); secs > 0 {
				interval = time.Duration(secs) * time.Second
			}
		} else {
			// A mis-typed interval (e.g. the string "300") silently disarmed
			// the liveness backstop; surface it on the dropped-keys path so
			// the producer bug is visible (#115).
			dropped = append(dropped, attrEntityInterval)
		}
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
