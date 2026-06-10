package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	tr := newTenantRouter(reg, nil, func(st *registry.Stack) (http.Handler, error) {
		built++
		name := st.Tenant
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, name)
		}), nil
	})

	if rec := get(t, tr, ""); rec.Body.String() != tenant.Default {
		t.Errorf("no header: served %q, want %q", rec.Body.String(), tenant.Default)
	}
	// acme exists because ingest (here: the registry directly) created it; the
	// router itself never creates tenants.
	if _, err := reg.For("acme"); err != nil {
		t.Fatal(err)
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
	tr := newTenantRouter(reg, nil, func(*registry.Stack) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})

	if rec := get(t, tr, "../escape"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid header: status = %d, want 400", rec.Code)
	}
}

// TestTenantRouterErrorIsGeneric pins #115: a handler-build failure logs
// server-side and returns a generic message — internal detail (paths, store
// errors) must not leak to the client.
func TestTenantRouterErrorIsGeneric(t *testing.T) {
	reg, err := registry.Open(t.TempDir(), store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	tr := newTenantRouter(reg, slog.New(slog.DiscardHandler), func(*registry.Stack) (http.Handler, error) {
		return nil, fmt.Errorf("secret internal detail: /var/lib/toise/data is on fire")
	})
	rec := httptest.NewRecorder()
	tr.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graphql", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "secret internal detail") {
		t.Errorf("internal error detail leaked to the client: %q", body)
	}
}

// TestTenantRouterDoesNotMintTenants pins #115: reading an unknown tenant is a
// 404 and must NOT create its store directory — before this, any GET with a
// ghost X-Scope-OrgID lazily minted a tenant on disk.
func TestTenantRouterDoesNotMintTenants(t *testing.T) {
	dir := t.TempDir()
	reg, err := registry.Open(dir, store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	tr := newTenantRouter(reg, nil, func(*registry.Stack) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})

	rec := get(t, tr, "ghost")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tenant status = %d, want 404", rec.Code)
	}
	if _, serr := os.Stat(filepath.Join(dir, "ghost")); !os.IsNotExist(serr) {
		t.Errorf("reading tenant ghost created its directory (stat err = %v)", serr)
	}
}
