package mcp

import (
	"context"

	"github.com/toise-dev/toise/internal/projection"
)

// graphAt resolves which graph a read tool queries: the live projection when
// asOf is empty, otherwise a projection folded from the event log up to asOf via
// the shared as-of service (projection.At), which streams the fold under the
// caller's context (the per-tool budget bounds it), refuses an asOf before the
// retention horizon, and bounds concurrent folds.
//
// The reading is EVENT-TIME ("the world as it was at T"): events whose
// event_time is at or before asOf, applied in event-time order. The audit
// reading ("what Toise knew at T", recorded-at) stays where it already lives —
// entity_history's as_known_at.
func (s *Server) graphAt(ctx context.Context, asOf string) (Graph, error) {
	if asOf == "" {
		return s.graph, nil
	}
	t, err := parseOptTime(asOf, "as_of")
	if err != nil {
		return nil, err
	}
	return projection.At(ctx, s.store, t)
}
