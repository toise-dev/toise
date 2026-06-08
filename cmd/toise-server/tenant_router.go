package main

import (
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
	reg   *registry.Registry
	build func(*registry.Stack) (http.Handler, error)

	mu       sync.Mutex
	handlers map[string]http.Handler
}

func newTenantRouter(reg *registry.Registry, build func(*registry.Stack) (http.Handler, error)) *tenantRouter {
	return &tenantRouter{reg: reg, build: build, handlers: make(map[string]http.Handler)}
}

func (tr *tenantRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, ok := tenant.FromHTTP(r)
	if !ok {
		http.Error(w, "invalid "+tenant.HeaderOrgID+" header", http.StatusBadRequest)
		return
	}
	h, err := tr.handlerFor(id)
	if err != nil {
		http.Error(w, "resolving tenant: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.ServeHTTP(w, r)
}

func (tr *tenantRouter) handlerFor(id string) (http.Handler, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if h, ok := tr.handlers[id]; ok {
		return h, nil
	}
	st, err := tr.reg.For(id)
	if err != nil {
		return nil, err
	}
	h, err := tr.build(st)
	if err != nil {
		return nil, err
	}
	tr.handlers[id] = h
	return h, nil
}
