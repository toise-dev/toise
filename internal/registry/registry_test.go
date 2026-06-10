package registry

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

// TestLimitsBoundTenantMinting pins #115: runtime creation respects
// auto-create, the allowlist, and the cap, while pre-existing tenants and the
// default always open.
func TestLimitsBoundTenantMinting(t *testing.T) {
	dir := t.TempDir()
	// Seed a persisted tenant, then reopen with restrictive limits.
	seed, err := Open(dir, store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ferr := seed.For("persisted"); ferr != nil {
		t.Fatal(ferr)
	}
	if cerr := seed.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	reg, err := OpenWithLimits(dir, store.DefaultConfig(), 0, Limits{AutoCreate: false}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	if _, ferr := reg.For("persisted"); ferr != nil {
		t.Errorf("pre-existing tenant must open under auto-create=false: %v", ferr)
	}
	if _, ferr := reg.For(tenant.Default); ferr != nil {
		t.Errorf("default must always open: %v", ferr)
	}
	if _, ferr := reg.For("fresh"); !errors.Is(ferr, tenant.ErrNotAllowed) {
		t.Errorf("minting with auto-create=false = %v, want ErrNotAllowed", ferr)
	}

	// Allowlist: only listed ids may be minted.
	dir2 := t.TempDir()
	reg2, err := OpenWithLimits(dir2, store.DefaultConfig(), 0,
		Limits{AutoCreate: true, Allowlist: []string{"acme"}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg2.Close() })
	if _, ferr := reg2.For("acme"); ferr != nil {
		t.Errorf("allowlisted tenant refused: %v", ferr)
	}
	if _, ferr := reg2.For("globex"); !errors.Is(ferr, tenant.ErrNotAllowed) {
		t.Errorf("non-allowlisted mint = %v, want ErrNotAllowed", ferr)
	}

	// Cap: creation beyond max is refused.
	dir3 := t.TempDir()
	reg3, err := OpenWithLimits(dir3, store.DefaultConfig(), 0,
		Limits{AutoCreate: true, MaxTenants: 2}, slog.New(slog.DiscardHandler)) // default counts for 1
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg3.Close() })
	if _, ferr := reg3.For("one"); ferr != nil {
		t.Fatal(ferr)
	}
	if _, ferr := reg3.For("two"); !errors.Is(ferr, tenant.ErrNotAllowed) {
		t.Errorf("mint over the cap = %v, want ErrNotAllowed", ferr)
	}

	// Peek never creates.
	if _, ok := reg3.Peek("ghost"); ok {
		t.Error("Peek returned a stack for an unknown tenant")
	}
	if _, serr := os.Stat(filepath.Join(dir3, "ghost")); !os.IsNotExist(serr) {
		t.Error("Peek created a tenant directory")
	}
}

// TestConcurrentForOpensOnce pins the singleflight: concurrent first requests
// for one tenant open its stack exactly once, off the registry mutex.
func TestConcurrentForOpensOnce(t *testing.T) {
	reg, err := Open(t.TempDir(), store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	const callers = 16
	stacks := make([]*Stack, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, ferr := reg.For("racy")
			if ferr != nil {
				t.Error(ferr)
				return
			}
			stacks[i] = s
		}(i)
	}
	wg.Wait()
	for i := 1; i < callers; i++ {
		if stacks[i] != stacks[0] {
			t.Fatalf("caller %d got a different stack instance", i)
		}
	}
}
