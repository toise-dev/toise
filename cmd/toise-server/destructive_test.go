package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

// seedTenant creates a tenant store under dataDir/<id> with one host entity and
// closes it, so a cold subcommand can operate on a realistic on-disk stack.
func seedTenant(t *testing.T, dataDir, id string) {
	t.Helper()
	st, err := store.Open(filepath.Join(dataDir, id), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	when := time.Unix(1_700_000_000, 0).UTC()
	ev := model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityCreated,
		Entity: model.Entity{ID: model.EntityID(id + "-e1"), Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}}},
		EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
	if aerr := st.Append(ev); aerr != nil {
		t.Fatalf("append to %s: %v", id, aerr)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close %s: %v", id, cerr)
	}
}

func noEnv(string) string { return "" }

// TestDeleteTenantRemovesOnlyItsStack pins the destructive core of the
// operator-facing delete-tenant procedure (#166): it removes exactly the named
// tenant's on-disk stack and leaves every other tenant intact.
func TestDeleteTenantRemovesOnlyItsStack(t *testing.T) {
	dataDir := t.TempDir()
	seedTenant(t, dataDir, "acme")
	seedTenant(t, dataDir, "globex")

	if err := runDeleteTenant([]string{"--data-dir", dataDir, "acme"}, noEnv); err != nil {
		t.Fatalf("runDeleteTenant: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "acme")); !os.IsNotExist(err) {
		t.Errorf("acme stack still present (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "globex")); err != nil {
		t.Errorf("globex stack was disturbed (stat err = %v)", err)
	}
}

// TestDeleteTenantRefusesDefault pins the guard: the default tenant is the
// always-present zero-config store and must never be deletable — the refusal
// happens before any filesystem touch.
func TestDeleteTenantRefusesDefault(t *testing.T) {
	dataDir := t.TempDir()
	seedTenant(t, dataDir, tenant.Default)

	err := runDeleteTenant([]string{"--data-dir", dataDir, tenant.Default}, noEnv)
	if err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("err = %v, want a refusal to delete the default tenant", err)
	}
	if _, serr := os.Stat(filepath.Join(dataDir, tenant.Default)); serr != nil {
		t.Errorf("the default stack was removed despite the refusal (stat err = %v)", serr)
	}
}

// TestDeleteTenantRejectsInvalidID pins that a traversal-y or malformed id is
// rejected by sanitization before any removal — the arg reaches os.RemoveAll only
// as a validated, single-segment tenant id.
func TestDeleteTenantRejectsInvalidID(t *testing.T) {
	dataDir := t.TempDir()
	seedTenant(t, dataDir, "acme")
	for _, bad := range []string{"../acme", "a/b", ""} {
		if err := runDeleteTenant([]string{"--data-dir", dataDir, bad}, noEnv); err == nil {
			t.Errorf("delete of invalid id %q was accepted", bad)
		}
	}
	// The valid neighbor is untouched by any of the rejected attempts.
	if _, err := os.Stat(filepath.Join(dataDir, "acme")); err != nil {
		t.Errorf("acme was disturbed by a rejected delete (stat err = %v)", err)
	}
}

// TestDeleteTenantMissingIsError pins that deleting an absent tenant is a clear
// error, not a silent exit 0 — an operator typo must be visible.
func TestDeleteTenantMissingIsError(t *testing.T) {
	dataDir := t.TempDir()
	seedTenant(t, dataDir, "acme")
	err := runDeleteTenant([]string{"--data-dir", dataDir, "ghost"}, noEnv)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

// TestDeleteTenantResolvesDataDirFromEnv pins that the subcommand honors
// TOISE_DATA_DIR (the cold config-resolution path) the same as --data-dir, so an
// operator running it from the service environment targets the right stores.
func TestDeleteTenantResolvesDataDirFromEnv(t *testing.T) {
	dataDir := t.TempDir()
	seedTenant(t, dataDir, "acme")
	getenv := func(k string) string {
		if k == "TOISE_DATA_DIR" {
			return dataDir
		}
		return ""
	}
	if err := runDeleteTenant([]string{"acme"}, getenv); err != nil {
		t.Fatalf("runDeleteTenant via env: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "acme")); !os.IsNotExist(err) {
		t.Errorf("acme not removed via TOISE_DATA_DIR (stat err = %v)", err)
	}
}

// TestDropSnapshotDiscardsSnapshotKeepsLog pins the corrupt-snapshot recovery
// path: drop-snapshot removes each tenant's persisted projection snapshot while
// leaving the event log — the source of truth — fully intact for the next replay.
func TestDropSnapshotDiscardsSnapshotKeepsLog(t *testing.T) {
	dataDir := t.TempDir()
	// A registry-minted tenant, so drop-snapshot's OpenExistingWritable finds it.
	reg, err := registry.Open(dataDir, store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	st, err := reg.For("acme")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1_700_000_000, 0).UTC()
	ev := model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityCreated,
		Entity: model.Entity{ID: "e1", Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}},
		EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
	if aerr := st.Store.Append(ev); aerr != nil {
		t.Fatal(aerr)
	}
	if serr := st.Store.WriteSnapshot(st.Store.Sequence(), []model.Event{ev}, nil); serr != nil {
		t.Fatal(serr)
	}
	if cerr := reg.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	if rerr := runDropSnapshot([]string{"--data-dir", dataDir}, noEnv); rerr != nil {
		t.Fatalf("runDropSnapshot: %v", rerr)
	}

	reopened, err := store.Open(filepath.Join(dataDir, "acme"), store.DefaultConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, _, _, ok, rerr := reopened.ReadSnapshot(); rerr != nil || ok {
		t.Errorf("snapshot still present after drop (ok=%v err=%v)", ok, rerr)
	}
	g := projection.New()
	if rerr := g.Replay(reopened); rerr != nil {
		t.Fatalf("replay after drop: %v", rerr)
	}
	if g.EntityCount() != 1 {
		t.Errorf("log damaged by drop-snapshot: EntityCount = %d, want 1", g.EntityCount())
	}
}
