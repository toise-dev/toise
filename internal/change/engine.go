package change

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// stateKeys are the descriptive attribute keys whose change is classified as a
// state change rather than a plain attribute update. See ADR 0006.
var stateKeys = map[string]struct{}{
	"oper_state":  {},
	"admin_state": {},
	"status":      {},
}

// Appender persists qualified events. The store satisfies it.
type Appender interface {
	Append(events ...model.Event) error
}

// Subscriber is notified of each qualified event. highPriority is true for
// structural relation add/remove.
type Subscriber func(ev model.Event, highPriority bool)

// Engine classifies observations into qualified events. It is safe for
// concurrent use, but observations are serialized to keep classification
// consistent with the projection.
type Engine struct {
	graph    *projection.Graph
	appender Appender
	now      func() time.Time
	logger   *slog.Logger

	obsMu sync.Mutex // serializes observation processing

	// relation reconciliation buffer (opt-in via WithRelationBuffer): edges whose
	// endpoints are not yet present are parked here and retried when later entities
	// arrive, so out-of-order delivery does not drop edges. Guarded by obsMu.
	bufferTTL time.Duration
	pending   []pendingRelation

	// liveness backstop: per-entity and per-relation expiry deadlines, for entities
	// and edges observed with an interval. Sweep expires those past their deadline.
	// Guarded by obsMu.
	deadlines    map[model.EntityID]time.Time
	relDeadlines map[model.RelationID]time.Time

	subMu sync.RWMutex
	subs  map[int]Subscriber
	subID int
}

type pendingRelation struct {
	obs      RelationObservation
	deadline time.Time
}

// errEndpointMissing marks a relation whose endpoint entity is not (yet) present,
// distinguishing a reconcilable out-of-order edge from a real failure.
var errEndpointMissing = errors.New("relation endpoint not found")

// Option configures an Engine.
type Option func(*Engine)

// WithClock overrides the clock used to stamp recorded_at (for tests).
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// WithLogger sets the logger used for reconciliation warnings.
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithRelationBuffer enables the out-of-order edge reconciliation buffer with the
// given TTL: a relation whose endpoints are not yet present is parked and retried
// as later entities arrive, and dropped (with a warning) if its endpoints have not
// appeared within ttl. A non-positive ttl leaves the buffer disabled (a missing
// endpoint is then a retriable error, the default).
func WithRelationBuffer(ttl time.Duration) Option {
	return func(e *Engine) { e.bufferTTL = ttl }
}

