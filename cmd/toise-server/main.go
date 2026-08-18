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
	"crypto/tls"
	"crypto/x509"
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

	"github.com/toise-dev/toise/internal/audit"
	"github.com/toise-dev/toise/internal/auth"
	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/debugui"
	"github.com/toise-dev/toise/internal/graphql"
	"github.com/toise-dev/toise/internal/graphql/resolvers"
	"github.com/toise-dev/toise/internal/ingest"
	"github.com/toise-dev/toise/internal/logship"
	"github.com/toise-dev/toise/internal/mcp"
	"github.com/toise-dev/toise/internal/metrics"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/oidc"
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

	if len(os.Args) > 1 && os.Args[1] == "drop-snapshot" {
		if err := runDropSnapshot(os.Args[2:], os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "delete-tenant" {
		if err := runDeleteTenant(os.Args[2:], os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "restore-log" {
		if err := runRestoreLog(os.Args[2:], os.Getenv); err != nil {
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
	storeCfg.AcceptUnknownTypes = cfg.AcceptUnknownTypes

	logger := slog.New(cfg.NewLogHandler(os.Stderr))
	if err := run(cfg, storeCfg, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// ingestTLSConfig returns the OTLP ingest listener's TLS config: a clone of base
// (the server certificate) plus, when clientCAFile is set, required and verified
// client-certificate auth — optional mTLS for ingest (ADR 0028) — against that
// PEM CA bundle. An empty clientCAFile returns base's clone unchanged. The HTTP
// read surfaces are unaffected (they authenticate by bearer/OIDC, not certs).
func ingestTLSConfig(base *tls.Config, clientCAFile string) (*tls.Config, error) {
	conf := base.Clone()
	if clientCAFile == "" {
		return conf, nil
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("reading TLS client CA %s: %w", clientCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("TLS client CA %s contains no usable certificates", clientCAFile)
	}
	conf.ClientCAs = pool
	conf.ClientAuth = tls.RequireAndVerifyClientCert
	return conf, nil
}

func run(cfg config.Config, storeCfg store.Config, logger *slog.Logger) error {
	// One {store, projection, engine} stack per tenant under <data-dir>/<tenant>/
	// (ADR 0025). A legacy single-tenant data dir is migrated to the default tenant
	// on open; existing tenants and the default are opened up front.
	reg, err := registry.OpenWithLimits(cfg.DataDir, storeCfg, cfg.RelationBufferTTL.D(), registry.Limits{
		AutoCreate: cfg.TenantAutoCreate,
		Allowlist:  cfg.TenantAllowlist,
		MaxTenants: cfg.MaxTenants,
	}, logger)
	if err != nil {
		return fmt.Errorf("opening tenant registry: %w", err)
	}
	defer func() { _ = reg.Close() }()

	// Final snapshot at graceful shutdown, declared right after the close so it
	// runs just before it (after the maintenance loops have joined and ingest
	// has stopped): producer references acquired since the last periodic
	// snapshot would otherwise be lost, leaving permanent zombies for producers
	// that die during the downtime (#139). Per-tenant failures are logged, never
	// allowed to abort the shutdown.
	if cfg.SnapshotInterval.D() > 0 {
		defer func() {
			for _, st := range reg.Stacks() {
				if serr := writeTenantSnapshot(st, logger); serr != nil {
					logger.Error("final snapshot at shutdown failed", "tenant", st.Tenant, "err", serr)
				}
			}
		}()
	}

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
		if serveErr := mcp.New(st.Graph, st.Store).SetAnnotations(st.Annotations).SetIdentityThreshold(cfg.IdentityThreshold).ServeStdio(ctx); serveErr != nil && !errors.Is(serveErr, context.Canceled) {
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
	scopedTokens, err := cfg.TenantTokensMap()
	if err != nil {
		return err
	}
	scopedRead, err := cfg.TenantReadTokensMap()
	if err != nil {
		return err
	}
	scopedIngest, err := cfg.TenantIngestTokensMap()
	if err != nil {
		return err
	}
	authn := auth.NewWithRoles(cfg.AuthTokens, cfg.ReadTokens, cfg.IngestTokens, scopedTokens).
		WithScopedRoleTokens(scopedRead, scopedIngest) // per-tenant RBAC (ADR 0028)
	authn.SetTenantTrustMode(cfg.DeriveOnlyTenancy()) // ADR 0028 anti-spoofing; off by default
	authn.SetIngestMTLSOnly(cfg.IngestMTLSOnly)       // #262: ingest gated by mTLS, reads by scoped token
	if cfg.OIDCEnabled() {
		// OIDC/JWT verification on the read surfaces (ADR 0028). Discovery hits the
		// issuer once at startup; off unless an issuer is configured.
		dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
		verifier, oerr := oidc.New(dctx, oidc.Config{
			Issuer: cfg.OIDCIssuer, Audience: cfg.OIDCAudience,
			TenantClaim: cfg.OIDCTenantClaim, RoleClaim: cfg.OIDCRoleClaim,
		})
		dcancel()
		if oerr != nil {
			return oerr
		}
		authn.SetOIDC(verifier)
		logger.Info("OIDC verification enabled on read surfaces", "issuer", cfg.OIDCIssuer)
	}
	var grpcOpts []grpc.ServerOption
	if authn.Enabled() {
		grpcOpts = append(grpcOpts,
			grpc.UnaryInterceptor(authn.UnaryInterceptor()),
			grpc.StreamInterceptor(authn.StreamInterceptor()))
	}
	var tlsConf *tls.Config
	if cfg.TLSEnabled() {
		// Explicit TLS posture (#144): minimum 1.2, and the certificate is
		// re-read per handshake so a renewed cert (certbot & friends) is
		// picked up without a restart. The load error surfaces at startup.
		if _, terr := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile); terr != nil {
			return fmt.Errorf("loading TLS credentials: %w", terr)
		}
		tlsConf = &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, lerr := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
				if lerr != nil {
					return nil, lerr
				}
				return &cert, nil
			},
		}
		// The OTLP ingest listener gets its own TLS config so optional mTLS
		// (client-certificate auth, ADR 0028) applies to ingest only — the HTTP
		// read surfaces stay on bearer/OIDC, not client certs. Off unless a client
		// CA is configured.
		ingestTLS, terr := ingestTLSConfig(tlsConf, cfg.TLSClientCAFile)
		if terr != nil {
			return terr
		}
		if cfg.TLSClientCAFile != "" {
			logger.Info("mTLS enabled on OTLP ingest", "client_ca", cfg.TLSClientCAFile)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(ingestTLS)))
	}

	ingestMetrics := ingest.NewMetrics()
	maint := metrics.NewMaintenance()
	authFailures := metrics.NewAuthFailures()
	authn.OnFailure(authFailures.Inc)
	queryMetrics := metrics.NewQueryMetrics() // per-MCP-tool call/duration, shared across tenants

	// An exposed listener without auth or TLS deserves a loud line at startup:
	// the trusted-network defaults are loopback, and leaving them is a choice
	// that should look like one (#115).
	if !authn.Enabled() || !cfg.TLSEnabled() {
		for _, addr := range []string{cfg.Listen, cfg.OTLPListen} {
			if !loopbackAddr(addr) {
				logger.Warn("listener is not loopback but auth and/or TLS are off — exposed deployments should set TOISE_AUTH_TOKENS and TLS (ADR 0024)",
					"addr", addr, "auth", authn.Enabled(), "tls", cfg.TLSEnabled())
			}
		}
	}

	// errc carries a fatal serve error from either server. A receiver that dies
	// after startup MUST reach it: otherwise the process keeps serving HTTP,
	// /readyz stays green, and ingestion is silently dead while the liveness
	// sweep starts expiring entities (#112). Exiting lets the supervisor restart.
	errc := make(chan error, 2)

	receiver := ingest.NewRoutedReceiver(engineFor, authn.AllowedForTenantGRPC, ingestMetrics, cfg.AcceptUnknownTypes, logger, grpcOpts...)
	if cfg.DeriveOnlyTenancy() {
		// derive-only: a scoped token's tenant is derived and locked, ignoring a
		// client X-Scope-OrgID / tenant.id resource attribute (ADR 0028).
		receiver.SetTenantResolver(authn.EffectiveTenantGRPC)
	}
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

	// Audit sink for operator writes (ADR 0028): an append-only JSON-line file,
	// off unless configured. One sink, shared across the per-tenant handlers.
	var auditor *audit.Auditor
	if cfg.AuditLog != "" {
		f, ferr := os.OpenFile(cfg.AuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if ferr != nil {
			return fmt.Errorf("opening audit log %s: %w", cfg.AuditLog, ferr)
		}
		defer f.Close()
		auditor = audit.New(f, logger)
		logger.Info("audit log enabled", "path", cfg.AuditLog)
	}

	// The GraphQL, MCP and debug-UI surfaces are scoped per tenant: a router builds
	// one handler per tenant on first use, bound to that tenant's stack, and
	// dispatches by the X-Scope-OrgID header (ADR 0025).
	graphqlRouter := newTenantRouter(reg, logger, func(st *registry.Stack) (http.Handler, error) {
		res := &resolvers.Resolver{
			Graph: st.Graph, Store: st.Store, Engine: st.Engine,
			Annotations: st.Annotations, Audit: auditor,
			IdentityThreshold: cfg.IdentityThreshold,
		}
		return graphql.NewHandler(res, graphql.Config{
			AllowedOrigins:       cfg.AllowedOrigins,
			DisableIntrospection: !cfg.GraphQLIntrospection,
		}), nil
	})
	mcpRouter := newTenantRouter(reg, logger, func(st *registry.Stack) (http.Handler, error) {
		return mcp.New(st.Graph, st.Store).SetObserver(queryMetrics).SetAnnotations(st.Annotations).SetAuditor(auditor).SetIdentityThreshold(cfg.IdentityThreshold).HTTPHandler(), nil
	})
	if cfg.DeriveOnlyTenancy() || cfg.OIDCEnabled() {
		// Route to the effective tenant: a derive-only scoped token's own tenant,
		// or an OIDC identity's claim tenant — ignoring the client X-Scope-OrgID
		// (ADR 0028). The auth middleware authorizes against the same tenant.
		graphqlRouter.resolve = authn.EffectiveTenantHTTP
		mcpRouter.resolve = authn.EffectiveTenantHTTP
	}

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
	quarantined := metrics.NewQuarantinedTenants()
	if q := reg.Quarantined(); len(q) > 0 {
		quarantined.Set(float64(len(q)))
		logger.Warn("tenants quarantined at boot (stores failed to open, left on disk for recovery)", "count", len(q), "tenants", q)
	}
	openTenants := metrics.NewOpenTenants(func() float64 { return float64(reg.TenantCount()) })
	metricsExtra := append(ingestMetrics.Collectors(), authFailures, quarantined, openTenants)
	metricsExtra = append(metricsExtra, maint.Collectors()...)
	metricsExtra = append(metricsExtra, queryMetrics.Collectors()...)
	mux.Handle("/metrics", metrics.Handler(metrics.NewCollector(
		aggregateGraph{reg}, aggregateStore{reg}, version.Version, version.Commit), metricsExtra...))
	if cfg.Playground {
		mux.Handle("/playground", playground.Handler("Toise", "/graphql"))
	}
	if cfg.DebugUI {
		// The tenant switcher exists only where tenant selection is already
		// client-controlled. Under claim-derived tenancy the claim resolver stays
		// in charge, the ?tenant= parameter is never consulted, and the tenant
		// list is not offered — a scoped reader must not learn which other
		// tenants exist (ADR 0028).
		var listTenants func() []string
		if !cfg.DeriveOnlyTenancy() && !cfg.OIDCEnabled() {
			listTenants = func() []string {
				stacks := reg.Stacks()
				ids := make([]string, len(stacks))
				for i, st := range stacks {
					ids[i] = st.Tenant
				}
				return ids
			}
		}
		debugRouter := newTenantRouter(reg, logger, func(st *registry.Stack) (http.Handler, error) {
			return debugui.New(st.Graph, st.Store, st.Tenant, listTenants)
		})
		if cfg.DeriveOnlyTenancy() || cfg.OIDCEnabled() {
			debugRouter.resolve = authn.EffectiveTenantHTTP
		} else {
			debugRouter.resolve = tenant.FromHTTPWithQuery
		}
		mux.Handle("/", debugRouter)
	}
	// Auth wraps the data surfaces; the operational probes/scrape stay public.
	public := map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           authn.HTTPMiddleware(public)(mux),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         tlsConf,
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
						_ = maint.Observe("sweep", st.Tenant, func() error {
							res, err := st.Engine.Sweep()
							if res.Total() > 0 {
								logger.Info("liveness sweep expired stale records", "tenant", st.Tenant,
									"entities", res.Entities, "relations", res.Relations)
							}
							return err
						})
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
						if err := maint.Observe("coalesce", st.Tenant, func() error {
							n, cerr := st.Store.CoalesceHeartbeats()
							if cerr != nil {
								return cerr
							}
							if n > 0 {
								logger.Info("coalesced heartbeat records", "tenant", st.Tenant, "removed", n)
							}
							return nil
						}); err != nil {
							logger.Error("heartbeat coalescing failed", "tenant", st.Tenant, "err", err)
						}
						if storeCfg.RetentionMaxAge > 0 {
							cutoff := time.Now().Add(-storeCfg.RetentionMaxAge)
							if err := maint.Observe("prune", st.Tenant, func() error {
								ev, by, perr := st.Store.PruneOlderThan(cutoff)
								if perr != nil {
									return perr
								}
								if ev > 0 {
									logger.Info("pruned events past retention", "tenant", st.Tenant, "events", ev, "bytes", by, "older_than", storeCfg.RetentionMaxAge.String())
								}
								return nil
							}); err != nil {
								logger.Error("retention pruning failed", "tenant", st.Tenant, "err", err)
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
	// The snapshot also carries the liveness memento (#139): with sweeping on but
	// snapshots off, producer refs die with every restart and a producer that was
	// merely quiet across the restart leaves zombies — worth a loud line.
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
						if err := maint.Observe("snapshot", st.Tenant, func() error {
							return writeTenantSnapshot(st, logger)
						}); err != nil {
							logger.Error("snapshot write failed", "tenant", st.Tenant, "err", err)
						}
					}
				}
			}
		}()
	} else if cfg.LivenessSweepInterval.D() > 0 {
		logger.Warn("liveness sweeping is on but snapshots are disabled: producer references will NOT survive restarts, so entities of producers that die while the server is down are never expired — set snapshot_interval (default 5m)")
	}

	// Periodic online backups (ADR 0029): every interval, checkpoint each tenant's
	// event log (Pebble's lock-free checkpoint) into <backup-dir>/<timestamp>/<tenant>
	// for an operator to sync off-node. Off unless backup_dir + backup_interval set.
	if cfg.BackupDir != "" && cfg.BackupInterval.D() > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(cfg.BackupInterval.D())
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					dest := filepath.Join(cfg.BackupDir, time.Now().UTC().Format("20060102T150405Z"))
					for _, st := range reg.Stacks() {
						if err := maint.Observe("backup", st.Tenant, func() error {
							return st.Store.Checkpoint(filepath.Join(dest, st.Tenant))
						}); err != nil {
							logger.Error("backup checkpoint failed", "tenant", st.Tenant, "dest", dest, "err", err)
						}
					}
					logger.Info("backup written", "dest", dest, "tenants", len(reg.Stacks()))
				}
			}
		}()
		logger.Info("scheduled backups enabled", "dir", cfg.BackupDir, "interval", cfg.BackupInterval.D())
	}

	// Continuous log shipping (ADR 0029): every interval, export each tenant's new
	// event-log tail as an immutable segment — the finer-RPO complement to the full
	// checkpoint backup. The target is a directory (mounted bucket/NFS/rsync) or an
	// S3-compatible store; the cursor is derived from the sink, so it survives
	// restarts without local state. Off unless a target + interval are set.
	if cfg.LogShipEnabled() {
		sink, serr := buildLogSink(cfg)
		if serr != nil {
			return fmt.Errorf("log shipping: %w", serr)
		}
		target := cfg.LogShipDir
		if cfg.LogShipS3Enabled() {
			target = "s3://" + cfg.LogShipS3Bucket
		}
		shipper := logship.New(sink)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(cfg.LogShipInterval.D())
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for _, st := range reg.Stacks() {
						if err := maint.Observe("logship", st.Tenant, func() error {
							n, err := shipper.Ship(ctx, st.Tenant, st.Store)
							if err == nil && n > 0 {
								logger.Info("log segment shipped", "tenant", st.Tenant, "events", n)
							}
							return err
						}); err != nil {
							logger.Error("log shipping failed", "tenant", st.Tenant, "err", err)
						}
					}
				}
			}
		}()
		logger.Info("log shipping enabled", "target", target, "interval", cfg.LogShipInterval.D())
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
			// Cert/key come from TLSConfig.GetCertificate; empty args keep them
			// off the legacy path.
			errc <- httpSrv.ListenAndServeTLS("", "")
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
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			// Long-lived streams (the MCP SSE listening stream, GraphQL WS
			// subscriptions) never drain on their own, so Shutdown's grace
			// always elapses when a client is connected. Cutting them is the
			// intended outcome of a deploy, not a failure: fall through to
			// Close and exit clean (#130).
			if errors.Is(err, context.DeadlineExceeded) {
				logger.Info("shutdown grace elapsed with streams still open; closing them")
				if cerr := httpSrv.Close(); cerr != nil {
					return fmt.Errorf("closing http server: %w", cerr)
				}
				return nil
			}
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	}
}

