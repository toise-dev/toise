package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/metadata"

	"github.com/toise-dev/toise/internal/auth"
	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/metrics"
	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

const baseYAML = `
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1"]
`

type reloadHarness struct {
	t          *testing.T
	path       string
	dataDir    string
	load       func() (config.Config, error)
	registry   *registry.Registry
	auth       *auth.Authenticator
	settings   *atomic.Pointer[tenantSettings]
	namesCount *atomic.Int64
	metrics    *metrics.ConfigReloads
	promReg    *prometheus.Registry
	logBuf     *lockedBuffer
	reloader   *Reloader
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (lb *lockedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.Write(p)
}

func (lb *lockedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.String()
}

func newReloadHarness(t *testing.T, yaml string) *reloadHarness {
	t.Helper()

	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "toise.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	load := func() (config.Config, error) {
		return config.Load([]string{"--config", path, "--data-dir", dataDir}, func(string) string { return "" })
	}
	cfg, err := load()
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}

	reg, err := registry.OpenWithLimits(
		dataDir,
		store.DefaultConfig(),
		time.Minute,
		registry.Limits{
			AutoCreate: cfg.TenantAutoCreate,
			Allowlist:  cfg.TenantAllowlist,
			MaxTenants: cfg.MaxTenants,
		},
		slog.New(slog.NewTextHandler(new(bytes.Buffer), nil)),
	)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reg.Close(); closeErr != nil {
			t.Errorf("close registry: %v", closeErr)
		}
	})

	scopedFull, err := cfg.TenantTokensMap()
	if err != nil {
		t.Fatalf("tenant tokens map: %v", err)
	}
	scopedRead, err := cfg.TenantReadTokensMap()
	if err != nil {
		t.Fatalf("tenant read tokens map: %v", err)
	}
	scopedIngest, err := cfg.TenantIngestTokensMap()
	if err != nil {
		t.Fatalf("tenant ingest tokens map: %v", err)
	}
	authn := auth.NewWithRoles(cfg.AuthTokens, cfg.ReadTokens, cfg.IngestTokens, scopedFull).
		WithScopedRoleTokens(scopedRead, scopedIngest)

	names := map[string]string{}
	if m, err := cfg.TenantNamesMap(); err == nil {
		names = m
	}
	retention := map[string]time.Duration{}
	if m, err := cfg.TenantRetentionMap(); err == nil {
		retention = m
	}
	settings := &atomic.Pointer[tenantSettings]{}
	settings.Store(&tenantSettings{names: names, retention: retention})

	namesCount := &atomic.Int64{}

	m := metrics.NewConfigReloads()
	promReg := prometheus.NewRegistry()
	for _, c := range m.Collectors() {
		promReg.MustRegister(c)
	}

	logBuf := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := NewReloader(ReloaderDeps{
		Load:     load,
		Initial:  cfg,
		Auth:     authn,
		Registry: reg,
		Settings: settings,
		OnNamesChanged: func() {
			namesCount.Add(1)
		},
		Metrics: m,
		Logger:  logger,
		Now:     time.Now,
	})

	return &reloadHarness{
		t:          t,
		path:       path,
		dataDir:    dataDir,
		load:       load,
		registry:   reg,
		auth:       authn,
		settings:   settings,
		namesCount: namesCount,
		metrics:    m,
		promReg:    promReg,
		logBuf:     logBuf,
		reloader:   r,
	}
}

func (h *reloadHarness) write(yaml string) {
	h.t.Helper()
	if err := os.WriteFile(h.path, []byte(yaml), 0o600); err != nil {
		h.t.Fatalf("rewrite config: %v", err)
	}
}

func (h *reloadHarness) outcome(name string) float64 {
	h.t.Helper()
	families, err := h.promReg.Gather()
	if err != nil {
		h.t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "toise_config_reloads_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "outcome" && lp.GetValue() == name {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func (h *reloadHarness) gaugeValue() float64 {
	h.t.Helper()
	families, err := h.promReg.Gather()
	if err != nil {
		h.t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "toise_config_last_reload_success_timestamp_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue()
		}
	}
	return 0
}

