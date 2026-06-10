package ingest

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
)

// embeddedReconciler ingests the OTel entity-events spec's embedded relationship
// model (spec PR #4836): an entity *state* event carries an `entity.relationships`
// array, and the relationship lifecycle is the source entity's — a relation a
// producer stops listing on its entity's state is removed (no explicit
// relation-delete on the wire).
//
// Per ADR 0022 this lives at the ingest boundary: it translates the embedded wire
// form into the engine's first-class relation events (ObserveRelation /
// RemoveRelation), leaving the engine unchanged. Embedded relationships are the
// sole on-wire relationship form.
//
// State is the set of embedded relations each source entity currently asserts,
// keyed by a stable wire-identity string (source → relation key → observation),
// so the removal diff needs no resolved entity IDs. It is in-memory: after a
// restart the first re-emit re-establishes the set (no spurious removals), and the
// interval liveness backstop covers producers that vanish without re-emitting.
type embeddedReconciler struct {
	mu    sync.Mutex
	state map[string]map[string]change.RelationObservation
}

func newEmbeddedReconciler() *embeddedReconciler {
	return &embeddedReconciler{state: make(map[string]map[string]change.RelationObservation)}
}

// handle reconciles the embedded relationships carried by one entity-event record.
// It is a no-op for non-entity records (those are routed by routeRecord). It
// returns the dotted keys of any non-scalar or malformed descriptor values it
// dropped, so the caller can surface the loss rather than discard it silently.
func (r *embeddedReconciler) handle(e engine, lr plog.LogRecord) (dropped []string, err error) {
	attrs := lr.Attributes()
	et := lr.EventName()
	if et != evEntityState && et != evEntityDelete {
		return nil, nil // not an entity event
	}
	sourceType, okT := strAttr(attrs, attrEntityType)
	sourceID, idDropped, okI := mapAttr(attrs, attrEntityID)
	if !okT || !okI || len(sourceID) == 0 {
		// malformed/incomplete entity event — routeRecord surfaces the error; we
		// have no source to key on, so there is nothing to reconcile.
		return idDropped, nil
	}
	source := change.EndpointRef{Type: sourceType, Identity: sourceID}
	sk := endpointKey(source)

	switch et {
	case evEntityDelete:
		// the entity is gone; the engine cascades its incident relations. Drop our
		// bookkeeping for it so a later re-creation starts clean.
		r.mu.Lock()
		prev, had := r.state[sk]
		delete(r.state, sk)
		r.mu.Unlock()
		if had {
			e.OnRollback(func() {
				r.mu.Lock()
				r.state[sk] = prev
				r.mu.Unlock()
			})
		}
		return idDropped, nil
	case evEntityState:
		when := eventTimeOf(lr)
		rels, relDropped := embeddedRelations(attrs, source, when)
		dropped = append(dropped, idDropped...)
		dropped = append(dropped, relDropped...)
		if rerr := r.reconcile(e, sk, rels, when); rerr != nil {
			return dropped, rerr
		}
		return dropped, nil
	default:
		return idDropped, nil
	}
}

// reconcile observes the desired relations and removes any the source previously
// asserted via embedded relationships but no longer lists. Removals are stamped
// with the current record's event time.
func (r *embeddedReconciler) reconcile(e engine, sourceKey string, desired []change.RelationObservation, when time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prevState, hadPrev := r.state[sourceKey]
	want := make(map[string]change.RelationObservation, len(desired))
	var errs []error
	for i := range desired {
		want[relationKey(desired[i])] = desired[i]
		if _, _, err := e.ObserveRelation(desired[i]); err != nil {
			errs = append(errs, err)
		}
	}
	for k := range prevState {
		if _, keep := want[k]; keep {
			continue
		}
		prev := prevState[k]
		prev.EventTime = when // the removal happens now, not when it was first seen
		if _, _, err := e.RemoveRelation(prev); err != nil {
			errs = append(errs, err)
		}
	}
	// The state always advances to what the source just asserted, even when an
	// observe/remove failed: keeping a stale entry would replay the same failure
	// on every subsequent export from that producer (#110).
	if len(want) == 0 {
		delete(r.state, sourceKey)
	} else {
		r.state[sourceKey] = want
	}
	// A failed batch flush discards the events this diff produced; restore the
	// pre-diff assertion set so the producer's retry re-derives the same
	// removals. Embedded edges carry no liveness interval, so a removal lost
	// here would otherwise never be retried (#108).
	e.OnRollback(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if hadPrev {
			r.state[sourceKey] = prevState
		} else {
			delete(r.state, sourceKey)
		}
	})
	return errors.Join(errs...)
}

// embeddedRelations parses an entity-state record's `entity.relationships` array
// into relation observations: From is the source entity, To is the descriptor
// target. A malformed descriptor (non-map element, missing/empty fields) is
// dropped (its key surfaced), never silently merged.
func embeddedRelations(attrs pcommon.Map, source change.EndpointRef, when time.Time) (rels []change.RelationObservation, dropped []string) {
	v, ok := attrs.Get(attrEntityRelationships)
	if !ok || v.Type() != pcommon.ValueTypeSlice {
		return nil, nil
	}
	sl := v.Slice()
	for i := 0; i < sl.Len(); i++ {
		key := fmt.Sprintf("%s[%d]", attrEntityRelationships, i)
		el := sl.At(i)
		if el.Type() != pcommon.ValueTypeMap {
			dropped = append(dropped, key)
			continue
		}
		m := el.Map()
		relType, okT := strFromMap(m, relDescType)
		toType, okTT := strFromMap(m, relDescEntityType)
		idv, hasID := m.Get(relDescEntityID)
		if !okT || !okTT || !hasID || idv.Type() != pcommon.ValueTypeMap {
			dropped = append(dropped, key)
			continue
		}
		toID, idDropped := kvsFromMap(idv.Map(), key+"."+relDescEntityID)
		dropped = append(dropped, idDropped...)
		if len(toID) == 0 {
			dropped = append(dropped, key+"."+relDescEntityID)
			continue
		}
		rels = append(rels, change.RelationObservation{
			Type:      relType,
			From:      source,
			To:        change.EndpointRef{Type: toType, Identity: toID},
			EventTime: when,
		})
	}
	return rels, dropped
}

func strFromMap(m pcommon.Map, key string) (string, bool) {
	v, ok := m.Get(key)
	if !ok || v.Type() != pcommon.ValueTypeStr {
		return "", false
	}
	return v.Str(), true
}

// endpointKey is a stable wire-identity string for a relation endpoint.
func endpointKey(ep change.EndpointRef) string {
	return ep.Type + "|" + canonicalIdentity(ep.Identity)
}

// relationKey identifies an embedded relation within its source (From is always
// the source), so it keys on the relation type and the target endpoint.
func relationKey(o change.RelationObservation) string {
	return o.Type + "=>" + endpointKey(o.To)
}

func canonicalIdentity(kvs []model.KeyValue) string {
	parts := make([]string, len(kvs))
	for i, kv := range kvs {
		parts[i] = kv.Key + "=" + kv.Value.String()
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
