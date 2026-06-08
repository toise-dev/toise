// Command toise-server is the main entry point for the Toise server. It opens
// the event log, rebuilds the in-memory projection, starts the OTLP/gRPC
// ingestion receiver and an HTTP server, and runs until interrupted. The HTTP
// server exposes the GraphQL API at /graphql (with a playground at /playground),
// the MCP server at /mcp (Streamable HTTP), a minimal debug UI at /, the
// /healthz (liveness) and /readyz (readiness) probes, and Prometheus /metrics.
// The MCP server can alternatively be run over stdio with --mcp-stdio (for Claude
// Desktop).
//
// Phase 1 has no authentication: the servers default to loopback addresses and
// are intended for trusted networks only (see the README security note and ADR
// 0014).
package main

import (
	"context"
	"errors"
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
	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/debugui"
	"github.com/toise-dev/toise/internal/graphql"
	"github.com/toise-dev/toise/internal/graphql/resolvers"
	"github.com/toise-dev/toise/internal/ingest"
	"github.com/toise-dev/toise/internal/mcp"
	"github.com/toise-dev/toise/internal/metrics"
	"github.com/toise-dev/toise/internal/ops"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/version"
)

func main() {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if errors.Is(err, config.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	storeCfg := store.DefaultConfig()
	storeCfg.RetentionMaxAge = cfg.RetentionMaxAge.D()
	storeCfg.CompactionInterval = cfg.CompactionInterval.D()

	logger := slog.New(cfg.NewLogHandler(os.Stderr))
	if err := run(cfg.Listen, cfg.OTLPListen, cfg.DataDir, cfg.MCPStdio,
		cfg.RelationBufferTTL.D(), cfg.LivenessSweepInterval.D(), storeCfg, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func run(listen, otlpListen, dataDir string, mcpStdio bool, relationBufferTTL, livenessSweepInterval time.Duration, cfg store.Config, logger *slog.Logger) error {
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

	if mcpStdio {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		logger.Info("serving MCP over stdio", "data_dir", dataDir)
		if serveErr := mcp.New(graph, st).ServeStdio(ctx); serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			return fmt.Errorf("mcp stdio: %w", serveErr)
		}
		return nil
	}

	engine := change.New(graph, st,
		change.WithLogger(logger),
		change.WithRelationBuffer(relationBufferTTL))

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

	ui, err := debugui.New(graph, st)
	if err != nil {
		return fmt.Errorf("building debug UI: %w", err)
	}

	res := &resolvers.Resolver{Graph: graph, Store: st, Engine: engine}
	mux := http.NewServeMux()
	mux.Handle("/graphql", graphql.NewHandler(res, graphql.Config{}))
	mux.Handle("/mcp", mcp.New(graph, st).HTTPHandler())
	mux.Handle("/playground", playground.Handler("Toise", "/graphql"))
	mux.Handle("/healthz", ops.Healthz())
	mux.Handle("/readyz", ops.Readyz(func() error { return st.Healthy() }))
	mux.Handle("/metrics", metrics.Handler(metrics.NewCollector(graph, st, version.Version, version.Commit)))
	mux.Handle("/", ui)
	httpSrv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if livenessSweepInterval > 0 {
		go func() {
			ticker := time.NewTicker(livenessSweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if n := engine.Sweep(); n > 0 {
						logger.Info("liveness sweep expired stale entities", "count", n)
					}
				}
			}
		}()
	}

	// Compaction: coalesce heartbeat runs, and — when a retention max-age is set —
	// prune events older than it to bound on-disk growth (the current-state
	// projection is preserved). See ADR 0013, #45.
	if cfg.CompactionInterval > 0 {
		go func() {
			ticker := time.NewTicker(cfg.CompactionInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if n, err := st.CoalesceHeartbeats(); err != nil {
						logger.Error("heartbeat coalescing failed", "err", err)
					} else if n > 0 {
						logger.Info("coalesced heartbeat records", "removed", n)
					}
					if cfg.RetentionMaxAge > 0 {
						cutoff := time.Now().Add(-cfg.RetentionMaxAge)
						if ev, by, err := st.PruneOlderThan(cutoff); err != nil {
							logger.Error("retention pruning failed", "err", err)
						} else if ev > 0 {
							logger.Info("pruned events past retention", "events", ev, "bytes", by, "older_than", cfg.RetentionMaxAge.String())
						}
					}
				}
			}
		}()
	}

	fmt.Printf("Toise %s — the living map of your infrastructure\n", version.String())
	logger.Info("toise-server ready",
		"debug_ui", "http://"+listen+"/",
		"graphql", "http://"+listen+"/graphql",
		"mcp", "http://"+listen+"/mcp",
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