// New returns an engine writing to appender and projecting into graph.
func New(graph *projection.Graph, appender Appender, opts ...Option) *Engine {
	e := &Engine{
		graph:        graph,
		appender:     appender,
		now:          time.Now,
		logger:       slog.Default(),
		subs:         make(map[int]Subscriber),
		deadlines:    make(map[model.EntityID]time.Time),
		relDeadlines: make(map[model.RelationID]time.Time),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Subscribe registers fn and returns a function that unsubscribes it.
func (e *Engine) Subscribe(fn Subscriber) func() {
	e.subMu.Lock()
	defer e.subMu.Unlock()
	id := e.subID
	e.subID++
	e.subs[id] = fn
	return func() {
		e.subMu.Lock()
		defer e.subMu.Unlock()
		delete(e.subs, id)
	}
}

func (e *Engine) notify(ev model.Event, highPriority bool) {
	e.subMu.RLock()
	defer e.subMu.RUnlock()
	for _, fn := range e.subs {
		fn(ev, highPriority)
	}
}

// EntityObservation is an entity's observed state at a point in time.
type EntityObservation struct {
	Type       string
	Identity   []model.KeyValue
	Attributes []model.KeyValue
	SchemaURL  string
	EventTime  time.Time
	// Interval is the producer's heartbeat cadence. When > 0 it arms a liveness
	// backstop: if the entity is not re-asserted within Interval, Sweep expires it
	// (a missed-delete safety net, not the primary delete signal). The producer
	// should size Interval to include slack for jitter/a missed heartbeat.
	Interval time.Duration
}

// EndpointRef references a relation endpoint by its entity identity.
type EndpointRef struct {
	Type     string
	Identity []model.KeyValue
}

// RelationObservation is an observed relation between two entities.
type RelationObservation struct {
	Type       string
	From       EndpointRef
	To         EndpointRef
	Attributes []model.KeyValue
	EventTime  time.Time
	// Interval, when > 0, arms the same liveness backstop as for entities: an edge
	// not re-asserted within Interval is expired (relation.removed) by Sweep.
	Interval time.Duration
}

// ObserveEntity classifies an entity observation, persists the qualified event,
// updates the projection, and notifies subscribers. It always emits an event
// (an unchanged observation produces an entity.unchanged heartbeat).
func (e *Engine) ObserveEntity(obs EntityObservation) (model.Event, error) {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()

	id, found := e.graph.MatchIdentity(obs.Type, obs.Identity)

	var (
		ct          model.ChangeType
		entityID    model.EntityID
		changedKeys []string
	)
	switch {
	case !found:
		ct, entityID = model.EntityCreated, model.NewEntityID()
	default:
		entityID = id
		existing, _, _ := e.graph.GetEntity(id)
		changed, stateChanged := diffAttributes(existing.Attributes, obs.Attributes)
		switch {
		case len(changed) == 0:
			ct = model.EntityUnchanged
		case stateChanged:
			ct = model.EntityStateChanged
		default:
			ct = model.EntityAttributeUpdated
		}
		changedKeys = changed
	}

	ev := model.Event{Entity: &model.EntityEvent{
		EventID:    model.NewEventID(),
		ChangeType: ct,
		Entity: model.Entity{
			ID:         entityID,
			Type:       obs.Type,
			Identity:   obs.Identity,
			Attributes: obs.Attributes,
			SchemaURL:  obs.SchemaURL,
		},
		EventTime:     obs.EventTime,
		RecordedAt:    e.now(),
		SchemaVersion: model.SchemaVersion,
		ChangedKeys:   changedKeys,
	}}
	if err := e.commit(ev, false); err != nil {
		return model.Event{}, err
	}
	// Arm (or clear) the liveness backstop for this entity.
	if obs.Interval > 0 {
		e.deadlines[entityID] = e.now().Add(obs.Interval)
	} else {
		delete(e.deadlines, entityID)
	}
	// A new/updated entity may be the missing endpoint of a parked edge.
	e.flushPending()
	return ev, nil
}

// DeleteEntity emits entity.deleted for an entity matched by identity. If no
// matching entity exists, no event is emitted (emitted=false).
func (e *Engine) DeleteEntity(obs EntityObservation) (ev model.Event, emitted bool, err error) {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()

	id, found := e.graph.MatchIdentity(obs.Type, obs.Identity)
	if !found {
		return model.Event{}, false, nil
	}
	ev = model.Event{Entity: &model.EntityEvent{
		EventID:    model.NewEventID(),
		ChangeType: model.EntityDeleted,
		Entity: model.Entity{
			ID:         id,
			Type:       obs.Type,
			Identity:   obs.Identity,
			Attributes: obs.Attributes,
			SchemaURL:  obs.SchemaURL,
		},
		EventTime:     obs.EventTime,
		RecordedAt:    e.now(),
		SchemaVersion: model.SchemaVersion,
	}}
	if err := e.commit(ev, false); err != nil {
		return model.Event{}, false, err
	}
	delete(e.deadlines, id) // explicit delete clears any liveness backstop
	e.removeIncidentRelations(id, obs.EventTime)
	return ev, true, nil
}

// removeIncidentRelations emits relation.removed for every edge touching id: an
// edge to a deleted entity is meaningless, so edge liveness is derived from its
// endpoints (a deleted node takes its edges with it). The caller must hold obsMu.
func (e *Engine) removeIncidentRelations(id model.EntityID, when time.Time) int {
	edges := append(e.graph.ListRelations("", id, ""), e.graph.ListRelations("", "", id)...)
	seen := make(map[model.RelationID]struct{}, len(edges))
	n := 0
	for _, rel := range edges {
		if _, dup := seen[rel.ID]; dup {
			continue
		}
		seen[rel.ID] = struct{}{}
		ev := e.relationEvent(model.RelationRemoved, rel, when)
		if err := e.commit(ev, rel.Structural); err != nil {
			e.logger.Error("failed to remove edge of deleted entity", "relation_id", rel.ID, "err", err)
			continue
		}
		delete(e.relDeadlines, rel.ID)
		n++
	}
	return n
}

// Sweep expires entities whose liveness backstop has lapsed: an entity observed
// or edge with an interval that has not been re-asserted within it is soft-deleted
// (entity.deleted / relation.removed), as a safety net for missed explicit deletes
// (crash, host off, network partition). It returns the number of entities and
// edges expired. Drive it from a periodic ticker.
func (e *Engine) Sweep() int {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()

	now := e.now()
	n := 0

	var expiredEntities []model.EntityID
	for id, deadline := range e.deadlines {
		if now.After(deadline) {
			expiredEntities = append(expiredEntities, id)
		}
	}
	for _, id := range expiredEntities {
		ent, ok, deleted := e.graph.GetEntity(id)
		if !ok || deleted {
			delete(e.deadlines, id)
			continue
		}
		ev := model.Event{Entity: &model.EntityEvent{
			EventID:       model.NewEventID(),
			ChangeType:    model.EntityDeleted,
			Entity:        ent, // last-known state
			EventTime:     now,
			RecordedAt:    now,
			SchemaVersion: model.SchemaVersion,
		}}
		if err := e.commit(ev, false); err != nil {
			e.logger.Error("failed to expire stale entity", "id", id, "err", err)
			continue // keep the deadline and retry next sweep
		}
		delete(e.deadlines, id)
		e.logger.Warn("expired stale entity: no heartbeat within its interval",
			"entity_type", ent.Type, "entity_id", id)
		n++
		n += e.removeIncidentRelations(id, now) // edges die with their node
	}

	var expiredRelations []model.RelationID
	for id, deadline := range e.relDeadlines {
		if now.After(deadline) {
			expiredRelations = append(expiredRelations, id)
		}
	}
	for _, id := range expiredRelations {
		rel, ok := e.graph.GetRelation(id)
		if !ok {
			delete(e.relDeadlines, id)
			continue
		}
		ev := e.relationEvent(model.RelationRemoved, rel, now)
		if err := e.commit(ev, rel.Structural); err != nil {
			e.logger.Error("failed to expire stale relation", "id", id, "err", err)
			continue
		}
		delete(e.relDeadlines, id)
		e.logger.Warn("expired stale relation: not re-asserted within its interval",
			"relation_type", rel.Type, "relation_id", id)
		n++
	}
	return n
}

// ObserveRelation classifies an observed relation. It emits relation.added for a
// new relation or relation.attribute_changed when an existing relation's
// attributes differ. If the relation already exists unchanged, no event is
// emitted (emitted=false).
func (e *Engine) ObserveRelation(obs RelationObservation) (ev model.Event, emitted bool, err error) {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()

	ev, emitted, err = e.observeRelationLocked(obs)
	if err != nil && e.bufferTTL > 0 && errors.Is(err, errEndpointMissing) {
		// Out-of-order edge: park it and retry as later entities arrive, instead
		// of failing. Not an error to the caller.
		e.pending = append(e.pending, pendingRelation{obs: obs, deadline: e.now().Add(e.bufferTTL)})
		return model.Event{}, false, nil
	}
	return ev, emitted, err
}

// observeRelationLocked resolves, classifies, and commits a relation. The caller
// must hold obsMu. It does not buffer — that is ObserveRelation's concern.
func (e *Engine) observeRelationLocked(obs RelationObservation) (model.Event, bool, error) {
	from, to, err := e.resolveEndpoints(obs.From, obs.To)
	if err != nil {
		return model.Event{}, false, err
	}
	rel := model.NewRelation(obs.Type, from, to, obs.Attributes...)

	// Arm (or clear) the edge liveness backstop, even when the observation is
	// otherwise unchanged — re-asserting an edge resets its deadline.
	if obs.Interval > 0 {
		e.relDeadlines[rel.ID] = e.now().Add(obs.Interval)
	} else {
		delete(e.relDeadlines, rel.ID)
	}

	var ct model.ChangeType
	if existing, ok := e.graph.GetRelation(rel.ID); ok {
		changed, _ := diffAttributes(existing.Attributes, obs.Attributes)
		if len(changed) == 0 {
			return model.Event{}, false, nil
		}
		ct = model.RelationAttributeChanged
	} else {
		ct = model.RelationAdded
	}

	ev := e.relationEvent(ct, rel, obs.EventTime)
	highPriority := rel.Structural && ct == model.RelationAdded
	if err := e.commit(ev, highPriority); err != nil {
		return model.Event{}, false, err
	}
	return ev, true, nil
}

// flushPending retries parked relations: those whose endpoints now resolve are
// committed and removed; those past their TTL are dropped with a warning (never
// silently); the rest stay parked. The caller must hold obsMu.
func (e *Engine) flushPending() {
	if len(e.pending) == 0 {
		return
	}
	now := e.now()
	var kept []pendingRelation
	for i := range e.pending {
		obs := e.pending[i].obs
		_, _, err := e.observeRelationLocked(obs)
		switch {
		case err == nil:
			// resolved and committed; drop from the buffer
		case errors.Is(err, errEndpointMissing) && now.After(e.pending[i].deadline):
			e.logger.Warn("dropping relation: endpoints did not arrive within the reconciliation TTL",
				"relation_type", obs.Type, "from_type", obs.From.Type, "to_type", obs.To.Type)
		default:
			kept = append(kept, e.pending[i]) // still waiting, or a transient commit error to retry
		}
	}
	e.pending = kept
}

// RemoveRelation emits relation.removed for an existing relation between the
// referenced endpoints. If the relation does not exist, no event is emitted.
func (e *Engine) RemoveRelation(obs RelationObservation) (ev model.Event, emitted bool, err error) {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()

	from, to, err := e.resolveEndpoints(obs.From, obs.To)
	if err != nil {
		return model.Event{}, false, err
	}
	id := model.ComputeRelationID(obs.Type, from, to)
	delete(e.relDeadlines, id) // explicit remove clears any liveness backstop
	existing, ok := e.graph.GetRelation(id)
	if !ok {
		return model.Event{}, false, nil
	}
	ev = e.relationEvent(model.RelationRemoved, existing, obs.EventTime)
	highPriority := existing.Structural
	if err := e.commit(ev, highPriority); err != nil {
		return model.Event{}, false, err
	}
	return ev, true, nil
}

func (e *Engine) relationEvent(ct model.ChangeType, rel model.Relation, eventTime time.Time) model.Event {
	return model.Event{Relation: &model.RelationEvent{
		EventID:       model.NewEventID(),
		ChangeType:    ct,
		Relation:      rel,
		EventTime:     eventTime,
		RecordedAt:    e.now(),
		SchemaVersion: model.SchemaVersion,
	}}
}

func (e *Engine) resolveEndpoints(from, to EndpointRef) (fromID, toID model.EntityID, err error) {
	var ok bool
	fromID, ok = e.graph.MatchIdentity(from.Type, from.Identity)
	if !ok {
		return "", "", fmt.Errorf("relation from-endpoint not found: type %q: %w", from.Type, errEndpointMissing)
	}
	toID, ok = e.graph.MatchIdentity(to.Type, to.Identity)
	if !ok {
		return "", "", fmt.Errorf("relation to-endpoint not found: type %q: %w", to.Type, errEndpointMissing)
	}
	return fromID, toID, nil
}

func (e *Engine) commit(ev model.Event, highPriority bool) error {
	if err := e.appender.Append(ev); err != nil {
		return fmt.Errorf("appending qualified event: %w", err)
	}
	e.graph.Apply(ev)
	e.notify(ev, highPriority)
	return nil
}

// diffAttributes returns the keys whose value differs between old and new (in
// either direction) and whether any of them is a state-flagged key.
func diffAttributes(oldAttrs, newAttrs []model.KeyValue) (changed []string, stateChanged bool) {
	om := canonMap(oldAttrs)
	nm := canonMap(newAttrs)
	for k, nv := range nm {
		if ov, ok := om[k]; !ok || ov != nv {
			changed = append(changed, k)
		}
	}
	for k := range om {
		if _, ok := nm[k]; !ok {
			changed = append(changed, k)
		}
	}
	for _, k := range changed {
		if _, ok := stateKeys[k]; ok {
			stateChanged = true
			break
		}
	}
	return changed, stateChanged
}

func canonMap(kvs []model.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.String()
	}
	return m
}
