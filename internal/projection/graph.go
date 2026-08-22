package projection

import (
	"sort"
	"sync"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// EventScanner replays events in append order. The store satisfies it.
type EventScanner interface {
	Scan(fn func(seq uint64, ev model.Event) error) error
}

// defaultTombstoneCap bounds how many soft-deleted entities keep their full
// payload readable by id. Beyond it the oldest tombstones are evicted from the
// projection entirely — their history stays in the log (entity_history), but
// GetEntity answers not-found. Without a bound, projection memory grows with
// CUMULATIVE churn (every veth interface that ever flapped), not with the
// live graph (#140).
const defaultTombstoneCap = 1024

// defaultTombstoneTTL is the grace window during which a soft-deleted entity's
// identity stays resurrectable: a producer that goes silent (crash, partition,
// a heartbeat slower than its interval) and returns within it keeps its original
// logical id and a continuous history, instead of being minted a fresh ULID
// (#183). The cap still bounds memory; the TTL bounds how stale a resurrection
// may be.
const defaultTombstoneTTL = 15 * time.Minute

// Graph is the in-memory projection of the event log. It is safe for concurrent
// use.
type Graph struct {
	mu sync.RWMutex

	entities  map[model.EntityID]model.Entity
	deleted   map[model.EntityID]bool
	relations map[model.RelationID]model.Relation

	out map[model.EntityID]map[model.RelationID]struct{}
	in  map[model.EntityID]map[model.RelationID]struct{}

	byHash map[string]model.EntityID
	byType map[string]map[model.EntityID]struct{}

	// tombstones is the deletion-order queue backing the bounded tombstone
	// cache; tombstoneCap bounds liveTombstones, the count of queue entries
	// still actually tombstoned (the queue may carry stale ids) (#140).
	tombstones     []model.EntityID
	tombstoneCap   int
	liveTombstones int

	// tombByHash keeps the identity->id mapping of soft-deleted entities so a
	// re-asserted identity resurrects its original logical id (#183). It mirrors
	// byHash but for tombstones; an id leaves it on resurrection or eviction.
	tombByHash map[string]model.EntityID
	// tombDeadline is when each tombstone leaves the resurrection grace window.
	tombDeadline map[model.EntityID]time.Time
	tombstoneTTL time.Duration
	now          func() time.Time
}

// New returns an empty graph with the default tombstone bound.
func New() *Graph {
	return NewWithTombstoneCap(defaultTombstoneCap)
}

// NewWithTombstoneCap returns an empty graph keeping at most limit soft-deleted
// entities readable by id (limit <= 0 means the default).
func NewWithTombstoneCap(limit int) *Graph {
	if limit <= 0 {
		limit = defaultTombstoneCap
	}
	return &Graph{
		entities:     make(map[model.EntityID]model.Entity),
		deleted:      make(map[model.EntityID]bool),
		relations:    make(map[model.RelationID]model.Relation),
		out:          make(map[model.EntityID]map[model.RelationID]struct{}),
		in:           make(map[model.EntityID]map[model.RelationID]struct{}),
		byHash:       make(map[string]model.EntityID),
		byType:       make(map[string]map[model.EntityID]struct{}),
		tombstoneCap: limit,
		tombByHash:   make(map[string]model.EntityID),
		tombDeadline: make(map[model.EntityID]time.Time),
		tombstoneTTL: defaultTombstoneTTL,
		now:          time.Now,
	}
}

// SetClock overrides the clock backing the tombstone grace window (for tests
// and for sharing the engine's clock). A nil clock is ignored.
func (g *Graph) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now = now
}

// SetTombstoneTTL overrides the resurrection grace window. A non-positive ttl
// disables the time bound (tombstones are then retained until the cap evicts
// them).
func (g *Graph) SetTombstoneTTL(ttl time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tombstoneTTL = ttl
}

// TombstoneTTL returns the resurrection grace window in force — the observable
// half of SetTombstoneTTL, so wiring (config → registry → every stack) can be
// verified rather than trusted.
func (g *Graph) TombstoneTTL() time.Duration {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.tombstoneTTL
}

// Replay rebuilds the graph by applying every event from the scanner in order.
func (g *Graph) Replay(s EventScanner) error {
	return s.Scan(func(_ uint64, ev model.Event) error {
		g.Apply(ev)
		return nil
	})
}

// Apply mutates the graph for a single qualified event.
func (g *Graph) Apply(ev model.Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case ev.Entity != nil:
		g.applyEntity(ev.Entity)
	case ev.Relation != nil:
		g.applyRelation(ev.Relation)
	}
}

