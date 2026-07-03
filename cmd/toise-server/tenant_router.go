package main

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/tenant"
)

// tenantRouter dispatches each HTTP request to a per-tenant http.Handler, building
// and caching one handler per tenant on first use. The tenant is read from the
// X-Scope-OrgID header (default when absent); an invalid value is rejected with
// 400. Routing at the transport boundary keeps each tenant's GraphQL/MCP/debug-UI
// bound to its own isolated stack (ADR 0025, #95) without touching those handlers.
type tenantRouter struct {
	reg    *registry.Registry
	build  func(*registry.Stack) (http.Handler, error)
	logger *slog.Logger
	// resolve decides a request's tenant. Defaults to tenant.FromHTTP (the
	// X-Scope-OrgID header); the server swaps in auth.EffectiveTenantHTTP under
	// derive-only so a scoped token routes to its own tenant (ADR 0028).
	resolve func(*http.Request) (string, bool)

	mu       sync.Mutex
	handlers map[string]http.Handler
}

func newTenantRouter(reg *registry.Registry, logger *slog.Logger, build func(*registry.Stack) (http.Handler, error)) *tenantRouter {
	if logger == nil {
		logger = slog.Default()
	}
	return &tenantRouter{reg: reg, build: build, logger: logger, resolve: tenant.FromHTTP, handlers: make(map[string]http.Handler)}
}

func (tr *tenantRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, ok := tr.resolve(r)
	if !ok {
		http.Error(w, "invalid "+tenant.HeaderOrgID+" header", http.StatusBadRequest)
		return
	}
	h, ok, err := tr.handlerFor(id)
	if err != nil {
		// Log the cause server-side; the client gets a generic message — the
		// error detail (paths, store internals) is ours, not theirs (#115).
		tr.logger.Error("resolving tenant handler", "tenant", id, "err", err)
		http.Error(w, "internal error resolving tenant", http.StatusInternalServerError)
		return
	}
	if !ok {
		// Reading must never mint a tenant (#115): an id with no open stack is
		// a 404, exactly like an unknown resource.
		http.Error(w, "unknown tenant", http.StatusNotFound)
		return
	}
	// Stamp the resolved tenant into the context so downstream consumers agree
	// with the stack we routed to. The audit log reads it (ADR 0028): without
	// this, tenant.FromContext falls back to the default and every operator write
	// — including a derive-only scoped token's — is misattributed to "default".
	h.ServeHTTP(w, r.WithContext(tenant.NewContext(r.Context(), id)))
}

func (tr *tenantRouter) handlerFor(id string) (http.Handler, bool, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if h, ok := tr.handlers[id]; ok {
		return h, true, nil
	}
	// Peek, never For: the query surfaces read tenants, they do not create
	// them — ingest (or boot) is what brings a tenant into existence.
	st, ok := tr.reg.Peek(id)
	if !ok {
		return nil, false, nil
	}
	h, err := tr.build(st)
	if err != nil {
		return nil, false, err
	}
	tr.handlers[id] = h
	return h, true, nil
}
