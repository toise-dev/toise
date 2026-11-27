package mcp

import (
	"github.com/toise-dev/toise/internal/model"
)

// viaResolvedEndpoint marks impact reached through a read-time endpoint-to-
// listener resolution rather than a stored relation.
const viaResolvedEndpoint = "resolved_endpoint"

// endpointIndex lazily builds a reverse map from resolved target to the
// network endpoints that resolve onto it. The graph stores no edge between
// them, so impact traversal needs this to bridge endpoints to their
// canonical listeners or hosts.
type endpointIndex struct {
	built  bool
	byDest map[model.EntityID][]model.Entity
}

// endpointsFor returns the network endpoints that resolve onto target,
// building the index on first use. Only listeners and hosts can be
// resolution targets, so other types return nil without building anything.
func (ix *endpointIndex) endpointsFor(g Graph, target model.Entity) []model.Entity {
	switch target.Type {
	case model.TypeServiceListener, model.TypeHost:
	default:
		return nil
	}
	if !ix.built {
		ix.built = true
		ix.byDest = make(map[model.EntityID][]model.Entity)
		for _, ep := range g.ListEntities(model.TypeNetworkEndpoint) {
			res, ok := resolveEndpoint(g, ep)
			if !ok {
				continue
			}
			ix.byDest[res.ID] = append(ix.byDest[res.ID], ep)
		}
	}
	return ix.byDest[target.ID]
}