// writeTenantSnapshot writes one projection snapshot for st: the reference
// sequence is read before sampling the graph so the replayed tail overlaps
// idempotently (#49), and the liveness memento rides along (#139). A failed
// liveness blob degrades to a snapshot without it rather than no snapshot.
func writeTenantSnapshot(st *registry.Stack, logger *slog.Logger) error {
	seq := st.Store.Sequence()
	events := st.Graph.SnapshotEvents(time.Now())
	liveness, lerr := st.Engine.LivenessBlob()
	if lerr != nil {
		logger.Error("liveness snapshot failed; writing snapshot without it", "tenant", st.Tenant, "err", lerr)
	}
	if werr := st.Store.WriteSnapshot(seq, events, liveness); werr != nil {
		return werr
	}
	logger.Info("wrote projection snapshot", "tenant", st.Tenant, "snapshot_seq", seq, "events", len(events))
	return nil
}

// runCheckpoint takes a consistent Pebble checkpoint of every tenant store
// into <dst>/<tenant> — the operator-facing trigger for Store.Checkpoint the
// docs promise (#115). The data dir resolves with the server's precedence
// (defaults < config file < TOISE_* env < flags), and the stores are opened
// strictly read-only: a backup must never mint or alter what it backs up, so
// a missing data dir or one holding no tenant stores is a hard error instead
// of an empty "success" (#162). It is a cold-backup tool: run it while
// toise-server is stopped (a running server holds the pebble lock, and the
// open fails cleanly).
func runCheckpoint(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("toise-server checkpoint", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML config file (env: TOISE_CONFIG)")
	dataDir := fs.String("data-dir", "", "directory of the tenant event logs (env: TOISE_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: toise-server checkpoint [--config file] [--data-dir dir] <destination-dir>")
	}
	dst := fs.Arg(0)

	var cfgArgs []string
	if *configPath != "" {
		cfgArgs = append(cfgArgs, "--config="+*configPath)
	}
	if *dataDir != "" {
		cfgArgs = append(cfgArgs, "--data-dir="+*dataDir)
	}
	cfg, err := config.LoadCold(cfgArgs, getenv)
	if err != nil {
		return fmt.Errorf("resolving configuration: %w", err)
	}

	stores, err := registry.OpenExisting(cfg.DataDir, store.DefaultConfig(), slog.Default())
	if err != nil {
		return fmt.Errorf("opening tenant stores: %w", err)
	}
	defer func() {
		for _, ts := range stores {
			_ = ts.Store.Close()
		}
	}()
	for _, ts := range stores {
		out := filepath.Join(dst, ts.Tenant)
		if cerr := ts.Store.Checkpoint(out); cerr != nil {
			return fmt.Errorf("tenant %s: %w", ts.Tenant, cerr)
		}
		fmt.Printf("checkpointed tenant %s -> %s\n", ts.Tenant, out)
	}
	return nil
}

