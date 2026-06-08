package registry

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

var clock = time.Unix(1_700_000_000, 0).UTC()

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func observeHost(t *testing.T, eng *change.Engine, hostID string) {
	t.Helper()
	if _, err := eng.ObserveEntity(change.EntityObservation{
		Type:      model.TypeHost,
		Identity:  []model.KeyValue{{Key: "host.id", Value: model.StringValue(hostID)}},
		EventTime: clock,
	}); err != nil {
		t.Fatalf("observe host %s: %v", hostID, err)
	}
}

func hasHost(g *projection.Graph, hostID string) bool {
	_, found := g.MatchIdentity(model.TypeHost, []model.KeyValue{{Key: "host.id", Value: model.StringValue(hostID)}})
	return found
}

func TestOpenFreshHasDefault(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	stacks := reg.Stacks()
	if len(stacks) != 1 || stacks[0].Tenant != tenant.Default {
		t.Fatalf("fresh registry stacks = %v, want [default]", tenantNames(stacks))
	}
}

func TestForIsolatesTenants(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	a, err := reg.For("acme")
	if err != nil {
		t.Fatalf("for acme: %v", err)
	}
	b, err := reg.For("globex")
	if err != nil {
		t.Fatalf("for globex: %v", err)
	}
	observeHost(t, a.Engine, "h-a")
	observeHost(t, b.Engine, "h-b")

	if !hasHost(a.Graph, "h-a") || hasHost(a.Graph, "h-b") {
		t.Error("acme graph is not isolated from globex")
	}
	if !hasHost(b.Graph, "h-b") || hasHost(b.Graph, "h-a") {
		t.Error("globex graph is not isolated from acme")
	}

	// Each tenant has its own on-disk store directory.
	for _, id := range []string{"acme", "globex", tenant.Default} {
		if _, err := os.Stat(filepath.Join(stackDir(reg, id))); err != nil {
			t.Errorf("tenant %s store dir missing: %v", id, err)
		}
	}
}

func TestForCachesStack(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	a1, _ := reg.For("acme")
	a2, _ := reg.For("acme")
	if a1 != a2 {
		t.Error("For returned distinct stacks for the same tenant")
	}
}

func TestForRejectsInvalidTenant(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	if _, err := reg.For("../escape"); err == nil {
		t.Error("expected an error for an invalid tenant id, got nil")
	}
}

func TestPreOpensExistingTenantDirs(t *testing.T) {
	dataDir := t.TempDir()
	// Seed an existing tenant directory with persisted data.
	seedStore(t, filepath.Join(dataDir, "acme"), "h-seed")

	reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	names := tenantNames(reg.Stacks())
	if !contains(names, "acme") || !contains(names, tenant.Default) {
		t.Fatalf("stacks = %v, want acme and default pre-opened", names)
	}
	a, _ := reg.For("acme")
	if !hasHost(a.Graph, "h-seed") {
		t.Error("pre-opened acme did not rebuild its persisted host")
	}
}

func TestMigratesLegacyDataDir(t *testing.T) {
	dataDir := t.TempDir()
	// A legacy single-tenant build writes the Pebble store directly under dataDir.
	seedStore(t, dataDir, "h-legacy")
	if _, err := os.Stat(filepath.Join(dataDir, legacyMarker)); err != nil {
		t.Fatalf("test setup: legacy marker not present: %v", err)
	}

	reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	def, _ := reg.For(tenant.Default)
	if !hasHost(def.Graph, "h-legacy") {
		t.Error("default tenant did not inherit the migrated legacy graph")
	}
	if _, err := os.Stat(filepath.Join(dataDir, legacyMarker)); !os.IsNotExist(err) {
		t.Errorf("legacy store left at data-dir root after migration (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, tenant.Default, legacyMarker)); err != nil {
		t.Errorf("migrated store not found under default tenant: %v", err)
	}
}

// --- helpers -----------------------------------------------------------------

// seedStore opens a store at dir, persists one host, and closes it — simulating a
// pre-existing data directory.
func seedStore(t *testing.T, dir, hostID string) {
	t.Helper()
	st, err := store.Open(dir, store.DefaultConfig())
	if err != nil {
		t.Fatalf("seed store open: %v", err)
	}
	g := projection.New()
	eng := change.New(g, st, change.WithClock(func() time.Time { return clock }))
	observeHost(t, eng, hostID)
	if err := st.Close(); err != nil {
		t.Fatalf("seed store close: %v", err)
	}
}

func stackDir(reg *Registry, id string) string { return filepath.Join(reg.dataDir, id) }

func tenantNames(stacks []*Stack) []string {
	out := make([]string, len(stacks))
	for i, s := range stacks {
		out[i] = s.Tenant
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
