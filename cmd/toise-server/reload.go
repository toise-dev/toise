package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/toise-dev/toise/internal/auth"
	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/metrics"
	"github.com/toise-dev/toise/internal/registry"
)

// tenantSettings is the per-tenant configuration visible to request handling.
// Instances are immutable once stored; readers load the pointer atomically so
// a reload swap is never observed half-applied.
type tenantSettings struct {
	names     map[string]string
	retention map[string]time.Duration
}

type ReloaderDeps struct {
	Load           func() (config.Config, error)
	Initial        config.Config
	Auth           *auth.Authenticator
	Registry       *registry.Registry
	Settings       *atomic.Pointer[tenantSettings]
	OnNamesChanged func()
	Metrics        *metrics.ConfigReloads
	Logger         *slog.Logger
	Now            func() time.Time
}

// Reloader applies SIGHUP-triggered configuration reloads. The mutex
// serializes reloads so observers never interleave two attempts.
type Reloader struct {
	deps    ReloaderDeps
	mu      sync.Mutex
	current config.Config
}

func NewReloader(d ReloaderDeps) *Reloader {
	return &Reloader{deps: d, current: d.Initial}
}

var backtickQuoted = regexp.MustCompile("`[^`]*`")

// redactReloadError masks backtick-quoted scalars: YAML decode errors
// reproduce them verbatim, and a quoted scalar could be a token value.
func redactReloadError(err error) string {
	return backtickQuoted.ReplaceAllString(err.Error(), "<redacted>")
}

// tokensForUnlistedTenants returns the tenant ids for which the pending
// configuration introduces at least one new token while leaving the tenant
// outside a non-empty allowlist. Because the hot reload swaps tokens before
// it swaps the tenant creation policy, such a token could transiently create
// a tenant that neither the running nor the pending configuration permits,
// so the reload must be rejected. A tenant is reported only when all of the
// following hold: the allowlist is non-empty; the tenant is absent from it;
// the tenant is not "default" (always allowed); isOpen reports that the
// tenant is not already open (an open tenant is never created, so its tokens
// are harmless); and next contains at least one token for the tenant that
// prev does not (a token carried over unchanged is not new). next and prev
// map tenant ids to token lists and are the union over the three roles. The
// returned ids are sorted and deduplicated; nil is returned when no tenant
// qualifies. Token values are never included in the result.
func tokensForUnlistedTenants(allowlist []string, isOpen func(string) bool, next, prev map[string][]string) []string {
	if len(allowlist) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, id := range allowlist {
		allowed[id] = true
	}
	var tenants []string
	for tenant, tokens := range next {
		if tenant == "default" || allowed[tenant] {
			continue
		}
		if isOpen != nil && isOpen(tenant) {
			continue
		}
		oldTokens := make(map[string]bool, len(prev[tenant]))
		for _, tok := range prev[tenant] {
			oldTokens[tok] = true
		}
		for _, tok := range tokens {
			if !oldTokens[tok] {
				tenants = append(tenants, tenant)
				break
			}
		}
	}
	// Deduplicate (map keys are unique, but be explicit) and sort.
	seen := make(map[string]bool, len(tenants))
	deduped := tenants[:0]
	for _, id := range tenants {
		if !seen[id] {
			seen[id] = true
			deduped = append(deduped, id)
		}
	}
	for i := 1; i < len(deduped); i++ {
		for j := i; j > 0 && deduped[j] < deduped[j-1]; j-- {
			deduped[j], deduped[j-1] = deduped[j-1], deduped[j]
		}
	}
	if len(deduped) == 0 {
		return nil
	}
	return deduped
}

// unionTokens merges per-role tenant token maps into one tenant -> tokens map.
func unionTokens(maps ...map[string][]string) map[string][]string {
	out := make(map[string][]string)
	for _, m := range maps {
		for id, toks := range m {
			out[id] = append(out[id], toks...)
		}
	}
	return out
}

