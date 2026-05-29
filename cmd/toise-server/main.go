// Command toise-server is the main entry point for the Toise server.
//
// At this early stage it prints version and project information and resolves
// the retention configuration from flags. It will grow incrementally into the
// full server: configuration loading, the event store, the graph projection,
// the query API (GraphQL), the MCP server, OTLP ingestion, and a minimal debug
// UI.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/version"
)

func main() {
	cfg := store.DefaultConfig()
	fs := flag.NewFlagSet("toise-server", flag.ExitOnError)
	fs.DurationVar(&cfg.RetentionMaxAge, "retention-max-age", cfg.RetentionMaxAge,
		"maximum age of retained events (0 = unlimited)")
	fs.DurationVar(&cfg.CompactionInterval, "retention-compaction-interval", cfg.CompactionInterval,
		"interval between heartbeat-coalescing compactions")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	maxAge := "unlimited"
	if cfg.RetentionMaxAge > 0 {
		maxAge = cfg.RetentionMaxAge.String()
	}

	fmt.Fprintf(os.Stdout, "Toise %s — the living map of your infrastructure\n", version.String())
	fmt.Fprintln(os.Stdout, "https://toise.dev")
	fmt.Fprintf(os.Stdout, "retention: max-age=%s compaction-interval=%s\n", maxAge, cfg.CompactionInterval)
	os.Exit(0)
}