func (h *reloadHarness) logs() string {
	h.t.Helper()
	return h.logBuf.String()
}

func (h *reloadHarness) ingestAllowed(tenantID, token string) bool {
	h.t.Helper()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	return h.auth.AllowedForTenantGRPC(ctx, tenantID)
}

func TestReloadAddsTenantAndToken(t *testing.T) {
	h := newReloadHarness(t, baseYAML)

	if h.ingestAllowed("beta", "fake-ingest-token-2") {
		t.Fatal("beta ingest allowed before reload")
	}
	if _, err := h.registry.ForWrite("beta"); !errors.Is(err, tenant.ErrNotAllowed) {
		t.Fatalf("ForWrite(beta) before reload: %v", err)
	}

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme", "beta"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1", "beta:fake-ingest-token-2"]
`)

	if err := h.reloader.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if !h.ingestAllowed("acme", "fake-ingest-token-1") {
		t.Fatal("acme ingest token not accepted after reload")
	}
	if !h.ingestAllowed("beta", "fake-ingest-token-2") {
		t.Fatal("beta ingest token not accepted after reload")
	}
	if _, err := h.registry.ForWrite("beta"); err != nil {
		t.Fatalf("ForWrite(beta) after reload: %v", err)
	}

	if got := h.outcome("applied"); got != 1 {
		t.Fatalf("applied counter = %v, want 1", got)
	}
	if h.gaugeValue() <= 0 {
		t.Fatalf("last reload gauge = %v, want > 0", h.gaugeValue())
	}

	logs := h.logs()
	if !strings.Contains(logs, "beta") {
		t.Fatalf("logs do not mention beta:\n%s", logs)
	}
	for _, secret := range []string{"fake-ingest-token-1", "fake-ingest-token-2", "fake-global-token"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain token %q:\n%s", secret, logs)
		}
	}
}

func TestReloadRemovedTokenRefusedNextRequest(t *testing.T) {
	h := newReloadHarness(t, `
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1", "acme:fake-ingest-token-2"]
`)

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1"]
`)

	if err := h.reloader.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if h.ingestAllowed("acme", "fake-ingest-token-2") {
		t.Fatal("removed token still accepted")
	}
	if !h.ingestAllowed("acme", "fake-ingest-token-1") {
		t.Fatal("kept token refused")
	}
	if _, err := h.registry.ForWrite("acme"); err != nil {
		t.Fatalf("ForWrite(acme) after reload: %v", err)
	}
}

func TestReloadInvalidFileChangesNothing(t *testing.T) {
	h := newReloadHarness(t, baseYAML)

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme", "beta"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1", "beta:fake-ingest-token-2"]
bogus_key: 1
`)

	err := h.reloader.Reload(context.Background())
	if err == nil {
		t.Fatal("reload of invalid config succeeded, want error")
	}

	if !h.ingestAllowed("acme", "fake-ingest-token-1") {
		t.Fatal("old token refused after rejected reload")
	}
	if h.ingestAllowed("beta", "fake-ingest-token-2") {
		t.Fatal("new tenant token accepted after rejected reload")
	}
	if _, writeErr := h.registry.ForWrite("beta"); !errors.Is(writeErr, tenant.ErrNotAllowed) {
		t.Fatalf("ForWrite(beta) after rejected reload: %v", writeErr)
	}
	if got := h.outcome("rejected"); got != 1 {
		t.Fatalf("rejected counter = %v, want 1", got)
	}
	if got := h.outcome("applied"); got != 0 {
		t.Fatalf("applied counter = %v, want 0", got)
	}
	if got := h.outcome("partial"); got != 0 {
		t.Fatalf("partial counter = %v, want 0", got)
	}
	if got := h.gaugeValue(); got != 0 {
		t.Fatalf("gauge = %v, want 0", got)
	}

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1"]
tenant_tokens: "acme:fake-secret-value"
`)

	err = h.reloader.Reload(context.Background())
	if err == nil {
		t.Fatal("reload of wrong-typed config succeeded, want error")
	}
	if strings.Contains(err.Error(), "fake-secret-value") {
		t.Fatalf("error text contains secret: %v", err)
	}
	if strings.Contains(h.logs(), "fake-secret-value") {
		t.Fatalf("logs contain secret:\n%s", h.logs())
	}
}

