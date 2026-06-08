// Command toise-server is the main entry point for the Toise server. It opens
// the event log, rebuilds the in-memory projection, starts the OTLP/gRPC
// ingestion receiver and an HTTP server, and runs until interrupted. The HTTP
// server exposes the GraphQL API at /graphql, the MCP server at /mcp (Streamable
// HTTP), the /healthz (liveness) and /readyz (readiness) probes, and Prometheus
// /metrics. The GraphQL playground (/playground), the debug UI (/), and GraphQL
// introspection are on by default but can be gated off individually or all at once
// with --production. The MCP server can alternatively be run over stdio with
// --mcp-stdio (for Claude Desktop).
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/toise-dev/toise/internal/auth"
	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/debugui"
	"github.com/toise-dev/toise/internal/graphql"
	"github.com/toise-dev/toise/internal/graphql/resolvers"
	"github.com/toise-dev/toise/internal/ingest"
	"github.com/toise-dev/toise/internal/mcp"
	"github.com/toise-dev/toise/internal/metrics"
	"github.com/toise-dev/toise/internal/model"
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
	if err := run(cfg, storeCfg, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, storeCfg store.Config, logger *slog.Logger) error {
	st, err := store.Open(cfg.DataDir, storeCfg)
	if err != nil {
		return fmt.Errorf("opening event log: %w", err)
	}
	defer func() { _ = st.Close() }()

	graph := projection.New()
	restoredFrom := uint64(0)
	if seq, snapEvents, ok, rerr := st.ReadSnapshot(); rerr != nil {
		return fmt.Errorf("reading snapshot: %w", rerr)
	} else if ok {
		for i := range snapEvents {
			graph.Apply(snapEvents[i])
		}
		restoredFrom = seq
		logger.Info("restored projection from snapshot", "snapshot_seq", seq, "snapshot_events", len(snapEvents))
	}
	if err = st.ScanFrom(restoredFrom, func(_ uint64, ev model.Event) error {
		graph.Apply(ev)
		return nil
	}); err != nil {
		return fmt.Errorf("replaying event tail: %w", err)
	}
	logger.Info("projection rebuilt", "entities", graph.EntityCount(), "relations", graph.RelationCount(), "from_snapshot_seq", restoredFrom)

	if cfg.MCPStdio {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		logger.Info("serving MCP over stdio", "data_dir", cfg.DataDir)
		if serveErr := mcp.New(graph, st).ServeStdio(ctx); serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			return fmt.Errorf("mcp stdio: %w", serveErr)
		}
		return nil
	}

	engine := change.New(graph, st,
		change.WithLogger(logger),
		change.WithRelationBuffer(cfg.RelationBufferTTL.D()))

	// Optional bearer-token auth and TLS on the data surfaces (ADR 0024). Both
	// off by default — the trusted-network posture (ADR 0014) is unchanged.
	authn := auth.New(cfg.AuthTokens)
	var grpcOpts []grpc.ServerOption
	if authn.Enabled() {
		grpcOpts = append(grpcOpts,
			grpc.UnaryInterceptor(authn.UnaryInterceptor()),
			grpc.StreamInterceptor(authn.StreamInterceptor()))
	}
	if cfg.TLSEnabled() {
		creds, terr := credentials.NewServerTLSFromFile(cfg.TLSCertFile, cfg.TLSKeyFile)
		if terr != nil {
			return fmt.Errorf("loading TLS credentials: %w", terr)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}

	receiver := ingest.NewReceiver(engine, logger, grpcOpts...)
	lis, err := net.Listen("tcp", cfg.OTLPListen)
	if err != nil {
		return fmt.Errorf("otlp listen on %s: %w", cfg.OTLPListen, err)
	}
	go func() {
		if serveErr := receiver.Serve(lis); serveErr != nil {
			logger.Error("otlp receiver stopped", "err", serveErr)
		}
	}()
	defer receiver.Stop()

	res := &resolvers.Resolver{Graph: graph, Store: st, Engine: engine}
	mux := http.NewServeMux()
	mux.Handle("/graphql", graphql.NewHandler(res, graphql.Config{
		AllowedOrigins:       cfg.AllowedOrigins,
		DisableIntrospection: !cfg.GraphQLIntrospection,
	}))
	mux.Handle("/mcp", mcp.New(graph, st).HTTPHandler())
	mux.Handle("/healthz", ops.Healthz())
	mux.Handle("/readyz", ops.Readyz(func() error { return st.Healthy() }))
	mux.Handle("/metrics", metrics.Handler(metrics.NewCollector(graph, st, version.Version, version.Commit)))
	if cfg.Playground {
		mux.Handle("/playground", playground.Handler("Toise", "/graphql"))
	}
	if cfg.DebugUI {
		ui, uierr := debugui.New(graph, st)
		if uierr != nil {
			return fmt.Errorf("building debug UI: %w", uierr)
		}
		mux.Handle("/", ui)
	}
	// Auth wraps the data surfaces; the operational probes/scrape stay public.
	public := map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           authn.HTTPMiddleware(public)(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if sweep := cfg.LivenessSweepInterval.D(); sweep > 0 {
		go func() {
			ticker := time.NewTicker(sweep)
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
	if storeCfg.CompactionInterval > 0 {
		go func() {
			ticker := time.NewTicker(storeCfg.CompactionInterval)
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
					if storeCfg.RetentionMaxAge > 0 {
						cutoff := time.Now().Add(-storeCfg.RetentionMaxAge)
						if ev, by, err := st.PruneOlderThan(cutoff); err != nil {
							logger.Error("retention pruning failed", "err", err)
						} else if ev > 0 {
							logger.Info("pruned events past retention", "events", ev, "bytes", by, "older_than", storeCfg.RetentionMaxAge.String())
						}
					}
				}
			}
		}()
	}

	scheme := "http"
	if cfg.TLSEnabled() {
		scheme = "https"
	}
	// Periodic projection snapshot for fast restart: replay only the tail since the
	// snapshot on the next start. The reference sequence is read before sampling the
	// graph, so the replayed tail overlaps idempotently rather than skips (#49).
	if snap := cfg.SnapshotInterval.D(); snap > 0 {
		go func() {
			ticker := time.NewTicker(snap)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					seq := st.Sequence()
					events := graph.SnapshotEvents(time.Now())
					if werr := st.WriteSnapshot(seq, events); werr != nil {
						logger.Error("snapshot write failed", "err", werr)
					} else {
						logger.Info("wrote projection snapshot", "snapshot_seq", seq, "events", len(events))
					}
				}
			}
		}()
	}

	fmt.Printf("Toise %s — the living map of your infrastructure\n", version.String())
	logger.Info("toise-server ready",
		"graphql", scheme+"://"+cfg.Listen+"/graphql",
		"mcp", scheme+"://"+cfg.Listen+"/mcp",
		"metrics", scheme+"://"+cfg.Listen+"/metrics",
		"otlp_grpc", cfg.OTLPListen,
		"data_dir", cfg.DataDir,
		"tls", cfg.TLSEnabled(),
		"auth", authn.Enabled(),
		"production", cfg.Production)

	errc := make(chan error, 1)
	go func() {
		if cfg.TLSEnabled() {
			errc <- httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			errc <- httpSrv.ListenAndServe()
		}
	}()

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
