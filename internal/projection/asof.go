package projection

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// TimeRangeReader streams the event log over an event-time range and reports the
// retention horizon. The store satisfies it. It is the slice of the store the
// as-of fold needs, kept narrow so MCP and GraphQL share one implementation.
type TimeRangeReader interface {
	ScanByTimeRange(ctx context.Context, start, end time.Time, fn func(model.Event) error) error
	PruneHorizon() time.Time
}

// asOfSem bounds simultaneous as-of folds across the whole process (every tenant
// and read surface): each one streams the log up to its instant, so an unbounded
// burst of as-of queries could exhaust CPU and memory. The live-graph reads are
// unaffected — only the fold path acquires it.
var asOfSem = make(chan struct{}, maxAsOfFolds())

func maxAsOfFolds() int {
	if n := runtime.NumCPU() / 2; n > 1 {
		return n
	}
	return 1
}

// At folds the event log into a fresh graph as it was at t (inclusive,
// event-time): every event whose event_time is at or before t, applied in
// event-time order. It STREAMS off the time index and applies each event as it
// arrives — no intermediate slice — under ctx, so the caller's per-call budget
// bounds the cost. An instant before the retention horizon is refused outright:
// those events are pruned, and a silently partial graph would mislead worse than
// an error (#135). This is the single as-of service shared by the MCP and
// GraphQL read paths (#166).
func At(ctx context.Context, r TimeRangeReader, t time.Time) (*Graph, error) {
	if h := r.PruneHorizon(); !h.IsZero() && t.Before(h) {
		return nil, fmt.Errorf("as-of %s is before the retention horizon %s: events that old have been pruned; query at or after the horizon",
			t.UTC().Format(time.RFC3339), h.UTC().Format(time.RFC3339))
	}
	select {
	case asOfSem <- struct{}{}:
		defer func() { <-asOfSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	g := New()
	if err := r.ScanByTimeRange(ctx, time.Unix(0, 0), t.Add(time.Nanosecond), func(ev model.Event) error {
		g.Apply(ev)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("folding events up to %s: %w", t.UTC().Format(time.RFC3339), err)
	}
	return g, nil
}