func (g *Graph) applyEntity(ee *model.EntityEvent) {
	id := ee.Entity.ID
	switch ee.ChangeType {
	case model.EntityCreated:
		g.putEntity(ee.Entity)
	case model.EntityIdentityChanged:
		if old, ok := g.entities[id]; ok {
			delete(g.byHash, old.IdentityHash())
		}
		g.putEntity(ee.Entity)
	case model.EntityAttributeUpdated, model.EntityStateChanged:
		// Identity is unchanged, but route through putEntity anyway (it is
		// idempotent): after retention pruning this can be the entity's first
		// surviving event on replay, and skipping the byHash/byType indexes
		// here would leave the entity unmatchable — the next observation would
		// mint a permanent duplicate (#107).
		g.putEntity(ee.Entity)
	case model.EntityDeleted:
		if e, ok := g.entities[id]; ok {
			// Retain the identity->id mapping as a tombstone so a re-asserted
			// identity resurrects this id rather than minting a new one (#183).
			h := e.IdentityHash()
			delete(g.byHash, h)
			g.tombByHash[h] = id
			if set := g.byType[e.Type]; set != nil {
				delete(set, id)
			}
		}
		if !g.deleted[id] {
			g.deleted[id] = true
			g.tombstones = append(g.tombstones, id)
			g.liveTombstones++
			if g.tombstoneTTL > 0 {
				// Anchor the grace window on the deletion event's recorded time,
				// not the apply-time clock: replay on restart re-applies the
				// event tail before the engine's clock is wired (registry
				// openStack replays, then change.New calls SetClock), so g.now()
				// would be boot time and revive the window for entities deleted
				// long before the restart. RecordedAt is absolute and survives
				// replay/snapshot; a window that elapsed during downtime then
				// arrives already expired.
				base := ee.RecordedAt
				if base.IsZero() && g.now != nil {
					base = g.now()
				}
				if !base.IsZero() {
					g.tombDeadline[id] = base.Add(g.tombstoneTTL)
				}
			}
			g.evictTombstones()
		}
	case model.EntityUnchanged:
		// heartbeat: ensure presence, no state change
		if _, ok := g.entities[id]; !ok {
			g.putEntity(ee.Entity)
		}
	}
}

// evictTombstones drops the oldest soft-deleted entities beyond the cap,
// amortized O(1) per delete. Entries whose entity was resurrected (replay
// ensure-presence) are skipped — the queue may carry stale ids, the maps and
// the live counter are the truth.
func (g *Graph) evictTombstones() {
	for g.liveTombstones > g.tombstoneCap && len(g.tombstones) > 0 {
		id := g.tombstones[0]
		g.tombstones = g.tombstones[1:]
		if !g.deleted[id] {
			continue // stale entry; not a tombstone anymore
		}
		g.dropTombstone(id)
	}
}

// dropTombstone evicts one soft-deleted entity entirely: it stops being
// readable by id and is no longer resurrectable by identity. The caller must
// hold the write lock and have confirmed g.deleted[id].
func (g *Graph) dropTombstone(id model.EntityID) {
	if e, ok := g.entities[id]; ok {
		h := e.IdentityHash()
		if g.tombByHash[h] == id { // a newer incarnation may already own the hash
			delete(g.tombByHash, h)
		}
	}
	delete(g.entities, id)
	delete(g.deleted, id)
	delete(g.tombDeadline, id)
	g.liveTombstones--
}

// PruneTombstones drops soft-deleted entities whose grace window has elapsed,
// so the resurrection window is bounded in time, not only in count. Returns the
// number pruned. Drive it from the same ticker as the engine's Sweep.
func (g *Graph) PruneTombstones() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.now == nil || g.tombstoneTTL <= 0 {
		return 0
	}
	now := g.now()
	n := 0
	for id, deadline := range g.tombDeadline {
		if !now.After(deadline) {
			continue
		}
		if g.deleted[id] {
			g.dropTombstone(id) // leaves a stale entry in the tombstones queue; evict skips it
			n++
			continue
		}
		delete(g.tombDeadline, id) // resurrected since; deadline is moot
	}
	// The cap path (evictTombstones) is the only consumer that pops the queue,
	// and under the default TTL window liveTombstones stays far below the cap, so
	// the queue is never compacted there. Drop the stale entries left by TTL
	// pruning and resurrection here, otherwise the backing slice grows without
	// bound under steady delete churn — the cumulative growth #140 bounds for
	// the cache must hold for the queue too.
	if len(g.tombstones) > 2*g.liveTombstones {
		g.compactTombstones()
	}
	return n
}

