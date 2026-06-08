// Package registry holds one independent {store, projection, change-engine} stack
// per tenant, so a single Toise process serves multiple tenants with physically
// isolated graphs (ADR 0025, #95). Each tenant's data lives under
// <data-dir>/<tenant>/; stacks are opened lazily on first use and cached, and the
// existing single-stack store/projection/engine are unchanged — multi-tenancy is a
// matter of composition at the server boundary.
package registry

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

// Stack is one tenant's isolated graph: its event store, the in-memory projection
// rebuilt from it, and the change engine that appends to it.
type Stack struct {
	Tenant string
	Store  *store.Store
	Graph  *projection.Graph
	Engine *change.Engine
}

// Registry lazily opens and caches one Stack per tenant under a shared data dir.
// It is safe for concurrent use.
type Registry struct {
	dataDir  string
	storeCfg store.Config
	relBuf   time.Duration
	logger   *slog.Logger

	mu     sync.Mutex
	stacks map[string]*Stack
}

// Open creates a registry over dataDir. It first migrates a legacy single-tenant
// data dir (a Pebble store written directly under dataDir by an older single-graph
// build) into dataDir/<default>/, then opens every existing tenant subdirectory
// plus the default tenant — so the liveness sweep, compaction and metrics cover
// persisted tenants from boot rather than only after a tenant's first request.
func Open(dataDir string, storeCfg store.Config, relBuf time.Duration, logger *slog.Logger) (*Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}
	if err := migrateLegacy(dataDir, logger); err != nil {
		return nil, err
	}
	r := &Registry{dataDir: dataDir, storeCfg: storeCfg, relBuf: relBuf, logger: logger, stacks: make(map[string]*Stack)}

	existing, err := tenantDirs(dataDir)
	if err != nil {
		return nil, err
	}
	for _, id := range existing {
		if _, err := r.For(id); err != nil {
			_ = r.Close()
			return nil, err
		}
	}
	// A default stack always exists, so a single-tenant deployment that never sets
	// X-Scope-OrgID behaves exactly as a single-graph build did.
	if _, err := r.For(tenant.Default); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

// For returns the stack for a (raw) tenant id, opening and caching it on first
// use. The id is sanitized to a safe single path segment; an id that cannot be
// sanitized is rejected rather than silently coerced.
func (r *Registry) For(rawTenant string) (*Stack, error) {
	id, ok := tenant.Sanitize(rawTenant)
	if !ok {
		return nil, fmt.Errorf("invalid tenant id %q", rawTenant)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.stacks[id]; ok {
		return s, nil
	}
	s, err := r.openStack(id)
	if err != nil {
		return nil, err
	}
	r.stacks[id] = s
	return s, nil
}

// Stacks returns the currently-open stacks, sorted by tenant id, for the
// background goroutines (sweep, compaction, snapshot) and aggregate metrics.
func (r *Registry) Stacks() []*Stack {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Stack, 0, len(r.stacks))
	for _, s := range r.stacks {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tenant < out[j].Tenant })
	return out
}

// Close closes every open stack's store, returning the first error.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for _, s := range r.stacks {
		if err := s.Store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// openStack opens one tenant's store, rebuilds its projection from the snapshot
// plus the event tail, and wires the change engine — the same sequence a
// single-stack build runs, scoped to <data-dir>/<id>/.
func (r *Registry) openStack(id string) (*Stack, error) {
	dir := filepath.Join(r.dataDir, id)
	st, err := store.Open(dir, r.storeCfg)
	if err != nil {
		return nil, fmt.Errorf("opening event log for tenant %q: %w", id, err)
	}
	graph := projection.New()
	restoredFrom := uint64(0)
	if seq, snapEvents, ok, rerr := st.ReadSnapshot(); rerr != nil {
		_ = st.Close()
		return nil, fmt.Errorf("reading snapshot for tenant %q: %w", id, rerr)
	} else if ok {
		for i := range snapEvents {
			graph.Apply(snapEvents[i])
		}
		restoredFrom = seq
	}
	if err := st.ScanFrom(restoredFrom, func(_ uint64, ev model.Event) error {
		graph.Apply(ev)
		return nil
	}); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("replaying event tail for tenant %q: %w", id, err)
	}
	engine := change.New(graph, st,
		change.WithLogger(r.logger.With("tenant", id)),
		change.WithRelationBuffer(r.relBuf))
	r.logger.Info("tenant stack ready", "tenant", id,
		"entities", graph.EntityCount(), "relations", graph.RelationCount(),
		"from_snapshot_seq", restoredFrom)
	return &Stack{Tenant: id, Store: st, Graph: graph, Engine: engine}, nil
}

// tenantDirs lists the tenant subdirectories under dataDir — directories whose
// name is already a valid, canonical tenant id. Hidden dirs and anything else are
// skipped.
func tenantDirs(dataDir string) ([]string, error) {
	ents, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("listing data dir %s: %w", dataDir, err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if id, ok := tenant.Sanitize(e.Name()); ok && id == e.Name() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// legacyMarker is the file Pebble keeps at the root of a store directory; its
// presence directly under dataDir means an older single-tenant build wrote the
// graph there, so we relocate the whole store under the default tenant.
const legacyMarker = "CURRENT"

func migrateLegacy(dataDir string, logger *slog.Logger) error {
	if _, err := os.Stat(filepath.Join(dataDir, legacyMarker)); err != nil {
		if os.IsNotExist(err) {
			return nil // fresh, or already a per-tenant layout
		}
		return fmt.Errorf("checking for legacy data layout: %w", err)
	}
	dst := filepath.Join(dataDir, tenant.Default)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("cannot migrate legacy data dir: %s already exists alongside a root Pebble store", dst)
	}
	// Move every root entry into a staging dir, then rename it to default in one
	// step — so the freshly-created default is never moved into itself, and a crash
	// mid-move leaves either the old layout or the staging dir, never a half-move.
	tmp, err := os.MkdirTemp(dataDir, ".migrate-")
	if err != nil {
		return fmt.Errorf("creating migration staging dir: %w", err)
	}
	ents, err := os.ReadDir(dataDir)
	if err != nil {
		return fmt.Errorf("listing data dir for migration: %w", err)
	}
	for _, e := range ents {
		if e.Name() == filepath.Base(tmp) {
			continue
		}
		if err := os.Rename(filepath.Join(dataDir, e.Name()), filepath.Join(tmp, e.Name())); err != nil {
			return fmt.Errorf("relocating %s during migration: %w", e.Name(), err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("finalizing migration to %s: %w", dst, err)
	}
	logger.Info("migrated legacy single-tenant data dir to default tenant", "data_dir", dataDir, "tenant", tenant.Default)
	return nil
}
