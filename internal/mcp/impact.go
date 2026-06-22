package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
)

// --- impact_of ---

// ImpactOfInput names the failed (or hypothetically failing) entity.
type ImpactOfInput struct {
	EntityID string `json:"entity_id" jsonschema:"the entity whose failure to propagate"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"how many propagation hops to follow, 1 to 10 (default 10)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum impacted entities to return (default 50, max 200); totals always cover everything"`
	AsOf     string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: propagate through the graph as it was then (event-time), instead of now"`
}

// ImpactedEntity is one entity the failure reaches, with how it was reached.
type ImpactedEntity struct {
	Entity
	Depth int    `json:"depth" jsonschema:"propagation hops from the failed entity"`
	Via   string `json:"via" jsonschema:"relation type of the edge that propagated the impact to this entity"`
}

// ImpactOfOutput is the blast radius, nearest first.
type ImpactOfOutput struct {
	Root      Entity           `json:"root" jsonschema:"the failed entity the propagation started from"`
	Impacted  []ImpactedEntity `json:"impacted"`
	Total     int              `json:"total" jsonschema:"impacted entities before the limit was applied"`
	Truncated bool             `json:"truncated"`
	ByType    []TypeCount      `json:"by_type,omitempty" jsonschema:"impacted entities per type, before the limit"`
	Aliases   int              `json:"aliases,omitempty" jsonschema:"count of high-confidence same_as aliases of the root folded into the origin (ADR 0020); the blast radius is computed for the whole canonical group"`
	Summary   string           `json:"summary" jsonschema:"a one-line natural-language summary of the blast radius"`
}

func (s *Server) impactOf(ctx context.Context, _ *mcpsdk.CallToolRequest, in ImpactOfInput) (*mcpsdk.CallToolResult, ImpactOfOutput, error) {
	if in.EntityID == "" {
		return nil, ImpactOfOutput{}, fmt.Errorf("an entity_id is required")
	}
	depth := in.MaxDepth
	if depth <= 0 || depth > maxPathDepth {
		depth = maxPathDepth
	}
	limit := clampLimit(in.Limit)
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, ImpactOfOutput{}, err
	}
	rootID := model.EntityID(in.EntityID)
	root, ok, deleted := g.GetEntity(rootID)
	if !ok || deleted {
		return nil, ImpactOfOutput{}, fmt.Errorf("no live entity with id %q; use find_entities to discover ids", in.EntityID)
	}

	out := ImpactOfOutput{Root: entityOut(root, false)}
	// Alias-aware (ADR 0020 Lot B): the root's canonical group is one logical node,
	// so seed the traversal with every high-confidence same_as alias. Querying any
	// facet of a machine then yields the same blast radius, and the aliases
	// themselves are origins, not downstream impact.
	group := s.canonicalMemberIDs(g, rootID)
	visited := make(map[model.EntityID]struct{}, len(group))
	for _, id := range group {
		visited[id] = struct{}{}
	}
	frontier := append([]model.EntityID(nil), group...)
	out.Aliases = len(group) - 1
	var impacted []ImpactedEntity
	counts := map[string]int{}
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		var next []model.EntityID
		for _, cur := range frontier {
			edges := edgesOf(g, cur, "")
			for i := range edges {
				e := &edges[i]
				if !impactPropagates(e, cur) {
					continue
				}
				if _, seen := visited[e.other]; seen {
					continue
				}
				visited[e.other] = struct{}{}
				next = append(next, e.other)
				ent, _, _ := g.GetEntity(e.other)
				impacted = append(impacted, ImpactedEntity{
					Entity: entityOut(ent, false), Depth: d, Via: e.rel.Type,
				})
				counts[ent.Type]++
			}
		}
		frontier = next
	}

	sortImpacted(impacted)
	out.Total = len(impacted)
	out.ByType = sortedCounts(counts)
	if len(impacted) > limit {
		impacted = impacted[:limit]
		out.Truncated = true
	}
	out.Impacted = impacted
	out.Summary = impactSummary(root, out)
	return nil, out, nil
}

// impactPropagates says whether a failure at the entity `failed` crosses edge e
// to reach e.other, per the relation type's registered flow direction:
// dependency edges (runs_on) carry impact from To to From, containment edges
// (has_interface) from From to To, connectivity both ways (#136).
func impactPropagates(e *edge, failed model.EntityID) bool {
	switch model.ImpactFlowOf(e.rel.Type) {
	case model.ImpactToFrom:
		return e.rel.To == failed
	case model.ImpactFromTo:
		return e.rel.From == failed
	case model.ImpactNone:
		return false // identity belief (same_as), not a failure path
	default: // ImpactBoth
		return true
	}
}

func impactSummary(root model.Entity, out ImpactOfOutput) string {
	if out.Total == 0 {
		return fmt.Sprintf("Nothing depends on %s: its failure propagates to no other entity within the explored depth.", label(root))
	}
	parts := make([]string, 0, len(out.ByType))
	for _, tc := range out.ByType {
		parts = append(parts, fmt.Sprintf("%d %s", tc.Count, tc.Type))
	}
	return fmt.Sprintf("A failure of %s impacts %d entities: %s.", label(root), out.Total, strings.Join(parts, ", "))
}

// sortImpacted keeps the output deterministic: nearest first, then label.
func sortImpacted(impacted []ImpactedEntity) {
	sort.SliceStable(impacted, func(i, j int) bool {
		if impacted[i].Depth != impacted[j].Depth {
			return impacted[i].Depth < impacted[j].Depth
		}
		return impacted[i].Label < impacted[j].Label
	})
}
