package projection

import (
	"sort"
	"sync"

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
		// Identity is unchanged; replace the stored entity with new attributes.
		g.entities[id] = ee.Entity
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
		g.relations[r.ID] = r
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

// MatchIdentity finds the logical entity ID for an observed identity. It returns
// (id, exact, found): an exact hash match (exact=true), or a tolerant match
// (exact=false) where the identity differs from a live entity of the same type
// in at most maxDiff identifying values (same key set). See ADR 0017.
func (g *Graph) MatchIdentity(typ string, identity []model.KeyValue, maxDiff int) (model.EntityID, bool, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	hash := model.Entity{Type: typ, Identity: identity}.IdentityHash()
	if id, ok := g.byHash[hash]; ok && !g.deleted[id] {
		return id, true, true
	}
	if maxDiff <= 0 {
		return "", false, false
	}
	for id := range g.byType[typ] {
		if g.deleted[id] {
			continue
		}
		same, diffs := identityDiff(g.entities[id].Identity, identity)
		// Require at least one unchanged identifying value as an anchor: a
		// single-key identity has no anchor, so any different value is a new
		// entity, not an identity change.
		if same && diffs >= 1 && diffs <= maxDiff && diffs < len(identity) {
			return id, false, true
		}
	}
	return "", false, false
}

// identityDiff reports whether two identity sets share the same keys and, if so,
// how many values differ.
func identityDiff(a, b []model.KeyValue) (sameKeys bool, valueDiffs int) {
	if len(a) != len(b) {
		return false, 0
	}
	am := make(map[string]string, len(a))
	for _, kv := range a {
		am[kv.Key] = kv.Value.String()
	}
	for _, kv := range b {
		v, ok := am[kv.Key]
		if !ok {
			return false, 0
		}
		if v != kv.Value.String() {
			valueDiffs++
		}
	}
	return true, valueDiffs
}
