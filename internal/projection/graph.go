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
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		entities:  make(map[model.EntityID]model.Entity),
		deleted:   make(map[model.EntityID]bool),
		relations: make(map[model.RelationID]model.Relation),
		out:       make(map[model.EntityID]map[model.RelationID]struct{}),
		in:        make(map[model.EntityID]map[model.RelationID]struct{}),
		byHash:    make(map[string]model.EntityID),
		byType:    make(map[string]map[model.EntityID]struct{}),
	}
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
			delete(g.byHash, e.IdentityHash())
			if set := g.byType[e.Type]; set != nil {
				delete(set, id)
			}
		}
		g.deleted[id] = true
	case model.EntityUnchanged:
		// heartbeat: ensure presence, no state change
		if _, ok := g.entities[id]; !ok {
			g.putEntity(ee.Entity)
		}
	}
}

func (g *Graph) putEntity(e model.Entity) {
	g.entities[e.ID] = e
	delete(g.deleted, e.ID)
	g.byHash[e.IdentityHash()] = e.ID
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

// EntityCount returns the number of live (non-deleted) entities.
func (g *Graph) EntityCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.entities) - len(g.deleted)
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
