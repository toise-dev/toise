package resolvers

import (
	"context"
	"time"

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
// newestEvent stays null here: the resolver's event-reader interface does not
// expose it, and this surface reports what it can reach rather than deriving a
// freshness figure from something else. The MCP graph block carries it.
func (r *queryResolver) GraphScope(_ context.Context) (*generated.GraphScope, error) {
	// Counted from the listings rather than through two new interface methods:
	// this query is asked occasionally, by a client orienting itself, and
	// widening the Graph interface for it would make every fake implement two
	// methods none of them needs.
	out := &generated.GraphScope{
		Entities:  len(r.Graph.ListEntities("")),
		Relations: len(r.Graph.ListRelations("", "", "")),
	}
	if r.Tenant != "" {
		t := r.Tenant
		out.Tenant = &t
	}
	if r.Store != nil {
		if h := r.Store.PruneHorizon(); !h.IsZero() {
			s := h.UTC().Format(time.RFC3339Nano)
			out.OldestAnswerable = &s
		}
	}
	return out, nil
}
