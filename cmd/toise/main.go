// Command toise is the main entry point for the Toise server.
//
// At this early stage it only prints version and project information. It will
// grow incrementally into the full server: configuration loading, the event
// store, the graph projection, the query API, and the receiver runtime.
package main

import (
	"fmt"
	"os"
)

const version = "0.0.0-dev"

func main() {
	fmt.Fprintf(os.Stdout, "Toise %s — the living map of your infrastructure\n", version)
	fmt.Fprintln(os.Stdout, "https://toise.dev")
	os.Exit(0)
}
