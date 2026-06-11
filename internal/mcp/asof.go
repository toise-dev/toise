package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/toise-dev/toise/internal/projection"
)

// graphAt resolves which graph a read tool queries: the live projection when
// asOf is empty, otherwise a projection folded from the event log up to asOf.
//
// The as-of reading is EVENT-TIME ("the world as it was at T"): events whose
// event_time is at or before asOf, applied in event-time order. The audit
// reading ("what Toise knew at T", recorded-at) stays where it already lives —
// entity_history's as_known_at. The fold streams off the time index and runs
// under the caller's context, so the per-tool budget (#125) bounds its cost;
// an asOf older than the retention horizon is rejected outright — those events
// are pruned and a silent partial graph would be worse than an error (#135).
func (s *Server) graphAt(ctx context.Context, asOf string) (Graph, error) {
	if asOf == "" {
		return s.graph, nil
	}
	t, err := parseOptTime(asOf, "as_of")
	if err != nil {
		return nil, err
	}
	if h := s.store.PruneHorizon(); !h.IsZero() && t.Before(h) {
		return nil, fmt.Errorf("as_of %s is before the retention horizon %s: events that old have been pruned; query at or after the horizon",
			t.UTC().Format(time.RFC3339), h.Format(time.RFC3339))
	}
	evs, err := s.store.ReadByTimeRange(ctx, time.Unix(0, 0), t.Add(time.Nanosecond))
	if err != nil {
		return nil, fmt.Errorf("reading events up to %s: %w", t.UTC().Format(time.RFC3339), err)
	}
	g := projection.New()
	for i := range evs {
		g.Apply(evs[i])
	}
	return g, nil
}
