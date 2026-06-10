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
// The servers default to loopback addresses and no authentication — the
// trusted-network posture (ADR 0014). For exposed deployments, bearer-token auth
// and TLS are opt-in (ADR 0024), and --production locks down the development
// surfaces. The graph is scoped per tenant by the X-Scope-OrgID metadata, each
// tenant living under <data-dir>/<tenant>/ (ADR 0025).
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
	"path/filepath"
	"sync"
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
	"github.com/toise-dev/toise/internal/ops"
	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
	"github.com/toise-dev/toise/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "checkpoint" {
		if err := runCheckpoint(os.Args[2:], os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

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
	// One {store, projection, engine} stack per tenant under <data-dir>/<tenant>/
	// (ADR 0025). A legacy single-tenant data dir is migrated to the default tenant
	// on open; existing tenants and the default are opened up front.
	reg, err := registry.Open(cfg.DataDir, storeCfg, cfg.RelationBufferTTL.D(), logger)
	if err != nil {
		return fmt.Errorf("opening tenant registry: %w", err)
	}
	defer func() { _ = reg.Close() }()

	if cfg.MCPStdio {
		// stdio carries no per-request tenant metadata, so it serves the default
		// tenant only (ADR 0025).
		st, ferr := reg.For(tenant.Default)
		if ferr != nil {
			return fmt.Errorf("opening default tenant: %w", ferr)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		logger.Info("serving MCP over stdio", "data_dir", cfg.DataDir, "tenant", tenant.Default)
		if serveErr := mcp.New(st.Graph, st.Store).ServeStdio(ctx); serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			return fmt.Errorf("mcp stdio: %w", serveErr)
		}
		return nil
	}

	engineFor := func(t string) (*change.Engine, error) {
		st, ferr := reg.For(t)
		if ferr != nil {
			return nil, ferr
		}
		return st.Engine, nil
	}

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

	ingestMetrics := ingest.NewMetrics()
	authFailures := metrics.NewAuthFailures()
	authn.OnFailure(authFailures.Inc)

	// errc carries a fatal serve error from either server. A receiver that dies
	// after startup MUST reach it: otherwise the process keeps serving HTTP,
	// /readyz stays green, and ingestion is silently dead while the liveness
	// sweep starts expiring entities (#112). Exiting lets the supervisor restart.
	errc := make(chan error, 2)

	receiver := ingest.NewRoutedReceiver(engineFor, ingestMetrics, logger, grpcOpts...)
	lis, err := net.Listen("tcp", cfg.OTLPListen)
	if err != nil {
		return fmt.Errorf("otlp listen on %s: %w", cfg.OTLPListen, err)
	}
	go func() {
		if serveErr := receiver.Serve(lis); serveErr != nil {
			errc <- fmt.Errorf("otlp receiver: %w", serveErr)
		}
	}()
	defer receiver.Stop()

	// The GraphQL, MCP and debug-UI surfaces are scoped per tenant: a router builds
	// one handler per tenant on first use, bound to that tenant's stack, and
	// dispatches by the X-Scope-OrgID header (ADR 0025).
	graphqlRouter := newTenantRouter(reg, logger, func(st *registry.Stack) (http.Handler, error) {
		res := &resolvers.Resolver{Graph: st.Graph, Store: st.Store, Engine: st.Engine}
		return graphql.NewHandler(res, graphql.Config{
			AllowedOrigins:       cfg.AllowedOrigins,
			DisableIntrospection: !cfg.GraphQLIntrospection,
		}), nil
	})
	mcpRouter := newTenantRouter(reg, logger, func(st *registry.Stack) (http.Handler, error) {
		return mcp.New(st.Graph, st.Store).HTTPHandler(), nil
	})

	mux := http.NewServeMux()
	mux.Handle("/graphql", graphqlRouter)
	mux.Handle("/mcp", mcpRouter)
	mux.Handle("/healthz", ops.Healthz())
	mux.Handle("/readyz", ops.Readyz(func() error {
		for _, st := range reg.Stacks() {
			if herr := st.Store.Healthy(); herr != nil {
				return fmt.Errorf("tenant %s: %w", st.Tenant, herr)
			}
		}
		return nil
	}))
	metricsExtra := append(ingestMetrics.Collectors(), authFailures)
	mux.Handle("/metrics", metrics.Handler(metrics.NewCollector(
		aggregateGraph{reg}, aggregateStore{reg}, version.Version, version.Commit), metricsExtra...))
	if cfg.Playground {
		mux.Handle("/playground", playground.Handler("Toise", "/graphql"))
	}
	if cfg.DebugUI {
		debugRouter := newTenantRouter(reg, logger, func(st *registry.Stack) (http.Handler, error) {
			return debugui.New(st.Graph, st.Store)
		})
		mux.Handle("/", debugRouter)
	}
	// Auth wraps the data surfaces; the operational probes/scrape stay public.
	public := map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           authn.HTTPMiddleware(public)(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The maintenance loops below append, prune, and snapshot against the tenant
	// stores; they must be joined before the deferred reg.Close() closes pebble
	// under them (DB.Close is unsafe concurrently with any other method —
	// "panic: pebble: closed" at SIGTERM otherwise, #112). LIFO defers: cancel,
	// join the loops, then receiver.Stop and reg.Close run.
	var wg sync.WaitGroup
	defer func() {
		stop()
		wg.Wait()
	}()

	if sweep := cfg.LivenessSweepInterval.D(); sweep > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(sweep)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, st := range reg.Stacks() {
						if n := st.Engine.Sweep(); n > 0 {
							logger.Info("liveness sweep expired stale entities", "tenant", st.Tenant, "count", n)
						}
					}
				}
			}
		}()
	}

	// Compaction: coalesce heartbeat runs, and — when a retention max-age is set —
	// prune events older than it to bound on-disk growth (the current-state
	// projection is preserved). Runs per tenant. See ADR 0013, #45.
	if storeCfg.CompactionInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(storeCfg.CompactionInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, st := range reg.Stacks() {
						if n, err := st.Store.CoalesceHeartbeats(); err != nil {
							logger.Error("heartbeat coalescing failed", "tenant", st.Tenant, "err", err)
						} else if n > 0 {
							logger.Info("coalesced heartbeat records", "tenant", st.Tenant, "removed", n)
						}
						if storeCfg.RetentionMaxAge > 0 {
							cutoff := time.Now().Add(-storeCfg.RetentionMaxAge)
							if ev, by, err := st.Store.PruneOlderThan(cutoff); err != nil {
								logger.Error("retention pruning failed", "tenant", st.Tenant, "err", err)
							} else if ev > 0 {
								logger.Info("pruned events past retention", "tenant", st.Tenant, "events", ev, "bytes", by, "older_than", storeCfg.RetentionMaxAge.String())
							}
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(snap)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, st := range reg.Stacks() {
						seq := st.Store.Sequence()
						events := st.Graph.SnapshotEvents(time.Now())
						if werr := st.Store.WriteSnapshot(seq, events); werr != nil {
							logger.Error("snapshot write failed", "tenant", st.Tenant, "err", werr)
						} else {
							logger.Info("wrote projection snapshot", "tenant", st.Tenant, "snapshot_seq", seq, "events", len(events))
						}
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
		"tenants", len(reg.Stacks()),
		"tls", cfg.TLSEnabled(),
		"auth", authn.Enabled(),
		"production", cfg.Production)

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
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// runCheckpoint takes a consistent Pebble checkpoint of every tenant store
// into <dst>/<tenant> — the operator-facing trigger for Store.Checkpoint the
// docs promise (#115). It opens the data dir directly, so it is a cold-backup
// tool: run it while toise-server is stopped (a running server holds the
// pebble lock, and the open fails cleanly).
func runCheckpoint(args []string, getenv func(string) string) error {
	defaultDir := "toise-data"
	if v := getenv("TOISE_DATA_DIR"); v != "" {
		defaultDir = v
	}
	fs := flag.NewFlagSet("toise-server checkpoint", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDir, "directory of the tenant event logs (env: TOISE_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: toise-server checkpoint [--data-dir dir] <destination-dir>")
	}
	dst := fs.Arg(0)

	reg, err := registry.Open(*dataDir, store.DefaultConfig(), 0, slog.Default())
	if err != nil {
		return fmt.Errorf("opening tenant registry (is toise-server still running?): %w", err)
	}
	defer func() { _ = reg.Close() }()
	for _, st := range reg.Stacks() {
		out := filepath.Join(dst, st.Tenant)
		if err := st.Store.Checkpoint(out); err != nil {
			return fmt.Errorf("tenant %s: %w", st.Tenant, err)
		}
		fmt.Printf("checkpointed tenant %s -> %s\n", st.Tenant, out)
	}
	return nil
}