// compactTombstones rewrites the deletion-order queue in place, keeping only ids
// still soft-deleted (one entry each, oldest position wins). The caller must hold
// the write lock.
func (g *Graph) compactTombstones() {
	kept := g.tombstones[:0]
	seen := make(map[model.EntityID]struct{}, g.liveTombstones)
	for _, id := range g.tombstones {
		if !g.deleted[id] {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		kept = append(kept, id)
	}
	g.tombstones = kept
}

func (g *Graph) putEntity(e model.Entity) {
	g.entities[e.ID] = e
	h := e.IdentityHash()
	if g.deleted[e.ID] {
		g.liveTombstones-- // resurrection un-tombstones; its queue entry goes stale
		delete(g.tombDeadline, e.ID)
	}
	delete(g.deleted, e.ID)
	delete(g.tombByHash, h) // identity is live again (or owned by this id now)
	g.byHash[h] = e.ID
	set := g.byType[e.Type]
	if set == nil {
		set = make(map[model.EntityID]struct{})
		g.byType[e.Type] = set
	}
	set[e.ID] = struct{}{}
}

func (g *Graph) applyRelation(re *model.RelationEvent) {
	r := re.Relation
	switch re.ChangeType {
	case model.RelationAdded:
		g.relations[r.ID] = r
		g.addAdjacency(r)
	case model.RelationAttributeChanged:
		// Same replay-after-pruning shape as for entities: this can be the
		// relation's first surviving event, so (re-)index the adjacency too;
		// addAdjacency is idempotent (#107).
		g.relations[r.ID] = r
		g.addAdjacency(r)
	case model.RelationRemoved:
		delete(g.relations, r.ID)
		if s := g.out[r.From]; s != nil {
			delete(s, r.ID)
		}
		if s := g.in[r.To]; s != nil {
			delete(s, r.ID)
		}
	}
}

func (g *Graph) addAdjacency(r model.Relation) {
	if g.out[r.From] == nil {
		g.out[r.From] = make(map[model.RelationID]struct{})
	}
	g.out[r.From][r.ID] = struct{}{}
	if g.in[r.To] == nil {
		g.in[r.To] = make(map[model.RelationID]struct{})
	}
	g.in[r.To][r.ID] = struct{}{}
}

// GetEntity returns the entity, whether it exists, and whether it is soft-deleted.
func (g *Graph) GetEntity(id model.EntityID) (model.Entity, bool, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	e, ok := g.entities[id]
	return e, ok, g.deleted[id]
}

// GetRelation returns the relation if present.
func (g *Graph) GetRelation(id model.RelationID) (model.Relation, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	r, ok := g.relations[id]
	return r, ok
}

// EntityCount returns the number of live (non-deleted) entities. It counts live
// entities by membership rather than len(entities)-len(deleted): a delete whose
// create aged out of retention leaves a phantom tombstone (an id in deleted but
// never in entities), and the subtraction would undercount — even go negative
// when phantoms outnumber live entities, which a clamp alone would not fix.
func (g *Graph) EntityCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := 0
	for id := range g.entities {
		if !g.deleted[id] {
			n++
		}
	}
	return n
}

// RelationCount returns the number of relations.
func (g *Graph) RelationCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.relations)
}

// CountByType returns the count of live entities per type.
func (g *Graph) CountByType() map[string]int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]int)
	for t, set := range g.byType {
		n := 0
		for id := range set {
			if !g.deleted[id] {
				n++
			}
		}
		if n > 0 {
			out[t] = n
		}
	}
	return out
}

// Neighbors returns the live entities reachable from id by traversing relations
// (in either direction) up to depth hops. relType filters by relation type when
// non-empty. The starting entity is not included.
func (g *Graph) Neighbors(id model.EntityID, relType string, depth int) []model.Entity {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if depth < 1 {
		return nil
	}
	visited := map[model.EntityID]struct{}{id: {}}
	frontier := []model.EntityID{id}
	var result []model.Entity
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []model.EntityID
		for _, cur := range frontier {
			for rid := range g.out[cur] {
				g.visitNeighbor(g.relations[rid].To, relType, g.relations[rid].Type, visited, &next, &result)
			}
			for rid := range g.in[cur] {
				g.visitNeighbor(g.relations[rid].From, relType, g.relations[rid].Type, visited, &next, &result)
			}
		}
		frontier = next
	}
	return result
}

func (g *Graph) visitNeighbor(nb model.EntityID, relType, edgeType string, visited map[model.EntityID]struct{}, next *[]model.EntityID, result *[]model.Entity) {
	if relType != "" && edgeType != relType {
		return
	}
	if _, seen := visited[nb]; seen {
		return
	}
	visited[nb] = struct{}{}
	*next = append(*next, nb)
	if e, ok := g.entities[nb]; ok && !g.deleted[nb] {
		*result = append(*result, e)
	}
}