// runDropSnapshot deletes the persisted projection snapshot of every tenant
// store so the next start replays the full log — the recovery path for a corrupt
// snapshot that fails to read (the server falls back to full replay and warns;
// dropping it stops the warning and lets a fresh snapshot be written). The event
// log is untouched. Cold tool: run it while toise-server is stopped (a running
// server holds the pebble lock and the open fails cleanly).
func runDropSnapshot(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("toise-server drop-snapshot", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML config file (env: TOISE_CONFIG)")
	dataDir := fs.String("data-dir", "", "directory of the tenant event logs (env: TOISE_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: toise-server drop-snapshot [--config file] [--data-dir dir]")
	}

	var cfgArgs []string
	if *configPath != "" {
		cfgArgs = append(cfgArgs, "--config="+*configPath)
	}
	if *dataDir != "" {
		cfgArgs = append(cfgArgs, "--data-dir="+*dataDir)
	}
	cfg, err := config.LoadCold(cfgArgs, getenv)
	if err != nil {
		return fmt.Errorf("resolving configuration: %w", err)
	}

	stores, err := registry.OpenExistingWritable(cfg.DataDir, store.DefaultConfig(), slog.Default())
	if err != nil {
		return fmt.Errorf("opening tenant stores: %w", err)
	}
	defer func() {
		for _, ts := range stores {
			_ = ts.Store.Close()
		}
	}()
	for _, ts := range stores {
		if derr := ts.Store.DropSnapshot(); derr != nil {
			return fmt.Errorf("tenant %s: %w", ts.Tenant, derr)
		}
		fmt.Printf("dropped snapshot for tenant %s\n", ts.Tenant)
	}
	return nil
}

