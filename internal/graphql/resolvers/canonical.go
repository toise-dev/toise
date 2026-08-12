package resolvers

import (
	"context"
	"sort"
	"strings"

	"github.com/toise-dev/toise/internal/canonical"
	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
)

// Canonical resolves the read-time identity overlay of ADR 0020 for one entity:
// the aliases high-confidence same_as edges assert are the same real thing, plus
// the edges justifying it. Null when the entity is unknown or stands alone.
//
// The walk itself lives in internal/canonical, shared with the MCP surface. A
// second spelling of it is how two read surfaces come to disagree about what the
// graph says, which is the one failure this overlay cannot afford: the whole
// point is that a consumer asking "is this the same machine?" gets one answer.
func (r *queryResolver) Canonical(ctx context.Context, id string, asOf *string) (*generated.CanonicalGroup, error) {
	g, err := r.graphAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	root := model.EntityID(id)
	if _, ok, _ := g.GetEntity(root); !ok {
		return nil, nil
	}
	members, links := canonical.Walk(g, root, r.identityThreshold())
	if len(members) <= 1 {
		return nil, nil
	}

	aliases := make([]generated.CanonicalMember, 0, len(members)-1)
	for _, mid := range members {
		if mid == root {
			continue
		}
		m := generated.CanonicalMember{ID: string(mid)}
		// A deleted alias keeps its id and loses its rendering: the belief edge is
		// still evidence the thing existed, but presenting a soft-deleted entity as
		// a live alias would misstate the graph.
		if ent, ok, deleted := g.GetEntity(mid); ok && !deleted {
			m.Type = ent.Type
			m.Label = entityLabel(ent)
		}
		aliases = append(aliases, m)
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].ID < aliases[j].ID })

	out := make([]generated.SameAsLink, len(links))
	for i, l := range links {
		out[i] = generated.SameAsLink{From: l.From, To: l.To, Confidence: l.Confidence, Basis: l.Basis}
	}
	return &generated.CanonicalGroup{Aliases: aliases, Links: out}, nil
}

// entityLabel renders an entity as "type key=value …" over its identifying
// attributes — enough for a human or a model to tell two aliases apart without
// a second round trip.
func entityLabel(e model.Entity) string {
	var b strings.Builder
	b.WriteString(e.Type)
	for _, kv := range e.Identity {
		b.WriteByte(' ')
		b.WriteString(kv.Key)
		b.WriteByte('=')
		b.WriteString(kv.Value.Display())
	}
	return b.String()
}
