package change

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

	// liveness: per-entity reference counts keyed by producer (the agent's
	// service.instance.id; "" for an anonymous/single producer), each with an
	// expiry deadline (zero = no interval, explicit-only). An entity is live while
	// any producer references it; it is deleted only when the last reference is
	// released (explicit delete or interval expiry). See ADR 0019. Per-relation
	// edge deadlines for the optional per-edge TTL. Guarded by obsMu.
	refs         map[model.EntityID]map[string]liveRef
	relDeadlines map[model.RelationID]liveRef

	// batch staging: while a Batch runs, commit stages each event and its
	// projection effects here instead of applying them; the durable append, the
	// projection apply, and the subscriber notifications all happen at flush
	// time, in that order, so neither the graph nor subscribers ever see an
	// event the log did not durably record (#108). Guarded by obsMu.
	staged *staged

	subMu sync.RWMutex
	subs  map[int]Subscriber
	subID int
}

type pendingRelation struct {
	obs      RelationObservation
	deadline time.Time
}

// liveRef is one armed liveness backstop: the absolute expiry deadline and the
// producer-declared heartbeat interval that armed it. The interval is kept so
// a deadline restored from a snapshot can be re-floored after downtime (see
// RestoreLiveness). A zero deadline means explicit-only (no interval).
type liveRef struct {
	deadline time.Time
	interval time.Duration
}

// staged is a batch's uncommitted unit of work: the events to flush plus an
// overlay of their projection effects. Classification reads consult the overlay
// first (through the engine's matchIdentity/getEntity/getRelation helpers) so
// in-batch observations classify consistently, while the projection itself
// stays untouched until the durable append succeeds.
type staged struct {
	events []stagedEvent

	entities   map[model.EntityID]model.Entity
	deleted    map[model.EntityID]bool
	byHash     map[string]model.EntityID
	relations  map[model.RelationID]model.Relation
	relRemoved map[model.RelationID]bool

	rollbacks []func()
}

type stagedEvent struct {
	ev model.Event
	hp bool
}

func newStaged() *staged {
	return &staged{
		entities:   make(map[model.EntityID]model.Entity),
		deleted:    make(map[model.EntityID]bool),
		byHash:     make(map[string]model.EntityID),
		relations:  make(map[model.RelationID]model.Relation),
		relRemoved: make(map[model.RelationID]bool),
	}
}

// apply records ev's projection effect on the overlay, mirroring what
// projection.Graph.Apply will do at flush time for the event kinds the engine
// emits.
func (st *staged) apply(ev model.Event) {
	switch {
	case ev.Entity != nil:
		ee := ev.Entity
		switch ee.ChangeType {
		case model.EntityCreated:
			st.entities[ee.Entity.ID] = ee.Entity
			delete(st.deleted, ee.Entity.ID)
			st.byHash[ee.Entity.IdentityHash()] = ee.Entity.ID
		case model.EntityAttributeUpdated, model.EntityStateChanged:
			st.entities[ee.Entity.ID] = ee.Entity
		case model.EntityDeleted:
			st.deleted[ee.Entity.ID] = true
		case model.EntityUnchanged:
			// presence was established by the identity match that classified
			// the observation as unchanged; nothing to overlay.
		}
	case ev.Relation != nil:
		re := ev.Relation
		switch re.ChangeType {
		case model.RelationAdded, model.RelationAttributeChanged:
			st.relations[re.Relation.ID] = re.Relation
			delete(st.relRemoved, re.Relation.ID)
		case model.RelationRemoved:
			delete(st.relations, re.Relation.ID)
			st.relRemoved[re.Relation.ID] = true
		}
	}
}

// errEndpointMissing marks a relation whose endpoint entity is not (yet) present,
// distinguishing a reconcilable out-of-order edge from a real failure.
var errEndpointMissing = errors.New("relation endpoint not found")

