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

// Limits bounds runtime tenant creation (#115). Tenants whose directory
// already exists always open; the limits apply only to minting new ones.
type Limits struct {
	// AutoCreate allows a first write to a new tenant id to create its stack.
	// Off, only pre-existing tenants (and the default) are served.
	AutoCreate bool
	// Allowlist, when non-empty, is the only set of new tenant ids that may be
	// created (the default tenant is always allowed).
	Allowlist []string
	// MaxTenants, when > 0, caps the number of open stacks; creation beyond it
	// is refused until tenants are removed.
	MaxTenants int
}

func (l Limits) allows(id string, open int) error {
	if id == tenant.Default {
		return nil
	}
	if !l.AutoCreate {
		return fmt.Errorf("%w: %q does not exist and tenant auto-creation is disabled", tenant.ErrNotAllowed, id)
	}
	if len(l.Allowlist) > 0 {
		found := false
		for _, a := range l.Allowlist {
			if a == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %q is not on the tenant allowlist", tenant.ErrNotAllowed, id)
		}
	}
	if l.MaxTenants > 0 && open >= l.MaxTenants {
		return fmt.Errorf("%w: tenant cap (%d) reached", tenant.ErrNotAllowed, l.MaxTenants)
	}
	return nil
}

// Registry lazily opens and caches one Stack per tenant under a shared data dir.
// It is safe for concurrent use.
type Registry struct {
	dataDir  string
	storeCfg store.Config
	relBuf   time.Duration
	limits   Limits
	logger   *slog.Logger

	// quarantined lists tenants whose store failed to open at boot; set once
	// during Open, read-only afterwards.
	quarantined []string

	mu      sync.Mutex
	stacks  map[string]*Stack
	opening map[string]*inflight
}

// inflight is a singleflight slot: the first caller opens the stack outside the
// registry mutex, later callers wait on done.
type inflight struct {
	done chan struct{}
	s    *Stack
	err  error
}

// Open creates a registry over dataDir. It first migrates a legacy single-tenant
// data dir (a Pebble store written directly under dataDir by an older single-graph
// build) into dataDir/<default>/, then opens every existing tenant subdirectory
// plus the default tenant — so the liveness sweep, compaction and metrics cover
// persisted tenants from boot rather than only after a tenant's first request.
func Open(dataDir string, storeCfg store.Config, relBuf time.Duration, logger *slog.Logger) (*Registry, error) {
	return OpenWithLimits(dataDir, storeCfg, relBuf, Limits{AutoCreate: true}, logger)
}

// OpenWithLimits is Open with runtime tenant-creation bounds (#115).
func OpenWithLimits(dataDir string, storeCfg store.Config, relBuf time.Duration, limits Limits, logger *slog.Logger) (*Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data dir %s: %w", dataDir, err)
	}
	if err := migrateLegacy(dataDir, logger); err != nil {
		return nil, err
	}
	r := &Registry{dataDir: dataDir, storeCfg: storeCfg, relBuf: relBuf, limits: limits, logger: logger,
		stacks: make(map[string]*Stack), opening: make(map[string]*inflight)}

	// Boot opens what already exists (plus the default) regardless of limits:
	// the limits bound runtime minting, never previously persisted tenants.
	existing, skipped, err := tenantDirs(dataDir)
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		// A directory that does not sanitize as a tenant id is never opened —
		// an ops accident (a hidden or renamed store) would otherwise vanish
		// silently from the registry forever (#144).
		logger.Warn("skipping non-tenant directories in the data dir", "data_dir", dataDir, "skipped", skipped)
	}
	for _, id := range existing {
		if _, err := r.ensure(id); err != nil {
			// Quarantine, do not abort: one tenant's unreadable store (corrupt
			// snapshot's own log, half-written pebble, bad perms) must not take
			// the whole multi-tenant process down with it. Warn, skip, count, and
			// keep its directory on disk for manual recovery; the healthy tenants
			// still come up. The default tenant below is the one exception.
			logger.Warn("quarantining tenant: its store failed to open at boot (left on disk for recovery)",
				"tenant", id, "err", err)
			r.quarantined = append(r.quarantined, id)
			continue
		}
	}
	// A default stack always exists, so a single-tenant deployment that never sets
	// X-Scope-OrgID behaves exactly as a single-graph build did. Its failure is
	// fatal — there is no degraded mode without it.
	if _, err := r.ensure(tenant.Default); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

