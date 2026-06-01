// Command toise-demo seeds a Toise event log with the phase-1 demonstration
// fixture — "a day in the life of web-server-1" (see internal/demo and
// docs/demo/scenario.md). Point toise-server at the same --data-dir afterwards
// to explore the result via the debug UI, GraphQL, or the MCP tools.
//
//	toise-demo --data-dir ./demo-data
//	toise-server --data-dir ./demo-data
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/demo"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/version"
)

func main() {
	dataDir := "toise-demo-data"
	startStr := ""

	fs := flag.NewFlagSet("toise-demo", flag.ExitOnError)
	fs.StringVar(&dataDir, "data-dir", dataDir, "directory for the Pebble event log to seed")
	fs.StringVar(&startStr, "start", startStr, "scenario start time (RFC 3339); defaults to 24h before now so the timeline ends about now")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(dataDir, startStr); err != nil {
		fmt.Fprintln(os.Stderr, "toise-demo:", err)
		os.Exit(1)
	}
}

func run(dataDir, startStr string) error {
	start := time.Now().UTC().Add(-24 * time.Hour)
	if startStr != "" {
		parsed, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return fmt.Errorf("parsing --start %q: %w (use RFC 3339, e.g. 2026-06-01T00:00:00Z)", startStr, err)
		}
		start = parsed.UTC()
	}

	st, err := store.Open(dataDir, store.DefaultConfig())
	if err != nil {
		return fmt.Errorf("opening event log: %w", err)
	}
	defer func() { _ = st.Close() }()

	graph := projection.New()
	if replayErr := graph.Replay(st); replayErr != nil {
		return fmt.Errorf("rebuilding projection: %w", replayErr)
	}
	if graph.EntityCount() > 0 {
		return fmt.Errorf("%s already contains %d entities; seed into a fresh, empty data directory", dataDir, graph.EntityCount())
	}

	clk := demo.NewClock()
	eng := change.New(graph, st, change.WithClock(clk.Now))
	summary, err := demo.Run(eng, clk, start)
	if err != nil {
		return fmt.Errorf("running scenario: %w", err)
	}

	fmt.Printf("Toise %s — seeded demo scenario \"a day in the life of web-server-1\"\n", version.String())
	fmt.Printf("  span:     %s → %s\n", summary.Start.Format(time.RFC3339), summary.End.Format(time.RFC3339))
	fmt.Printf("  events:   %d observations applied\n", summary.Events)
	fmt.Printf("  graph:    %d live entities, %d relations\n", graph.EntityCount(), graph.RelationCount())
	fmt.Printf("  data dir: %s\n\n", dataDir)
	fmt.Printf("Explore it:\n  toise-server --data-dir %s\n  then open http://127.0.0.1:8080/ (debug UI), /graphql, or connect an MCP client to /mcp\n", dataDir)
	return nil
}
