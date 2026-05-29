// Command toise-server is the main entry point for the Toise server. It opens
// the event log, rebuilds the in-memory projection, starts the OTLP/gRPC
// ingestion receiver and the GraphQL HTTP API (with a built-in playground), and
// runs until interrupted.
//
// Phase 1 has no authentication: the servers default to loopback addresses and
// are intended for trusted networks only (see the README security note and ADR
// 0014). The MCP server and debug UI are added in later milestones.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/playground"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/graphql"
	"github.com/toise-dev/toise/internal/graphql/resolvers"
	"github.com/toise-dev/toise/internal/ingest"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/version"
)

func main() {
	listen := "127.0.0.1:8080"
	otlpListen := "127.0.0.1:4317"
	dataDir := "toise-data"
	cfg := store.DefaultConfig()

	fs := flag.NewFlagSet("toise-server", flag.ExitOnError)
	fs.StringVar(&listen, "listen", listen, "address for the GraphQL/HTTP server (loopback by default; phase 1 has no auth)")
	fs.StringVar(&otlpListen, "otlp-listen", otlpListen, "address for the OTLP/gRPC ingestion server")
	fs.StringVar(&dataDir, "data-dir", dataDir, "directory for the Pebble event log")
	fs.DurationVar(&cfg.RetentionMaxAge, "retention-max-age", cfg.RetentionMaxAge,
		"maximum age of retained events (0 = unlimited)")
	fs.DurationVar(&cfg.CompactionInterval, "retention-compaction-interval", cfg.CompactionInterval,
		"interval between heartbeat-coalescing compactions")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(listen, otlpListen, dataDir, cfg, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func run(listen, otlpListen, dataDir string, cfg store.Config, logger *slog.Logger) error {
	st, err := store.Open(dataDir, cfg)
	if err != nil {
		return fmt.Errorf("opening event log: %w", err)
	}
	defer func() { _ = st.Close() }()

	graph := projection.New()
	if err = graph.Replay(st); err != nil {
		return fmt.Errorf("rebuilding projection: %w", err)
	}
	logger.Info("projection rebuilt", "entities", graph.EntityCount(), "relations", graph.RelationCount())

	engine := change.New(graph, st)

	receiver := ingest.NewReceiver(engine, logger)
	lis, err := net.Listen("tcp", otlpListen)
	if err != nil {
		return fmt.Errorf("otlp listen on %s: %w", otlpListen, err)
	}
	go func() {
		if serveErr := receiver.Serve(lis); serveErr != nil {
			logger.Error("otlp receiver stopped", "err", serveErr)
		}
	}()
	defer receiver.Stop()

	res := &resolvers.Resolver{Graph: graph, Store: st, Engine: engine}
	mux := http.NewServeMux()
	mux.Handle("/graphql", graphql.NewHandler(res, graphql.Config{}))
	mux.Handle("/", playground.Handler("Toise", "/graphql"))
	httpSrv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Toise %s — the living map of your infrastructure\n", version.String())
	logger.Info("toise-server ready",
		"graphql", "http://"+listen+"/",
		"otlp_grpc", otlpListen,
		"data_dir", dataDir)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}