// Quarantined returns the ids of tenants whose store failed to open at boot and
// were skipped rather than aborting the process. Their directories are left on
// disk for recovery. Stable after Open returns.
func (r *Registry) Quarantined() []string {
	out := make([]string, len(r.quarantined))
	copy(out, r.quarantined)
	return out
}

// TenantStore pairs a tenant id with its event store, for the read-only path.
type TenantStore struct {
	Tenant string
	Store  *store.Store
}

// OpenExisting opens, read-only, the event store of every tenant already
// persisted under dataDir — the no-mint counterpart to Open, for offline
// tooling such as the checkpoint subcommand. The registry is the only place
// that mints tenant stores, so the guarantee that this path creates nothing
// lives here too: a missing data dir, an unmigrated legacy layout, or a dir
// holding no tenant stores are errors rather than an empty registry, and
// each store is opened with store.OpenReadOnly so even the format stamp is
// skipped. Callers own closing the returned stores.
func OpenExisting(dataDir string, storeCfg store.Config, logger *slog.Logger) ([]TenantStore, error) {
	return openExistingStores(dataDir, storeCfg, logger, true)
}

// OpenExistingWritable is OpenExisting but opens the stores read-write, for cold
// maintenance tools that must mutate them (the drop-snapshot subcommand). Same
// lock semantics: run with the server stopped, as a running server holds the
// pebble lock and the open fails cleanly.
func OpenExistingWritable(dataDir string, storeCfg store.Config, logger *slog.Logger) ([]TenantStore, error) {
	return openExistingStores(dataDir, storeCfg, logger, false)
}

func openExistingStores(dataDir string, storeCfg store.Config, logger *slog.Logger, readOnly bool) ([]TenantStore, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := os.Stat(dataDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("data dir %s does not exist", dataDir)
		}
		return nil, fmt.Errorf("checking data dir %s: %w", dataDir, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, legacyMarker)); err == nil {
		return nil, fmt.Errorf("data dir %s holds a legacy single-tenant store: start toise-server against it once to migrate to the per-tenant layout", dataDir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking for legacy data layout: %w", err)
	}
	existing, skipped, err := tenantDirs(dataDir)
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		logger.Warn("skipping non-tenant directories in the data dir", "data_dir", dataDir, "skipped", skipped)
	}
	if len(existing) == 0 {
		return nil, fmt.Errorf("no tenant stores found in %s", dataDir)
	}
	open := store.OpenReadOnly
	if !readOnly {
		open = store.Open
	}
	out := make([]TenantStore, 0, len(existing))
	for _, id := range existing {
		st, oerr := open(filepath.Join(dataDir, id), storeCfg)
		if oerr != nil {
			for _, ts := range out {
				_ = ts.Store.Close()
			}
			return nil, fmt.Errorf("opening event log for tenant %q (is toise-server still running?): %w", id, oerr)
		}
		out = append(out, TenantStore{Tenant: id, Store: st})
	}
	return out, nil
}

