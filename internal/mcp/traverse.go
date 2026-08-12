package mcp

import (
	"context"
	"fmt"

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
	FromID       string `json:"from_id" jsonschema:"logical id of the start entity"`
	ToID         string `json:"to_id" jsonschema:"logical id of the destination entity"`
	RelationType string `json:"relation_type,omitempty" jsonschema:"only traverse relations of this type (omit to traverse any)"`
	MaxDepth     int    `json:"max_depth,omitempty" jsonschema:"maximum hops to explore, 1 to 10 (default 10)"`
	AsOf         string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: search the graph as it was then (event-time), instead of now"`
}

// PathHop is one entity along the path with the edge that reached it.
type PathHop struct {
	Entity      Entity `json:"entity"`
	ViaRelation string `json:"via_relation,omitempty" jsonschema:"relation type of the edge that reached this hop (empty on the start entity)"`
	Direction   string `json:"direction,omitempty" jsonschema:"outgoing if the edge points from the previous hop to this one, incoming otherwise"`
}

// FindPathOutput carries the shortest path, or a first-class 'not reachable'.
type FindPathOutput struct {
	Reachable bool      `json:"reachable" jsonschema:"false is a real answer: no path within max_depth (NOT an error)"`
	Hops      int       `json:"hops" jsonschema:"number of edges on the path (0 when from and to are the same entity)"`
	MaxDepth  int       `json:"max_depth" jsonschema:"the hop cap that was applied; raise it if reachable=false looks wrong"`
	Path      []PathHop `json:"path,omitempty" jsonschema:"the entities along the shortest path, start first"`
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
	from, to := model.EntityID(in.FromID), model.EntityID(in.ToID)
	for _, id := range []model.EntityID{from, to} {
		if _, ok, deleted := g.GetEntity(id); !ok || deleted {
			return nil, FindPathOutput{}, fmt.Errorf("no live entity with id %q; use find_entities to discover ids", id)
		}
	}

	out := FindPathOutput{MaxDepth: depth}
	type cameFrom struct {
		prev model.EntityID
		via  edge
	}
	parents := map[model.EntityID]cameFrom{from: {}}
	frontier := []model.EntityID{from}
	found := from == to
	for d := 0; d < depth && len(frontier) > 0 && !found; d++ {
		var next []model.EntityID
		for _, cur := range frontier {
			edges := edgesOf(g, cur, in.RelationType)
			for i := range edges {
				e := &edges[i]
				// Alias-aware (ADR 0020 Lot B): a same_as belief connects a path only
				// at/above the confidence threshold — a weak alias must not create a
				// spurious route. A strong one does, and shows as a same_as hop.
				if e.rel.Type == model.RelSameAs {
					if c, ok := canonical.Confidence(e.rel); !ok || c < s.idThr {
						continue
					}
				}
				if _, seen := parents[e.other]; seen {
					continue
				}
				parents[e.other] = cameFrom{prev: cur, via: *e}
				if e.other == to {
					found = true
					break
				}
				next = append(next, e.other)
			}
			if found {
				break
			}
		}
		frontier = next
	}
	if !found {
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
	return nil, out, nil
}
