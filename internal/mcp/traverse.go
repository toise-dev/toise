package mcp

import (
	"context"
	"fmt"
	"sort"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/canonical"
	"github.com/toise-dev/toise/internal/model"
)

// maxPathDepth caps find_path traversal: longer paths exist but stop being
// explainable, and BFS cost grows with the frontier. Unreachable within the cap
// is a first-class answer, not an error.
const maxPathDepth = 10

// edge is one traversable step from an entity, in either direction.
type edge struct {
	rel       model.Relation
	other     model.EntityID
	direction string // "outgoing" when the edge leaves the current entity
}

// edgesOf lists the traversable edges of id on g, deterministically ordered,
// with deleted endpoints already excluded. Taking the graph as a parameter
// lets every traversal run against an as-of fold as well as the live
// projection (#135).
func edgesOf(g Graph, id model.EntityID, relType string) []edge {
	// One indexed lookup over id's incident edges (sorted by rel id) instead of
	// two full ListRelations scans of every relation in the graph.
	rels := g.RelationsTouching(id, relType)
	out := make([]edge, 0, len(rels))
	for i := range rels {
		r := rels[i]
		other, direction := r.To, "outgoing"
		if r.To == id && r.From != id { // incoming; a self-loop stays outgoing
			other, direction = r.From, "incoming"
		}
		if _, ok, deleted := g.GetEntity(other); !ok || deleted {
			continue
		}
		out = append(out, edge{rel: r, other: other, direction: direction})
	}
	return out
}

// --- find_path ---

// FindPathInput names the two endpoints.
type FindPathInput struct {
	FromID       string `json:"from_id" jsonschema:"the start entity, by identity_fingerprint (preferred), by identity written inline as type:key=value, or by id"`
	ToID         string `json:"to_id" jsonschema:"the destination entity, by identity_fingerprint (preferred), by identity written inline as type:key=value, or by id"`
	RelationType string `json:"relation_type,omitempty" jsonschema:"only traverse relations of this type (omit to traverse any)"`
	MaxDepth     int    `json:"max_depth,omitempty" jsonschema:"maximum hops to explore, 1 to 10 (default 10)"`
	AsOf         string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: search the graph as it was then (event-time), instead of now — COSTLY: this is a full REPLAY of the event log from the origin into a fresh graph, not a filter. Its cost tracks the size of this tenant's log, NOT how far back you asked: two minutes costs the same as twenty-four hours. Folds are bounded process-wide (NumCPU/2 at once) across every tenant and surface, so several queue behind each other and behind other readers. One per displayed row will take an instance down. A live read is in memory and entity_history is a linear range read: neither is in this class"`
}

// PathHop is one entity along the path with the edge that reached it.
type PathHop struct {
	Entity      Entity `json:"entity"`
	ViaRelation string `json:"via_relation,omitempty" jsonschema:"relation type of the edge that reached this hop (empty on the start entity)"`
	Direction   string `json:"direction,omitempty" jsonschema:"outgoing if the edge points from the previous hop to this one, incoming otherwise"`
}

// FindPathOutput carries the shortest path, or a first-class 'not reachable'.
type FindPathOutput struct {
	Graph     GraphMeta `json:"graph" jsonschema:"what the answering graph holds and how fresh it is; read this before treating absence as fact"`
	Reachable bool      `json:"reachable" jsonschema:"false is a real answer: no path within max_depth (NOT an error)"`
	Hops      int       `json:"hops" jsonschema:"number of edges on the path (0 when from and to are the same entity)"`
	MaxDepth  int       `json:"max_depth" jsonschema:"the hop cap that was applied; raise it if reachable=false looks wrong"`
	Path      []PathHop `json:"path,omitempty" jsonschema:"the entities along the shortest path, start first"`

	Convergences     []Entity `json:"convergences,omitempty" jsonschema:"terminal entities (an observed network.endpoint) that BOTH ends depend on. They were reached from both sides and deliberately not crossed: dialing the same destination is not evidence that the two ends communicate. Read convergence_note before concluding anything from them"`
	ConvergenceTotal int      `json:"convergence_total,omitempty" jsonschema:"how many shared terminal entities were found, when convergences lists fewer"`
	ConvergenceNote  string   `json:"convergence_note,omitempty" jsonschema:"what the convergences mean and what they do not mean, in words"`
}