// maxPendingRelations caps the out-of-order reconciliation buffer: every parked
// edge is rescanned on each entity observation, so an unbounded buffer turns a
// chatty producer with broken references into a quadratic ingest cost (#115).
const maxPendingRelations = 4096

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
		refs:         make(map[model.EntityID]map[string]liveRef),
		relDeadlines: make(map[model.RelationID]liveRef),
	}
	for _, o := range opts {
		o(e)
	}
	// The projection's tombstone grace window must judge "expired" on the same
	// clock the engine stamps events with, so tests with a fake clock stay
	// deterministic (#183).
	graph.SetClock(e.now)
	return e
}

// Subscribe registers fn and returns a function that unsubscribes it.
//
// CONTRACT (#143): fn runs synchronously on the commit path, after the durable
// append, while the engine's observation lock is held. It MUST NOT block — a
// blocking subscriber freezes this tenant's ingestion — and MUST NOT call back
// into the engine (Observe*/Delete*/Sweep/Batch), which would deadlock. Fan
// out to consumers through a bounded queue and count your drops, as the
// GraphQL subscription stream does (it announces gaps in-band); do not do
// consumer work inline here.
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

// matchIdentity, getEntity, getRelation, and listRelationsTouching are the
// classification reads: they see the batch's staged effects layered over the
// projection, so in-batch observations classify as if earlier staged events
// were already applied, without the projection running ahead of the log.

func (e *Engine) matchIdentity(typ string, identity []model.KeyValue) (model.EntityID, bool) {
	st := e.staged
	if st == nil {
		return e.graph.MatchIdentity(typ, identity)
	}
	hash := (model.Entity{Type: typ, Identity: identity}).IdentityHash()
	if id, ok := st.byHash[hash]; ok {
		if st.deleted[id] {
			return "", false
		}
		return id, true
	}
	if id, ok := e.graph.MatchIdentity(typ, identity); ok && !st.deleted[id] {
		return id, true
	}
	return "", false
}

// matchTombstone resolves a soft-deleted entity's id for resurrection, layering
// the batch's staged effects over the projection like matchIdentity: an id
// deleted earlier in this same batch must not be resurrected by a later
// observation in it.
func (e *Engine) matchTombstone(typ string, identity []model.KeyValue) (model.EntityID, bool) {
	id, ok := e.graph.MatchTombstone(typ, identity)
	if !ok {
		return "", false
	}
	if st := e.staged; st != nil && st.deleted[id] {
		return "", false
	}
	return id, true
}

func (e *Engine) getEntity(id model.EntityID) (model.Entity, bool, bool) {
	st := e.staged
	if st == nil {
		return e.graph.GetEntity(id)
	}
	if ent, ok := st.entities[id]; ok {
		return ent, true, st.deleted[id]
	}
	ent, ok, deleted := e.graph.GetEntity(id)
	return ent, ok, deleted || st.deleted[id]
}

func (e *Engine) getRelation(id model.RelationID) (model.Relation, bool) {
	st := e.staged
	if st != nil {
		if st.relRemoved[id] {
			return model.Relation{}, false
		}
		if r, ok := st.relations[id]; ok {
			return r, true
		}
	}
	return e.graph.GetRelation(id)
}