func (r *Reloader) Reload(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	logger := r.deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := r.deps.Now
	if now == nil {
		now = time.Now
	}
	reject := func(err error) error {
		text := redactReloadError(err)
		logger.Error("config reload rejected", "reason", text)
		if r.deps.Metrics != nil {
			r.deps.Metrics.Record("rejected", now())
		}
		return errors.New("config reload rejected: " + text)
	}

	next, err := r.deps.Load()
	if err != nil {
		return reject(err)
	}
	full, err := next.TenantTokensMap()
	if err != nil {
		return reject(err)
	}
	read, err := next.TenantReadTokensMap()
	if err != nil {
		return reject(err)
	}
	ingest, err := next.TenantIngestTokensMap()
	if err != nil {
		return reject(err)
	}
	names, err := next.TenantNamesMap()
	if err != nil {
		return reject(err)
	}
	retention, err := next.TenantRetentionMap()
	if err != nil {
		return reject(err)
	}
	prevFull, _ := r.current.TenantTokensMap()
	prevRead, _ := r.current.TenantReadTokensMap()
	prevIngest, _ := r.current.TenantIngestTokensMap()
	isOpen := func(id string) bool { _, ok := r.deps.Registry.Peek(id); return ok }
	if ghosts := tokensForUnlistedTenants(next.TenantAllowlist, isOpen,
		unionTokens(full, read, ingest), unionTokens(prevFull, prevRead, prevIngest)); len(ghosts) > 0 {
		return reject(fmt.Errorf("new tokens for tenants missing from tenant_allowlist: %s (add them to the allowlist in the same edit)", strings.Join(ghosts, ", ")))
	}
	// Last fallible step: everything below cannot fail, so a successful
	// reload is never left half-applied.
	if err := r.deps.Auth.ReloadScopedTokens(full, read, ingest); err != nil {
		return reject(err)
	}
	r.deps.Registry.SetTenantPolicy(next.TenantAllowlist, next.MaxTenants)

	prev := r.deps.Settings.Load()
	namesChanged := prev == nil || !reflect.DeepEqual(prev.names, names)
	r.deps.Settings.Store(&tenantSettings{names: names, retention: retention})
	if namesChanged && r.deps.OnNamesChanged != nil {
		r.deps.OnNamesChanged()
	}

	ignored := r.current.IgnoredChanges(next)
	// Old maps come from the pre-reload config; they were validated at boot,
	// so a decode failure here is treated as empty rather than fatal.
	oldFull, _ := r.current.TenantTokensMap()
	oldRead, _ := r.current.TenantReadTokensMap()
	oldIngest, _ := r.current.TenantIngestTokensMap()
	oldNames, _ := r.current.TenantNamesMap()
	oldRetention, _ := r.current.TenantRetentionMap()
	oldAllowlist := r.current.TenantAllowlist

	// Keep only the reloadable fields so a pending non-reloadable change is
	// still reported as ignored on the next reload.
	cur := r.current
	cur.TenantAllowlist = next.TenantAllowlist
	cur.MaxTenants = next.MaxTenants
	cur.TenantNames = next.TenantNames
	cur.TenantRetentionMaxAge = next.TenantRetentionMaxAge
	cur.TenantTokens = next.TenantTokens
	cur.TenantReadTokens = next.TenantReadTokens
	cur.TenantIngestTokens = next.TenantIngestTokens
	r.current = cur

	for _, key := range ignored {
		logger.Warn("config key changed: requires a restart; not applied", "key", key)
	}

	added, removed := allowlistDiff(oldAllowlist, next.TenantAllowlist)
	logger.Info("config reloaded",
		"allowlist_added", added,
		"allowlist_removed", removed,
		"names_changed", mapDiff(oldNames, names),
		"retention_changed", mapDiff(oldRetention, retention),
		"tokens_full", tokenCountDiff(oldFull, full),
		"tokens_read", tokenCountDiff(oldRead, read),
		"tokens_ingest", tokenCountDiff(oldIngest, ingest),
		"ignored_keys", ignored,
	)

	outcome := "applied"
	if len(ignored) > 0 {
		outcome = "partial"
	}
	if r.deps.Metrics != nil {
		r.deps.Metrics.Record(outcome, now())
	}
	return nil
}

// WatchSIGHUP registers the signal handler synchronously before returning, so
// a SIGHUP arriving immediately after the call cannot be missed.
func (r *Reloader) WatchSIGHUP(ctx context.Context, wg *sync.WaitGroup) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer signal.Stop(ch)
		for {
			select {
			case <-ch:
				if err := r.Reload(ctx); err != nil {
					slog.Debug("config reload triggered by SIGHUP did not apply", "reason", err.Error())
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func allowlistDiff(oldList, newList []string) (added, removed []string) {
	oldSet := make(map[string]struct{}, len(oldList))
	for _, id := range oldList {
		oldSet[id] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newList))
	for _, id := range newList {
		newSet[id] = struct{}{}
	}
	for id := range newSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range oldSet {
		if _, ok := newSet[id]; !ok {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func mapDiff[V comparable](oldMap, newMap map[string]V) []string {
	var changed []string
	for id, v := range newMap {
		if ov, ok := oldMap[id]; !ok || ov != v {
			changed = append(changed, id)
		}
	}
	for id := range oldMap {
		if _, ok := newMap[id]; !ok {
			changed = append(changed, id)
		}
	}
	sort.Strings(changed)
	return changed
}

// tokenCountDiff reports per-tenant token counts only; token values must
// never reach a log line.
func tokenCountDiff(oldMap, newMap map[string][]string) []string {
	ids := make(map[string]struct{}, len(oldMap)+len(newMap))
	for id := range oldMap {
		ids[id] = struct{}{}
	}
	for id := range newMap {
		ids[id] = struct{}{}
	}
	var entries []string
	for id := range ids {
		before := len(oldMap[id])
		after := len(newMap[id])
		if before != after {
			entries = append(entries, id+"="+strconv.Itoa(before)+"->"+strconv.Itoa(after))
		}
	}
	sort.Strings(entries)
	return entries
}
