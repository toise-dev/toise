package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Open(t.TempDir(), store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func get(t *testing.T, h http.Handler, orgID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if orgID != "" {
		req.Header.Set(tenant.HeaderOrgID, orgID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTenantRouterDispatchesByHeader(t *testing.T) {
	reg := newTestRegistry(t)
	var built int
	tr := newTenantRouter(reg, func(st *registry.Stack) (http.Handler, error) {
		built++
		name := st.Tenant
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		}), nil
	})

	if rec := get(t, tr, ""); rec.Body.String() != tenant.Default {
		t.Errorf("no header: served %q, want %q", rec.Body.String(), tenant.Default)
	}
	if rec := get(t, tr, "acme"); rec.Body.String() != "acme" {
		t.Errorf("acme header: served %q, want acme", rec.Body.String())
	}

	// A second acme request reuses the cached handler.
	before := built
	if rec := get(t, tr, "acme"); rec.Body.String() != "acme" {
		t.Errorf("acme header (cached): served %q, want acme", rec.Body.String())
	}
	if built != before {
		t.Errorf("handler rebuilt for a cached tenant: built went %d -> %d", before, built)
	}
}

func TestTenantRouterRejectsInvalidHeader(t *testing.T) {
	reg := newTestRegistry(t)
	tr := newTenantRouter(reg, func(*registry.Stack) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})

	if rec := get(t, tr, "../escape"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid header: status = %d, want 400", rec.Code)
	}
}