// ListEntities returns the live (non-deleted) entities, optionally filtered by
// type, sorted by logical id for stable pagination.
func (g *Graph) ListEntities(typ string) []model.Entity {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]model.Entity, 0, len(g.entities))
	for id, e := range g.entities {
		if g.deleted[id] {
			continue
		}
		if typ != "" && e.Type != typ {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListRelations returns relations filtered by type and/or endpoints (empty
// arguments match any), sorted by id for stable pagination.
func (g *Graph) ListRelations(typ string, from, to model.EntityID) []model.Relation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]model.Relation, 0, len(g.relations))
	for _, r := range g.relations {
		if typ != "" && r.Type != typ {
			continue
		}
		if from != "" && r.From != from {
			continue
		}
		if to != "" && r.To != to {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RelationsTouching returns every relation incident to id (as From or To),
// optionally filtered by type, using the out/in adjacency index — O(degree of id)
// rather than the full O(all relations) scan two ListRelations calls would do.
// Order is by relation id, like ListRelations; a self-loop appears once.
func (g *Graph) RelationsTouching(id model.EntityID, relType string) []model.Relation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	seen := make(map[model.RelationID]struct{})
	var out []model.Relation
	add := func(rid model.RelationID) {
		if _, dup := seen[rid]; dup {
			return
		}
		r, ok := g.relations[rid]
		if !ok || (relType != "" && r.Type != relType) {
			return
		}
		seen[rid] = struct{}{}
		out = append(out, r)
	}
	for rid := range g.out[id] {
		add(rid)
	}
	for rid := range g.in[id] {
		add(rid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MatchIdentity finds the logical entity ID for an observed identity by an
// exact identity-hash match against a live entity of the same type. Identity is
// immutable (ADR 0018, superseding ADR 0017): an observation whose identity does
// not match exactly is a different entity, never a tolerant/fuzzy match.
func (g *Graph) MatchIdentity(typ string, identity []model.KeyValue) (model.EntityID, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	hash := model.Entity{Type: typ, Identity: identity}.IdentityHash()
	if id, ok := g.byHash[hash]; ok && !g.deleted[id] {
		return id, true
	}
	return "", false
}

// MatchTombstone finds the logical id of a soft-deleted entity whose identity
// matches exactly and whose resurrection grace window has not elapsed (#183).
// The engine uses it, after MatchIdentity misses, to resurrect an entity's
// original id instead of minting a new one — keeping entity_history continuous
// across a producer outage. Identity matching is exact, as for live entities.
func (g *Graph) MatchTombstone(typ string, identity []model.KeyValue) (model.EntityID, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	hash := model.Entity{Type: typ, Identity: identity}.IdentityHash()
	id, ok := g.tombByHash[hash]
	if !ok || !g.deleted[id] {
		return "", false
	}
	if g.now != nil && g.tombstoneTTL > 0 {
		if deadline, ok := g.tombDeadline[id]; ok && g.now().After(deadline) {
			return "", false // past the grace window: treat as genuinely gone
		}
	}
	return id, true
}

// SnapshotEvents returns synthetic create/add events that, applied in order to a
// fresh graph, reconstruct the current live state: every live entity as an
// EntityCreated, then every relation as a RelationAdded. Soft-deleted entities are
// omitted (they are not part of the live graph). The given time stamps the events;
// it is immaterial to reconstruction (Apply ignores event times). Used to write a
// projection snapshot for fast restart (#49).
func (g *Graph) SnapshotEvents(when time.Time) []model.Event {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]model.Event, 0, len(g.entities)+len(g.relations))
	for id, e := range g.entities {
		if g.deleted[id] {
			// A soft-deleted entity must not be written to the snapshot: its
			// EntityDeleted predates the snapshot sequence and is never
			// replayed, so emitting it here resurrects it on restore (#106).
			continue
		}
		out = append(out, model.Event{Entity: &model.EntityEvent{
			EventID:       model.NewEventID(),
			ChangeType:    model.EntityCreated,
			Entity:        e,
			EventTime:     when,
			RecordedAt:    when,
			SchemaVersion: model.SchemaVersion,
		}})
	}
	for _, r := range g.relations {
		out = append(out, model.Event{Relation: &model.RelationEvent{
			EventID:       model.NewEventID(),
			ChangeType:    model.RelationAdded,
			Relation:      r,
			EventTime:     when,
			RecordedAt:    when,
			SchemaVersion: model.SchemaVersion,
		}})
	}
	return out
}
