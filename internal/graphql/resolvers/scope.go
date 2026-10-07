package resolvers

import (
	"context"
	"fmt"
	"time"

	"github.com/toise-dev/toise/internal/projection"

	"github.com/toise-dev/toise/internal/graphql/generated"
)

// GraphScope answers what this answer was built from: the tenant served, the
// size of the graph, its freshness and its retention horizon.
//
// The tenant is the point (#399). In derive-only tenancy the tenant comes from
// the credential and a client-supplied X-Scope-OrgID is ignored. The isolation
// is exactly right — that credential can never reach another tenant — but a
// client asking for tenant 12 was answered from tenant 10 with a 200 and had no
// way to learn its header had been dropped. It cost someone a wrong diagnosis:
// a probe went red on a correct server.
//
// Naming it turns a check that required a trick — send a header for a tenant you
// cannot reach, compare counts — into reading one field.
//
// Freshness is read from the journal — when a producer last spoke — not from
// when a projection was rebuilt. A restarted replica has a brand-new projection
// over an old graph, so reporting the rebuild would claim freshness it does not
// have.
//
// Under asOf the counts come from the folded graph rather than the live one, as
// the MCP block does: an answer about a past instant whose scope describes the
// present is the mute source this issue is about, wearing a provenance block.
func (r *queryResolver) GraphScope(ctx context.Context, asOf *string) (*generated.GraphScope, error) {
	// Counted from the listings rather than through two new interface methods:
	// this query is asked occasionally, by a client orienting itself, and
	// widening the Graph interface for it would make every fake implement two
	// methods none of them needs.
	entities, relations := len(r.Graph.ListEntities("")), len(r.Graph.ListRelations("", "", ""))
	var asOfOut *string
	if asOf != nil && *asOf != "" {
		t, perr := time.Parse(time.RFC3339, *asOf)
		if perr != nil {
			return nil, fmt.Errorf("invalid asOf %q: use an RFC 3339 instant like 2026-09-20T08:00:00Z", *asOf)
		}
		g, ferr := projection.At(ctx, r.Store, t)
		if ferr != nil {
			return nil, ferr
		}
		entities, relations = g.EntityCount(), g.RelationCount()
		formatted := t.UTC().Format(time.RFC3339Nano)
		asOfOut = &formatted
	}
	out := &generated.GraphScope{
		Entities:  entities,
		Relations: relations,
		AsOf:      asOfOut,
	}
	if r.Tenant != "" {
		t := r.Tenant
		out.Tenant = &t
	}
	if r.TenantName != "" {
		n := r.TenantName
		out.TenantName = &n
	}
	if r.Store != nil {
		if h := r.Store.PruneHorizon(); !h.IsZero() {
			s := h.UTC().Format(time.RFC3339Nano)
			out.OldestAnswerable = &s
		}
		if t, ok, err := r.Store.NewestEventTime(); err == nil && ok {
			s := t.UTC().Format(time.RFC3339Nano)
			out.NewestEvent = &s
		}
	}
	return out, nil
}
