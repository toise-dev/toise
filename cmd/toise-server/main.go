// Command toise-server is the main entry point for the Toise server.
//
// At this early stage it only prints version and project information. It will
// grow incrementally into the full server: configuration loading, the event
// store, the graph projection, the query API (GraphQL), the MCP server, OTLP
// ingestion, and a minimal debug UI.
package main

import (
	"fmt"
	"os"

	"github.com/toise-dev/toise/internal/version"
)

func main() {
	fmt.Fprintf(os.Stdout, "Toise %s — the living map of your infrastructure\n", version.String())
	fmt.Fprintln(os.Stdout, "https://toise.dev")
	os.Exit(0)
}