func TestReloadNonReloadableKeyIsReportedNotApplied(t *testing.T) {
	h := newReloadHarness(t, baseYAML)

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme", "beta"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1", "beta:fake-ingest-token-2"]
log_level: debug
`)

	if err := h.reloader.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if !h.ingestAllowed("beta", "fake-ingest-token-2") {
		t.Fatal("beta change not applied")
	}
	logs := h.logs()
	if !strings.Contains(logs, "log_level") {
		t.Fatalf("logs do not name log_level:\n%s", logs)
	}
	if !strings.Contains(logs, "requires a restart") {
		t.Fatalf("logs do not mention restart:\n%s", logs)
	}
	if got := h.outcome("partial"); got != 1 {
		t.Fatalf("partial counter = %v, want 1", got)
	}
	if got := h.outcome("applied"); got != 0 {
		t.Fatalf("applied counter = %v, want 0", got)
	}

	if err := h.reloader.Reload(context.Background()); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	logs2 := h.logs()
	if strings.Count(logs2, "log_level") < 2 {
		t.Fatalf("second reload does not warn about log_level again:\n%s", logs2)
	}
}

func TestReloadRetentionAndNamesSwap(t *testing.T) {
	h := newReloadHarness(t, baseYAML)

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1"]
tenant_names: ["acme:Acme"]
`)

	if err := h.reloader.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := h.namesCount.Load(); got != 1 {
		t.Fatalf("OnNamesChanged called %v times, want 1", got)
	}
	s := h.settings.Load()
	if got := s.names["acme"]; got != "Acme" {
		t.Fatalf("names[acme] = %q, want Acme", got)
	}
	if got, ok := s.retention["acme"]; ok {
		t.Fatalf("retention[acme] = %v, want absent", got)
	}

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1"]
tenant_names: ["acme:Acme"]
tenant_retention_max_age: ["acme:48h"]
`)

	if err := h.reloader.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := h.namesCount.Load(); got != 1 {
		t.Fatalf("OnNamesChanged called %v times after retention-only change, want 1", got)
	}
	s = h.settings.Load()
	if got, ok := s.retention["acme"]; !ok || got != 48*time.Hour {
		t.Fatalf("retention[acme] = %v (ok=%v), want 48h", got, ok)
	}
	if got := s.names["acme"]; got != "Acme" {
		t.Fatalf("names[acme] = %q, want Acme", got)
	}
}

func TestReloadRefusesToTurnAuthOff(t *testing.T) {
	h := newReloadHarness(t, `
tenant_allowlist: ["acme"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1"]
`)

	h.write(`
tenant_allowlist: ["acme"]
tenant_ingest_tokens: []
`)

	if err := h.reloader.Reload(context.Background()); err == nil {
		t.Fatal("reload removing all tokens succeeded, want error")
	}
	if got := h.outcome("rejected"); got != 1 {
		t.Fatalf("rejected counter = %v, want 1", got)
	}
	if !h.ingestAllowed("acme", "fake-ingest-token-1") {
		t.Fatal("old token refused after rejected reload")
	}
}

func TestSIGHUPTriggersReload(t *testing.T) {
	h := newReloadHarness(t, baseYAML)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	h.reloader.WatchSIGHUP(ctx, &wg)

	h.write(`
auth_tokens: ["fake-global-token"]
tenant_allowlist: ["acme", "beta"]
tenant_ingest_tokens: ["acme:fake-ingest-token-1", "beta:fake-ingest-token-2"]
`)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for h.outcome("applied") != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("applied counter never reached 1; logs:\n%s", h.logs())
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	wg.Wait()
}