// maxConvergences caps how many shared terminals are listed; the total is
// reported separately so a long list is never silently cut.
const maxConvergences = 10

// cameFrom records the edge a BFS arrived by, for path reconstruction.
type cameFrom struct {
	prev model.EntityID
	via  edge
}

// pathWalk is the result of one bounded BFS.
type pathWalk struct {
	parents   map[model.EntityID]cameFrom
	terminals map[model.EntityID]struct{}
	found     bool
}

// walkPaths explores g from start, breadth-first, up to depth hops, stopping
// early if goal is reached.
//
// A terminal entity (ADR 0032: an observed endpoint is a destination, not a
// waypoint) may be reached, and may be the start, but may only be LEFT by an
// edge it is itself the source of. Walking back down a dependency that arrived
// at it is refused: that shape can only ever mean two dependents dial the same
// destination, and rendering it as a hop reports a convergence as a route
// (#442). The test is the edge direction, not the type, so the address join
// ADR 0032 section 3 still owes us (#443) will traverse the day it exists.
// Every refusal is recorded, so the caller can be given the convergence
// instead of a bare "not reachable".
func (s *Server) walkPaths(g Graph, start, goal model.EntityID, relType string, depth int) pathWalk {
	w := pathWalk{
		parents:   map[model.EntityID]cameFrom{start: {}},
		terminals: map[model.EntityID]struct{}{},
		found:     start == goal,
	}
	frontier := []model.EntityID{start}
	for d := 0; d < depth && len(frontier) > 0 && !w.found; d++ {
		var next []model.EntityID
		for _, cur := range frontier {
			terminal := false
			// arrivedBy is the relation type this BFS reached cur through, empty at
			// the start. Some types may be crossed only by CHANGING relation type,
			// which is what separates a real crossing from a convergence wearing the
			// same shape — see model.CrossingNeedsADifferentRelation.
			arrivedBy := ""
			if cur != start {
				if ent, ok, _ := g.GetEntity(cur); ok {
					if model.IsTerminalEntityType(ent.Type) {
						terminal = true
					}
					if model.CrossingNeedsADifferentRelation(ent.Type) {
						arrivedBy = w.parents[cur].via.rel.Type
					}
				}
			}
			edges := edgesOf(g, cur, relType)
			for i := range edges {
				e := &edges[i]
				if terminal && e.direction != "outgoing" {
					w.terminals[cur] = struct{}{}
					continue
				}
				// Leaving by the type we arrived by is a convergence, not a path: one
				// MAC heard on two interfaces of a host does not link them, and
				// traffic does not cross from one bond member to another. Recorded as
				// a terminal so the caller is given the convergence rather than a bare
				// "not reachable", exactly as for an endpoint.
				if arrivedBy != "" && e.rel.Type == arrivedBy {
					w.terminals[cur] = struct{}{}
					continue
				}
				// Alias-aware (ADR 0020 Lot B): a same_as belief connects a path only
				// at/above the confidence threshold — a weak alias must not create a
				// spurious route. A strong one does, and shows as a same_as hop.
				if e.rel.Type == model.RelSameAs {
					if c, ok := canonical.Confidence(e.rel); !ok || c < s.idThr {
						continue
					}
				}
				if _, seen := w.parents[e.other]; seen {
					continue
				}
				w.parents[e.other] = cameFrom{prev: cur, via: *e}
				if e.other == goal {
					w.found = true
					break
				}
				next = append(next, e.other)
			}
			if w.found {
				break
			}
		}
		frontier = next
	}
	return w
}