// runDeleteTenant removes a tenant's entire on-disk stack (event log + snapshot)
// from the data dir — the operator-facing tenant-deletion procedure (#166). It is
// destructive and a COLD tool: run it with toise-server stopped (a running server
// holds the pebble lock and serves the tenant). The default tenant cannot be
// deleted. MaxTenants then has room for a new one.
func runDeleteTenant(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("toise-server delete-tenant", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML config file (env: TOISE_CONFIG)")
	dataDir := fs.String("data-dir", "", "directory of the tenant event logs (env: TOISE_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: toise-server delete-tenant [--config file] [--data-dir dir] <tenant-id>")
	}
	id, ok := tenant.Sanitize(fs.Arg(0))
	if !ok {
		return fmt.Errorf("invalid tenant id %q", fs.Arg(0))
	}
	if id == tenant.Default {
		return fmt.Errorf("the %q tenant cannot be deleted", tenant.Default)
	}

	var cfgArgs []string
	if *configPath != "" {
		cfgArgs = append(cfgArgs, "--config="+*configPath)
	}
	if *dataDir != "" {
		cfgArgs = append(cfgArgs, "--data-dir="+*dataDir)
	}
	cfg, err := config.LoadCold(cfgArgs, getenv)
	if err != nil {
		return fmt.Errorf("resolving configuration: %w", err)
	}
	dir := filepath.Join(cfg.DataDir, id)
	if _, serr := os.Stat(dir); serr != nil {
		return fmt.Errorf("tenant %q not found under %s: %w", id, cfg.DataDir, serr)
	}
	// Take the tenant's Pebble lock before removing anything: a running server
	// holds it, so this fails cleanly instead of deleting files out from under
	// an open DB (which would leave a phantom in-memory tenant and a corrupt dir).
	// Same guard the checkpoint / drop-snapshot subcommands use.
	lock, lerr := store.Open(dir, store.DefaultConfig())
	if lerr != nil {
		return fmt.Errorf("tenant %q is in use (is the server running?): %w", id, lerr)
	}
	if cerr := lock.Close(); cerr != nil {
		return fmt.Errorf("releasing tenant %q lock: %w", id, cerr)
	}
	if rerr := os.RemoveAll(dir); rerr != nil {
		return fmt.Errorf("removing tenant %q: %w", id, rerr)
	}
	fmt.Printf("deleted tenant %s (%s)\n", id, dir)
	return nil
}