// For returns the stack for a (raw) tenant id, opening and caching it on first
// use. The id is sanitized to a safe single path segment; an id that cannot be
// sanitized is rejected rather than silently coerced. Creating a NEW tenant is
// subject to the registry's Limits; a tenant whose directory already exists
// (e.g. restored from a backup after boot) always opens.
func (r *Registry) For(rawTenant string) (*Stack, error) {
	id, ok := tenant.Sanitize(rawTenant)
	if !ok {
		return nil, fmt.Errorf("invalid tenant id %q", rawTenant)
	}
	r.mu.Lock()
	if s, ok := r.stacks[id]; ok {
		r.mu.Unlock()
		return s, nil
	}
	if _, derr := os.Stat(filepath.Join(r.dataDir, id)); derr != nil {
		// The directory does not exist: this call would mint a tenant.
		if lerr := r.limits.allows(id, len(r.stacks)+len(r.opening)); lerr != nil {
			r.mu.Unlock()
			return nil, lerr
		}
	}
	r.mu.Unlock()
	return r.ensure(id)
}

// Peek returns the stack only if it is already open. Query surfaces use it so
// reading an unknown tenant can never mint one (#115): before this, a GET with
// a ghost X-Scope-OrgID lazily created a store directory.
func (r *Registry) Peek(rawTenant string) (*Stack, bool) {
	id, ok := tenant.Sanitize(rawTenant)
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.stacks[id]
	return s, ok
}

// ensure opens and caches id's stack without applying Limits (the boot and
// already-exists paths). The pebble open runs OUTSIDE the registry mutex —
// opening one tenant must not block every other tenant's requests (#115) —
// with a singleflight slot so concurrent callers open it once.
func (r *Registry) ensure(id string) (*Stack, error) {
	r.mu.Lock()
	if s, ok := r.stacks[id]; ok {
		r.mu.Unlock()
		return s, nil
	}
	if fl, ok := r.opening[id]; ok {
		r.mu.Unlock()
		<-fl.done
		return fl.s, fl.err
	}
	fl := &inflight{done: make(chan struct{})}
	r.opening[id] = fl
	r.mu.Unlock()

	s, err := r.openStack(id)

	r.mu.Lock()
	if err == nil {
		r.stacks[id] = s
	}
	delete(r.opening, id)
	r.mu.Unlock()
	fl.s, fl.err = s, err
	close(fl.done)
	return s, err
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
	var liveness []byte
	if seq, snapEvents, blob, ok, rerr := st.ReadSnapshot(); rerr != nil {
		// A corrupt/unreadable snapshot must not block boot: the log is the
		// source of truth, so fall back to a full replay from the start instead
		// of failing. Clear it with `toise-server drop-snapshot` to stop the
		// warning and let a fresh snapshot be written.
		r.logger.Warn("ignoring unreadable projection snapshot; falling back to full replay",
			"tenant", id, "err", rerr)
	} else if ok {
		for i := range snapEvents {
			graph.Apply(snapEvents[i])
		}
		restoredFrom = seq
		liveness = blob
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
	// Restore the liveness Memento (#139): absolute deadlines, so a producer
	// that died during the downtime is swept on the first tick after boot.
	if err := engine.RestoreLiveness(liveness); err != nil {
		// A corrupt liveness section must not block boot: the projection is
		// intact, only the backstop re-arms lazily on the next observations.
		r.logger.Warn("ignoring unreadable liveness snapshot section", "tenant", id, "err", err)
	}
	r.logger.Info("tenant stack ready", "tenant", id,
		"entities", graph.EntityCount(), "relations", graph.RelationCount(),
		"from_snapshot_seq", restoredFrom)
	return &Stack{Tenant: id, Store: st, Graph: graph, Engine: engine}, nil
}

// tenantDirs lists the tenant subdirectories under dataDir — directories whose
// name is already a valid, canonical tenant id. Hidden dirs and anything else are
// skipped.
func tenantDirs(dataDir string) (valid, skipped []string, err error) {
	ents, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("listing data dir %s: %w", dataDir, err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if id, ok := tenant.Sanitize(e.Name()); ok && id == e.Name() {
			out = append(out, e.Name())
		} else {
			skipped = append(skipped, e.Name())
		}
	}
	return out, skipped, nil
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