// listRelationsTouching returns the deduplicated edges incident to id, staged
// versions taking precedence over the projection's, staged removals excluded.
func (e *Engine) listRelationsTouching(id model.EntityID) []model.Relation {
	st := e.staged
	var out []model.Relation
	seen := make(map[model.RelationID]struct{})
	if st != nil {
		for _, r := range st.relations {
			if r.From == id || r.To == id {
				out = append(out, r)
				seen[r.ID] = struct{}{}
			}
		}
	}
	for _, r := range append(e.graph.ListRelations("", id, ""), e.graph.ListRelations("", "", id)...) {
		if _, dup := seen[r.ID]; dup {
			continue
		}
		seen[r.ID] = struct{}{}
		if st != nil && st.relRemoved[r.ID] {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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
	// Producer identifies the observing agent (its service.instance.id). Liveness
	// is reference-counted per producer: an entity stays live while any producer
	// references it (ADR 0019). Empty means a single anonymous producer.
	Producer string
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
	return e.observeEntityLocked(obs)
}

func (e *Engine) observeEntityLocked(obs EntityObservation) (model.Event, error) {
	id, found := e.matchIdentity(obs.Type, obs.Identity)

	var (
		ct          model.ChangeType
		entityID    model.EntityID
		changedKeys []string
	)
	switch {
	case !found:
		// Resurrection: a re-asserted identity within the tombstone grace window
		// reclaims its original logical id, so entity_history stays continuous
		// across a producer outage instead of fragmenting across ULIDs (#183).
		if rid, ok := e.matchTombstone(obs.Type, obs.Identity); ok {
			ct, entityID = model.EntityCreated, rid
		} else {
			ct, entityID = model.EntityCreated, model.NewEntityID()
		}
	default:
		entityID = id
		existing, _, _ := e.getEntity(id)
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
	// Record this producer's reference, with its expiry deadline (zero = no
	// interval, released only by an explicit delete). See ADR 0019.
	producers := e.refs[entityID]
	if producers == nil {
		producers = make(map[string]liveRef)
		e.refs[entityID] = producers
	}
	if obs.Interval > 0 {
		producers[obs.Producer] = liveRef{deadline: e.now().Add(obs.Interval), interval: obs.Interval}
	} else {
		producers[obs.Producer] = liveRef{}
	}
	// A new/updated entity may be the missing endpoint of a parked edge.
	e.flushPending()
	return ev, nil
}

// DeleteEntity releases this producer's reference to an entity matched by
// identity. The entity is actually deleted (entity.deleted + cascade) only when
// the last producer has released it; while another producer still references it,
// the delete is a silent release with no event (ADR 0019). No matching entity
// means no event.
func (e *Engine) DeleteEntity(obs EntityObservation) (ev model.Event, emitted bool, err error) {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()
	return e.deleteEntityLocked(obs)
}

func (e *Engine) deleteEntityLocked(obs EntityObservation) (ev model.Event, emitted bool, err error) {
	id, found := e.matchIdentity(obs.Type, obs.Identity)
	if !found {
		return model.Event{}, false, nil
	}
	// Release this producer; if other producers still reference the entity, it
	// stays live.
	if producers, ok := e.refs[id]; ok {
		delete(producers, obs.Producer)
		if len(producers) > 0 {
			return model.Event{}, false, nil
		}
		delete(e.refs, id)
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
	e.removeIncidentRelations(id, obs.EventTime)
	return ev, true, nil
}

// removeIncidentRelations emits relation.removed for every edge touching id: an
// edge to a deleted entity is meaningless, so edge liveness is derived from its
// endpoints (a deleted node takes its edges with it). The caller must hold obsMu.
func (e *Engine) removeIncidentRelations(id model.EntityID, when time.Time) int {
	n := 0
	for _, rel := range e.listRelationsTouching(id) {
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

	// Drop each producer reference whose interval has lapsed; an entity with no
	// surviving reference is expired (ADR 0019).
	var orphaned []model.EntityID
	for id, producers := range e.refs {
		for p, ref := range producers {
			if !ref.deadline.IsZero() && now.After(ref.deadline) {
				delete(producers, p)
			}
		}
		if len(producers) == 0 {
			orphaned = append(orphaned, id)
		}
	}
	for _, id := range orphaned {
		ent, ok, deleted := e.graph.GetEntity(id)
		if !ok || deleted {
			delete(e.refs, id)
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
			continue // leave the (empty) ref set to retry next sweep
		}
		delete(e.refs, id)
		e.logger.Warn("expired stale entity: no producer heartbeat within its interval",
			"entity_type", ent.Type, "entity_id", id)
		n++
		n += e.removeIncidentRelations(id, now) // edges die with their node
	}

	// Expire parked out-of-order edges past their TTL: flushPending only runs on
	// entity observations, so without this a quiet instance never drops them.
	if len(e.pending) > 0 {
		kept := e.pending[:0]
		for i := range e.pending {
			if now.After(e.pending[i].deadline) {
				obs := e.pending[i].obs
				e.logger.Warn("dropping relation: endpoints did not arrive within the reconciliation TTL",
					"relation_type", obs.Type, "from_type", obs.From.Type, "to_type", obs.To.Type)
				continue
			}
			kept = append(kept, e.pending[i])
		}
		e.pending = kept
	}

	var expiredRelations []model.RelationID
	for id, ref := range e.relDeadlines {
		if now.After(ref.deadline) {
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

	// Bound the resurrection grace window in time: drop tombstones a producer
	// did not return to claim (#183). Not counted in n (no event is emitted —
	// the entity was already soft-deleted).
	e.graph.PruneTombstones()
	return n
}

// ObserveRelation classifies an observed relation. It emits relation.added for a
// new relation or relation.attribute_changed when an existing relation's
// attributes differ. If the relation already exists unchanged, no event is
// emitted (emitted=false).
func (e *Engine) ObserveRelation(obs RelationObservation) (ev model.Event, emitted bool, err error) {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()
	return e.observeRelationBuffered(obs)
}

// observeRelationBuffered runs observeRelationLocked and parks an out-of-order
// edge when the reconciliation buffer is enabled. The caller must hold obsMu.
func (e *Engine) observeRelationBuffered(obs RelationObservation) (model.Event, bool, error) {
	ev, emitted, err := e.observeRelationLocked(obs)
	if err != nil && e.bufferTTL > 0 && errors.Is(err, errEndpointMissing) {
		// Out-of-order edge: park it and retry as later entities arrive, instead
		// of failing. Not an error to the caller.
		if len(e.pending) >= maxPendingRelations {
			oldest := e.pending[0].obs
			e.pending = e.pending[1:]
			e.logger.Warn("pending-relation buffer full: dropping the oldest parked edge",
				"relation_type", oldest.Type, "from_type", oldest.From.Type, "to_type", oldest.To.Type,
				"cap", maxPendingRelations)
		}
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
		e.relDeadlines[rel.ID] = liveRef{deadline: e.now().Add(obs.Interval), interval: obs.Interval}
	} else {
		delete(e.relDeadlines, rel.ID)
	}

	var ct model.ChangeType
	if existing, ok := e.getRelation(rel.ID); ok {
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
	return e.removeRelationLocked(obs)
}

func (e *Engine) removeRelationLocked(obs RelationObservation) (ev model.Event, emitted bool, err error) {
	from, to, err := e.resolveEndpoints(obs.From, obs.To)
	if err != nil {
		// A missing endpoint means there is nothing to remove: the endpoint's
		// deletion already cascaded the edge away (or the edge never resolved).
		// Removal must be a no-op then, not an error — surfacing it would fail
		// the producer's entire export for an edge that is already gone (#110).
		if errors.Is(err, errEndpointMissing) {
			return model.Event{}, false, nil
		}
		return model.Event{}, false, err
	}
	id := model.ComputeRelationID(obs.Type, from, to)
	delete(e.relDeadlines, id) // explicit remove clears any liveness backstop
	existing, ok := e.getRelation(id)
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
	fromID, ok = e.matchIdentity(from.Type, from.Identity)
	if !ok {
		return "", "", fmt.Errorf("relation from-endpoint not found: type %q: %w", from.Type, errEndpointMissing)
	}
	toID, ok = e.matchIdentity(to.Type, to.Identity)
	if !ok {
		return "", "", fmt.Errorf("relation to-endpoint not found: type %q: %w", to.Type, errEndpointMissing)
	}
	return fromID, toID, nil
}

func (e *Engine) commit(ev model.Event, highPriority bool) error {
	if st := e.staged; st != nil {
		// Stage the event and its overlay effect; the durable append, the
		// projection apply, and the notify all happen at the batch flush, in
		// that order. In-batch classification sees the staged effects through
		// the overlay-aware read helpers, never through the projection (#108).
		st.events = append(st.events, stagedEvent{ev: ev, hp: highPriority})
		st.apply(ev)
		return nil
	}
	// Unbatched: same contract, degenerate form — durable append first, then
	// apply and notify.
	if err := e.appender.Append(ev); err != nil {
		return fmt.Errorf("appending qualified event: %w", err)
	}
	e.graph.Apply(ev)
	e.notify(ev, highPriority)
	return nil
}

// Batch processes a sequence of observations under a single lock and commits all
// resulting events to the store in one durable batch append (one fsync) instead
// of one per event. The OTLP receiver uses it per export to lift the
// fsync-bound ingestion ceiling. fn receives a Batch whose Observe/Delete methods
// mirror the engine's but run lock-free; do not retain it past the call.
//
// The batch is a staged unit of work: events reach the projection and the
// subscribers only after the durable append succeeds. On a failed flush the
// projection still matches the log and nothing was broadcast, so the producer's
// retry re-classifies every observation against durable state and regenerates
// the lost events (#108). Liveness bookkeeping (refs, relation deadlines, the
// out-of-order buffer) touched by a failed batch is intentionally not rolled
// back: re-observation overwrites it, and Sweep self-heals entries pointing at
// entities or relations that never materialized.
func (e *Engine) Batch(fn func(*Batch)) error {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()

	st := newStaged()
	e.staged = st
	fn(&Batch{e})
	e.staged = nil

	if len(st.events) > 0 {
		evs := make([]model.Event, len(st.events))
		for i := range st.events {
			evs[i] = st.events[i].ev
		}
		if err := e.appender.Append(evs...); err != nil {
			// Undo side state advanced during the batch, most recent first.
			for i := len(st.rollbacks) - 1; i >= 0; i-- {
				st.rollbacks[i]()
			}
			return fmt.Errorf("flushing batch append: %w", err)
		}
		for i := range st.events {
			e.graph.Apply(st.events[i].ev)
			e.notify(st.events[i].ev, st.events[i].hp)
		}
	}
	return nil
}

// Batch routes observations to the engine with the obsMu already held by Batch.
type Batch struct{ e *Engine }

// OnRollback registers fn to run — in reverse registration order — if the
// batch's durable flush fails. Callers that advance side state during the batch
// (the ingest reconciler's assertion sets) register its undo here, so a failed
// flush restores it and the producer's retry re-derives the same events.
// Dropped on success.
func (b *Batch) OnRollback(fn func()) {
	b.e.staged.rollbacks = append(b.e.staged.rollbacks, fn)
}

// OnRollback on the engine itself is a no-op: outside a batch every commit is
// durable before the observation returns, so there is no flush to roll back.
func (e *Engine) OnRollback(func()) {}

// ObserveEntity mirrors Engine.ObserveEntity within a batch.
func (b *Batch) ObserveEntity(obs EntityObservation) (model.Event, error) {
	return b.e.observeEntityLocked(obs)
}

// DeleteEntity mirrors Engine.DeleteEntity within a batch.
func (b *Batch) DeleteEntity(obs EntityObservation) (model.Event, bool, error) {
	return b.e.deleteEntityLocked(obs)
}

// ObserveRelation mirrors Engine.ObserveRelation within a batch.
func (b *Batch) ObserveRelation(obs RelationObservation) (model.Event, bool, error) {
	return b.e.observeRelationBuffered(obs)
}

// RemoveRelation mirrors Engine.RemoveRelation within a batch.
func (b *Batch) RemoveRelation(obs RelationObservation) (model.Event, bool, error) {
	return b.e.removeRelationLocked(obs)
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

// livenessSnapshot is the serialized form of the engine's liveness bookkeeping
// (the Memento, #139): producer references with their absolute expiry
// deadlines, and per-relation deadlines. Absolute times mean downtime counts
// against them; the parallel interval maps let RestoreLiveness re-floor lapsed
// deadlines so a restart after long downtime does not mass-delete entities of
// producers that are alive but have not re-exported yet. Blobs written before
// the interval maps existed decode with zero intervals and keep their stored
// deadlines as-is.
type livenessSnapshot struct {
	Refs         map[string]map[string]time.Time     `json:"refs"`
	RelDeadlines map[string]time.Time                `json:"rel_deadlines"`
	RefIntervals map[string]map[string]time.Duration `json:"ref_intervals,omitempty"`
	RelIntervals map[string]time.Duration            `json:"rel_intervals,omitempty"`
}

// LivenessBlob serializes the current liveness bookkeeping for inclusion in
// the projection snapshot.
func (e *Engine) LivenessBlob() ([]byte, error) {
	e.obsMu.Lock()
	snap := livenessSnapshot{
		Refs:         make(map[string]map[string]time.Time, len(e.refs)),
		RelDeadlines: make(map[string]time.Time, len(e.relDeadlines)),
		RefIntervals: make(map[string]map[string]time.Duration, len(e.refs)),
		RelIntervals: make(map[string]time.Duration, len(e.relDeadlines)),
	}
	for id, producers := range e.refs {
		ds := make(map[string]time.Time, len(producers))
		is := make(map[string]time.Duration, len(producers))
		for p, ref := range producers {
			ds[p] = ref.deadline
			is[p] = ref.interval
		}
		snap.Refs[string(id)] = ds
		snap.RefIntervals[string(id)] = is
	}
	for id, ref := range e.relDeadlines {
		snap.RelDeadlines[string(id)] = ref.deadline
		snap.RelIntervals[string(id)] = ref.interval
	}
	e.obsMu.Unlock()
	b, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("marshaling liveness snapshot: %w", err)
	}
	return b, nil
}

// RestoreLiveness seeds the liveness bookkeeping from a snapshot blob, called
// once at boot after the projection is restored. Entries pointing at entities
// or relations the snapshot does not contain are harmless: Sweep self-heals
// them. A nil/empty blob (pre-#139 snapshots) is a no-op.
func (e *Engine) RestoreLiveness(blob []byte) error {
	if len(blob) == 0 {
		return nil
	}
	var snap livenessSnapshot
	if err := json.Unmarshal(blob, &snap); err != nil {
		return fmt.Errorf("unmarshaling liveness snapshot: %w", err)
	}
	now := e.now()
	e.obsMu.Lock()
	defer e.obsMu.Unlock()
	for id, producers := range snap.Refs {
		ps := make(map[string]liveRef, len(producers))
		for p, deadline := range producers {
			interval := snap.RefIntervals[id][p]
			ps[p] = liveRef{deadline: floorDeadline(deadline, interval, now), interval: interval}
		}
		e.refs[model.EntityID(id)] = ps
	}
	for id, deadline := range snap.RelDeadlines {
		interval := snap.RelIntervals[id]
		e.relDeadlines[model.RelationID(id)] = liveRef{deadline: floorDeadline(deadline, interval, now), interval: interval}
	}
	return nil
}

// floorDeadline raises a restored deadline to now+interval: after downtime
// longer than a producer's heartbeat interval every stored deadline has
// lapsed, and without the floor the first sweep would mass-delete entities of
// producers that are alive but have not re-exported yet, only for them to be
// re-created moments later. The trade-off: an entity of a producer that truly
// died during the downtime lingers at most one extra interval before the
// sweep reaps it. Zero deadlines (explicit-only) and entries without a known
// interval (pre-interval blobs) are returned unchanged.
func floorDeadline(deadline time.Time, interval time.Duration, now time.Time) time.Time {
	if deadline.IsZero() || interval <= 0 {
		return deadline
	}
	if floor := now.Add(interval); deadline.Before(floor) {
		return floor
	}
	return deadline
}
