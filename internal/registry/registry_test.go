package registry

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"

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

// TestQuarantinesCorruptTenantAtBoot pins #166: one tenant whose store fails to
// open must not abort the whole multi-tenant process — it is quarantined (warned,
// skipped, listed, its dir left on disk), and the healthy tenants plus the default
// still come up.
func TestQuarantinesCorruptTenantAtBoot(t *testing.T) {
	dataDir := t.TempDir()
	seedStore(t, filepath.Join(dataDir, "good"), "h-good")
	seedStore(t, filepath.Join(dataDir, "bad"), "h-bad")
	// Corrupt the bad tenant's manifest so its store cannot open.
	manifests, _ := filepath.Glob(filepath.Join(dataDir, "bad", "MANIFEST-*"))
	if len(manifests) == 0 {
		t.Fatal("test setup: no MANIFEST to corrupt")
	}
	if err := os.WriteFile(manifests[0], []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("boot must succeed despite a corrupt tenant: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	if q := reg.Quarantined(); len(q) != 1 || q[0] != "bad" {
		t.Fatalf("Quarantined() = %v, want [bad]", q)
	}
	// The healthy tenant and the default are served.
	good, ferr := reg.For("good")
	if ferr != nil || !hasHost(good.Graph, "h-good") {
		t.Errorf("good tenant should be served: err=%v", ferr)
	}
	if _, ferr := reg.For(tenant.Default); ferr != nil {
		t.Errorf("default must always open: %v", ferr)
	}
	// The corrupt dir is left on disk for recovery.
	if _, statErr := os.Stat(filepath.Join(dataDir, "bad")); statErr != nil {
		t.Errorf("quarantined tenant dir must be left on disk: %v", statErr)
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

// snapshotStack mirrors the server's snapshot body (sequence first, then the
// graph sample, then the liveness memento) so the boot path under test sees
// exactly what production writes.
func snapshotStack(t *testing.T, st *Stack) {
	t.Helper()
	seq := st.Store.Sequence()
	events := st.Graph.SnapshotEvents(time.Now())
	liveness, err := st.Engine.LivenessBlob()
	if err != nil {
		t.Fatalf("liveness blob: %v", err)
	}
	if err := st.Store.WriteSnapshot(seq, events, liveness); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// TestOpenStackRestoresLivenessMemento drives the production boot wiring
// (openStack): a snapshot carrying the memento boots with producer references
// live — the first sweep does not reap a fresh producer's entity, and a
// release by a different producer is silent because the original reference
// survived the restart (ADR 0019).
func TestOpenStackRestoresLivenessMemento(t *testing.T) {
	dataDir := t.TempDir()
	reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatal(err)
	}
	st, err := reg.For(tenant.Default)
	if err != nil {
		t.Fatal(err)
	}
	ident := []model.KeyValue{{Key: "host.id", Value: model.StringValue("h-memento")}}
	if _, oerr := st.Engine.ObserveEntity(change.EntityObservation{
		Type: model.TypeHost, Identity: ident,
		Interval: time.Hour, Producer: "p1", EventTime: time.Now(),
	}); oerr != nil {
		t.Fatal(oerr)
	}
	snapshotStack(t, st)
	if cerr := reg.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	reg2, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg2.Close() })
	st2, err := reg2.For(tenant.Default)
	if err != nil {
		t.Fatal(err)
	}
	if !hasHost(st2.Graph, "h-memento") {
		t.Fatal("snapshot did not restore the entity")
	}
	if n := st2.Engine.Sweep(); n != 0 {
		t.Fatalf("first sweep after boot expired %d, want 0 (producer is fresh)", n)
	}
	_, emitted, derr := st2.Engine.DeleteEntity(change.EntityObservation{
		Type: model.TypeHost, Identity: ident, Producer: "p2", EventTime: time.Now(),
	})
	if derr != nil {
		t.Fatal(derr)
	}
	if emitted {
		t.Error("release by another producer emitted a delete: p1's restored reference should keep the entity live")
	}
	if !hasHost(st2.Graph, "h-memento") {
		t.Error("entity gone after a foreign producer's release")
	}
}

// TestOpenStackFallsBackOnCorruptSnapshot: an unreadable projection snapshot must
// not block boot — the log is the source of truth, so openStack falls back to a
// full replay and rebuilds the graph intact (#166 P1).
func TestOpenStackFallsBackOnCorruptSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatal(err)
	}
	st, err := reg.For(tenant.Default)
	if err != nil {
		t.Fatal(err)
	}
	ident := []model.KeyValue{{Key: "host.id", Value: model.StringValue("h-corrupt")}}
	if _, oerr := st.Engine.ObserveEntity(change.EntityObservation{
		Type: model.TypeHost, Identity: ident, EventTime: time.Now(),
	}); oerr != nil {
		t.Fatal(oerr)
	}
	snapshotStack(t, st)
	if cerr := reg.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	// Corrupt the persisted snapshot (too short to hold the 8-byte reference
	// sequence). "meta/snapshot" is the store's stable on-disk snapshot key.
	db, err := pebble.Open(stackDir(reg, tenant.Default), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if serr := db.Set([]byte("meta/snapshot"), []byte{0x01}, pebble.Sync); serr != nil {
		t.Fatal(serr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	reg2, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatalf("boot must succeed despite a corrupt snapshot: %v", err)
	}
	t.Cleanup(func() { _ = reg2.Close() })
	st2, err := reg2.For(tenant.Default)
	if err != nil {
		t.Fatal(err)
	}
	if !hasHost(st2.Graph, "h-corrupt") {
		t.Fatal("full-replay fallback did not rebuild the entity")
	}
}

// TestOpenStackFloorsRestoredDeadlines pins the #164 boot grace end-to-end:
// a memento whose deadlines lapsed during downtime must not feed a delete
// storm on the first sweep after boot; only a sweep past boot+interval reaps.
func TestOpenStackFloorsRestoredDeadlines(t *testing.T) {
	dataDir := t.TempDir()
	reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatal(err)
	}
	st, err := reg.For(tenant.Default)
	if err != nil {
		t.Fatal(err)
	}
	if _, oerr := st.Engine.ObserveEntity(change.EntityObservation{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h-floor")}},
		Interval: 50 * time.Millisecond, Producer: "p1", EventTime: time.Now(),
	}); oerr != nil {
		t.Fatal(oerr)
	}
	snapshotStack(t, st)
	if cerr := reg.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	// Downtime longer than the producer's interval: the stored deadline lapses.
	time.Sleep(120 * time.Millisecond)

	reg2, err := Open(dataDir, store.DefaultConfig(), 0, discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg2.Close() })
	st2, err := reg2.For(tenant.Default)
	if err != nil {
		t.Fatal(err)
	}
	if n := st2.Engine.Sweep(); n != 0 {
		t.Fatalf("sweep right after boot expired %d, want 0 (deadline floored to boot+interval)", n)
	}
	if !hasHost(st2.Graph, "h-floor") {
		t.Fatal("entity reaped by the boot-time sweep despite the grace floor")
	}

	// Past the floored deadline the producer is genuinely silent: reap it.
	time.Sleep(120 * time.Millisecond)
	if n := st2.Engine.Sweep(); n != 1 {
		t.Fatalf("post-grace sweep expired %d, want 1", n)
	}
	if hasHost(st2.Graph, "h-floor") {
		t.Error("silent producer's entity survived past the floored deadline")
	}
}