func (s *Server) findPath(ctx context.Context, _ *mcpsdk.CallToolRequest, in FindPathInput) (*mcpsdk.CallToolResult, FindPathOutput, error) {
	if in.FromID == "" || in.ToID == "" {
		return nil, FindPathOutput{}, fmt.Errorf("both from_id and to_id are required")
	}
	depth := in.MaxDepth
	if depth <= 0 || depth > maxPathDepth {
		depth = maxPathDepth
	}
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, FindPathOutput{}, err
	}
	from, fromOK := g.ResolveHandle(in.FromID)
	to, toOK := g.ResolveHandle(in.ToID)
	for _, h := range []struct {
		id     model.EntityID
		handle string
		ok     bool
	}{{from, in.FromID, fromOK}, {to, in.ToID, toOK}} {
		if !h.ok {
			return nil, FindPathOutput{}, fmt.Errorf("no live entity for handle %q; use find_entities to discover entities", h.handle)
		}
		if _, ok, deleted := g.GetEntity(h.id); !ok || deleted {
			return nil, FindPathOutput{}, fmt.Errorf("no live entity for handle %q; use find_entities to discover entities", h.handle)
		}
	}

	out := FindPathOutput{MaxDepth: depth}
	w := s.walkPaths(g, from, to, in.RelationType, depth)
	parents, found := w.parents, w.found
	if !found && len(w.terminals) > 0 {
		// The two ends may still share a destination. Say which one, rather than
		// leaving the caller with a bare denial: the shared endpoint IS the finding,
		// and it is what a false path was built out of before (#442).
		back := s.walkPaths(g, to, from, in.RelationType, depth)
		shared := make([]model.EntityID, 0, len(w.terminals))
		for id := range w.terminals {
			if _, ok := back.terminals[id]; ok {
				shared = append(shared, id)
			}
		}
		if len(shared) > 0 {
			out.Convergences, out.ConvergenceTotal = convergenceEntities(g, shared)
			out.ConvergenceNote = convergenceNote(out.ConvergenceTotal)
		}
	}
	if !found {
		out.Graph = s.graphMeta(g, in.AsOf)
		return nil, out, nil // reachable: false is the answer, not an error
	}

	var hops []PathHop
	for cur := to; ; {
		p := parents[cur]
		ent, _, _ := g.GetEntity(cur)
		hop := PathHop{Entity: entityOut(ent, false)}
		if cur != from {
			hop.ViaRelation = p.via.rel.Type
			hop.Direction = p.via.direction
		}
		hops = append(hops, hop)
		if cur == from {
			break
		}
		cur = p.prev
	}
	for i, j := 0, len(hops)-1; i < j; i, j = i+1, j-1 {
		hops[i], hops[j] = hops[j], hops[i]
	}
	out.Reachable = true
	out.Path = hops
	out.Hops = len(hops) - 1
	out.Graph = s.graphMeta(g, in.AsOf)
	return nil, out, nil
}

// convergenceEntities renders the shared terminals, deterministically ordered
// and capped, with the true total.
func convergenceEntities(g Graph, ids []model.EntityID) (listed []Entity, total int) {
	listed = make([]Entity, 0, len(ids))
	for _, id := range ids {
		ent, ok, deleted := g.GetEntity(id)
		if !ok || deleted {
			continue
		}
		listed = append(listed, entityOut(ent, false))
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].IdentityFingerprint < listed[j].IdentityFingerprint })
	total = len(listed)
	if total > maxConvergences {
		listed = listed[:maxConvergences]
	}
	return listed, total
}

// convergenceNote states what a convergence means and what it does not, in a
// sentence: a flag nobody reads is a flag ignored.
func convergenceNote(n int) string {
	subject := "this destination"
	if n > 1 {
		subject = "these destinations"
	}
	return fmt.Sprintf("no path within max_depth, but both ends depend on %d shared terminal %s. "+
		"They were reached from both sides and NOT crossed: dialing %s is not evidence that the two ends "+
		"communicate with each other, and treating it as a hop is how a convergence gets reported as a route. "+
		"Call get_neighbors on one to see who else dials it.",
		n, plural("entity", "entities", n), subject)
}

func plural(one, many string, n int) string {
	if n == 1 {
		return one
	}
	return many
}