// runRestoreLog rebuilds tenant event logs from shipped segments (logship,
// ADR 0029) into a data dir: for each tenant under --from (or just --tenant) it
// replays the contiguous segments and re-appends the events into a fresh
// per-tenant store, faithfully reconstructing the log (the bi-temporal facts ride
// on the events; the projection rebuilds on the next server start). It refuses to
// write into a store that already holds events, so it never clobbers or
// duplicates an existing log — restore into an empty/new data dir. COLD tool: run
// it with toise-server stopped.
func runRestoreLog(args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("toise-server restore-log", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a YAML config file (env: TOISE_CONFIG)")
	dataDir := fs.String("data-dir", "", "destination data dir to reconstruct into (env: TOISE_DATA_DIR)")
	from := fs.String("from", "", "read segments from this directory; default: the configured shipping target (log_shipping_dir or the S3 bucket)")
	only := fs.String("tenant", "", "restore only this tenant (default: every tenant found at the source)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var cfgArgs []string
	if *configPath != "" {
		cfgArgs = append(cfgArgs, "--config="+*configPath)
	}
	if *dataDir != "" {
		cfgArgs = append(cfgArgs, "--data-dir="+*dataDir)
	}
	cfg, err := config.LoadCold(cfgArgs, getenv)
	if err != nil {
		return fmt.Errorf("resolving configuration: %w", err)
	}

	// Source: an explicit --from directory, else the configured shipping target —
	// S3 when a bucket is set, otherwise log_shipping_dir. Restore from wherever you
	// ship to, or from a local copy (a mirror, or a checkpoint of the segments).
	var sink logship.Sink
	switch {
	case *from != "":
		sink, err = logship.NewFileSink(*from)
	case cfg.LogShipS3Enabled() || cfg.LogShipDir != "":
		sink, err = buildLogSink(cfg)
	default:
		return fmt.Errorf("no source: pass --from <dir>, or configure a shipping target (log_shipping_dir or log_shipping_s3_bucket)")
	}
	if err != nil {
		return err
	}
	shipper := logship.New(sink)
	ctx := context.Background()

	var tenants []string
	if *only != "" {
		id, ok := tenant.Sanitize(*only)
		if !ok {
			return fmt.Errorf("invalid tenant id %q", *only)
		}
		tenants = []string{id}
	} else if tenants, err = shipper.Tenants(ctx); err != nil {
		return err
	}
	if len(tenants) == 0 {
		return fmt.Errorf("no shipped segments found under %s", *from)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", cfg.DataDir, err)
	}
	for _, id := range tenants {
		if err := restoreTenantLog(ctx, shipper, cfg.DataDir, id); err != nil {
			return fmt.Errorf("tenant %s: %w", id, err)
		}
	}
	return nil
}

func restoreTenantLog(ctx context.Context, shipper *logship.Shipper, dataDir, id string) error {
	// A restore reconstructs previously-accepted events; it does not re-police
	// vocabulary, so relax the type check (the events passed it once already).
	scfg := store.DefaultConfig()
	scfg.AcceptUnknownTypes = true
	st, err := store.Open(filepath.Join(dataDir, id), scfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if st.Sequence() > 0 {
		return fmt.Errorf("destination store already holds %d events; restore into an empty data dir", st.Sequence())
	}

	const batchSize = 1000
	batch := make([]model.Event, 0, batchSize)
	count := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if aerr := st.Append(batch...); aerr != nil {
			return aerr
		}
		batch = batch[:0]
		return nil
	}
	if rerr := shipper.Replay(ctx, id, func(ev model.Event) error {
		batch = append(batch, ev)
		count++
		if len(batch) >= batchSize {
			return flush()
		}
		return nil
	}); rerr != nil {
		return rerr
	}
	if ferr := flush(); ferr != nil {
		return ferr
	}
	fmt.Printf("restored tenant %s -> %s (%d events)\n", id, filepath.Join(dataDir, id), count)
	return nil
}

// buildLogSink picks the log-shipping sink from config: an S3-compatible store
// when a bucket is set (works against AWS S3 and MinIO/Ceph/R2/… alike),
// otherwise a directory.
func buildLogSink(cfg config.Config) (logship.Sink, error) {
	if cfg.LogShipS3Enabled() {
		return logship.NewS3Sink(logship.S3Config{
			Endpoint:  cfg.LogShipS3Endpoint,
			Bucket:    cfg.LogShipS3Bucket,
			Region:    cfg.LogShipS3Region,
			Prefix:    cfg.LogShipS3Prefix,
			AccessKey: cfg.LogShipS3AccessKey,
			SecretKey: cfg.LogShipS3SecretKey,
			UseSSL:    cfg.LogShipS3UseSSL,
		})
	}
	return logship.NewFileSink(cfg.LogShipDir)
}

// loopbackAddr reports whether addr binds a loopback interface.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