// TestOpenStackToleratesBrokenLivenessSection: neither JSON-level corruption
// nor a physically truncated liveness section may fail the tenant boot — the
// projection is intact and the sweep backstop re-arms on the next
// observations (#164).
func TestOpenStackToleratesBrokenLivenessSection(t *testing.T) {
	t.Run("corrupt json", func(t *testing.T) {
		dataDir := t.TempDir()
		reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
		if err != nil {
			t.Fatal(err)
		}
		st, err := reg.For(tenant.Default)
		if err != nil {
			t.Fatal(err)
		}
		observeHost(t, st.Engine, "h-corrupt")
		seq := st.Store.Sequence()
		if werr := st.Store.WriteSnapshot(seq, st.Graph.SnapshotEvents(time.Now()), []byte(`{"refs": broken`)); werr != nil {
			t.Fatal(werr)
		}
		if cerr := reg.Close(); cerr != nil {
			t.Fatal(cerr)
		}

		var logs strings.Builder
		reg2, err := Open(dataDir, store.DefaultConfig(), 0, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil {
			t.Fatalf("boot with corrupt liveness section failed: %v", err)
		}
		t.Cleanup(func() { _ = reg2.Close() })
		st2, err := reg2.For(tenant.Default)
		if err != nil {
			t.Fatal(err)
		}
		if !hasHost(st2.Graph, "h-corrupt") {
			t.Error("projection lost despite intact snapshot events")
		}
		if !strings.Contains(logs.String(), "liveness") {
			t.Error("no warning logged for the unreadable liveness section")
		}
	})

	t.Run("truncated section", func(t *testing.T) {
		dataDir := t.TempDir()
		reg, err := Open(dataDir, store.DefaultConfig(), 0, discard())
		if err != nil {
			t.Fatal(err)
		}
		st, err := reg.For(tenant.Default)
		if err != nil {
			t.Fatal(err)
		}
		observeHost(t, st.Engine, "h-truncated")
		if cerr := reg.Close(); cerr != nil {
			t.Fatal(cerr)
		}

		// Plant a snapshot whose liveness section declares more bytes than
		// follow — the torn-write shape that used to fail ReadSnapshot and
		// with it the whole tenant.
		db, err := pebble.Open(filepath.Join(dataDir, tenant.Default), &pebble.Options{})
		if err != nil {
			t.Fatal(err)
		}
		torn := make([]byte, 0, 24)
		torn = append(torn, make([]byte, 8)...)       // seq 0: replay everything
		torn = append(torn, 0xFF, 0xFF, 0xFF, 0xFF)   // liveness sentinel
		torn = append(torn, 0x00, 0x00, 0x04, 0x00)   // declares 1024 bytes...
		torn = append(torn, []byte(`{"refs":{"x`)...) // ...but the write tore here
		if serr := db.Set([]byte("meta/snapshot"), torn, pebble.Sync); serr != nil {
			t.Fatal(serr)
		}
		if cerr := db.Close(); cerr != nil {
			t.Fatal(cerr)
		}

		var logs strings.Builder
		reg2, err := Open(dataDir, store.DefaultConfig(), 0, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil {
			t.Fatalf("boot with truncated liveness section failed: %v", err)
		}
		t.Cleanup(func() { _ = reg2.Close() })
		st2, err := reg2.For(tenant.Default)
		if err != nil {
			t.Fatal(err)
		}
		if !hasHost(st2.Graph, "h-truncated") {
			t.Error("projection lost despite a replayable event log")
		}
		if !strings.Contains(logs.String(), "liveness") {
			t.Error("no warning logged for the truncated liveness section")
		}
	})
}
