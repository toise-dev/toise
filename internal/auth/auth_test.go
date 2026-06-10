package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestDisabledPassesThrough(t *testing.T) {
	a := New([]string{"", "  "}) // blanks are ignored
	if a.Enabled() {
		t.Fatal("no real tokens => disabled")
	}
	rec := httptest.NewRecorder()
	a.HTTPMiddleware(nil)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graphql", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("disabled auth should pass through, got %d", rec.Code)
	}
}

func TestHTTPMiddleware(t *testing.T) {
	a := New([]string{"secret"})
	mw := a.HTTPMiddleware(map[string]bool{"/healthz": true})

	cases := []struct {
		name, path, header string
		want               int
	}{
		{"no token", "/graphql", "", http.StatusUnauthorized},
		{"valid token", "/graphql", "Bearer secret", http.StatusOK},
		{"wrong token", "/graphql", "Bearer nope", http.StatusUnauthorized},
		{"malformed header", "/graphql", "secret", http.StatusUnauthorized},
		{"public path no token", "/healthz", "", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			mw(okHandler()).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestUnaryInterceptor(t *testing.T) {
	a := New([]string{"secret"})
	ui := a.UnaryInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }

	if _, err := ui(context.Background(), nil, nil, handler); err == nil {
		t.Error("missing token should be rejected")
	}
	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer nope"))
	if _, err := ui(bad, nil, nil, handler); err == nil {
		t.Error("wrong token should be rejected")
	}
	good := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer secret"))
	if _, err := ui(good, nil, nil, handler); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
}

// TestTenantScopedTokens pins #104: a scoped token authenticates but is
// authorized only for its tenant — 403 elsewhere — while global tokens stay
// valid for every tenant.
func TestTenantScopedTokens(t *testing.T) {
	a := NewWithTenantTokens([]string{"global-tok"}, map[string][]string{"acme": {"acme-tok"}})
	if !a.Enabled() {
		t.Fatal("scoped tokens must enable auth")
	}

	do := func(token, tenantID string) int {
		req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if tenantID != "" {
			req.Header.Set("X-Scope-OrgID", tenantID)
		}
		rec := httptest.NewRecorder()
		a.HTTPMiddleware(nil)(okHandler()).ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do("acme-tok", "acme"); code != http.StatusOK {
		t.Errorf("scoped token on its tenant = %d, want 200", code)
	}
	if code := do("acme-tok", "globex"); code != http.StatusForbidden {
		t.Errorf("scoped token on another tenant = %d, want 403", code)
	}
	if code := do("acme-tok", ""); code != http.StatusForbidden {
		t.Errorf("scoped token on the default tenant = %d, want 403", code)
	}
	if code := do("global-tok", "acme"); code != http.StatusOK {
		t.Errorf("global token on acme = %d, want 200", code)
	}
	if code := do("global-tok", "globex"); code != http.StatusOK {
		t.Errorf("global token on globex = %d, want 200", code)
	}
	if code := do("wrong", "acme"); code != http.StatusUnauthorized {
		t.Errorf("unknown token = %d, want 401 (authn, not authz)", code)
	}

	// gRPC-side check, including the no-metadata case.
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer acme-tok"))
	if !a.AllowedForTenantGRPC(ctx, "acme") {
		t.Error("scoped token must be allowed for its tenant over gRPC")
	}
	if a.AllowedForTenantGRPC(ctx, "globex") {
		t.Error("scoped token must be refused for another tenant over gRPC")
	}
	if a.AllowedForTenantGRPC(context.Background(), "acme") {
		t.Error("no metadata must be refused when auth is on")
	}
	if !New(nil).AllowedForTenantGRPC(context.Background(), "any") {
		t.Error("disabled auth must allow")
	}
}
