package auth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/toise-dev/toise/internal/tenant"
)

func TestTokensHashedAtRest(t *testing.T) {
	const full, reader, scopedTok = "super-secret-token", "reader-token", "acme-token"
	a := NewWithRoles([]string{full}, []string{reader}, nil, map[string][]string{"acme": {scopedTok}})

	// Configured tokens still authenticate (behavior preserved).
	if !a.valid(full, surfaceRead) || !a.valid(reader, surfaceRead) || !a.valid(scopedTok, surfaceRead) {
		t.Fatal("configured tokens must still authenticate after hashing at rest")
	}

	// Every stored entry is a 32-byte SHA-256, never the plaintext token.
	plaintext := map[string]bool{full: true, reader: true, scopedTok: true}
	check := func(sets ...[][]byte) {
		for _, set := range sets {
			for _, b := range set {
				if len(b) != sha256.Size {
					t.Errorf("stored token is %d bytes, want a %d-byte hash", len(b), sha256.Size)
				}
				if plaintext[string(b)] {
					t.Error("a plaintext token was retained at rest")
				}
			}
		}
	}
	check(a.tokens, a.readTokens)
	for _, toks := range a.scoped {
		check(toks)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestDeriveOnlyTenancy(t *testing.T) {
	scoped := map[string][]string{"acme": {"acme-tok"}}

	// TenantForBearer derives a scoped token's tenant, never a global/unknown one.
	a := NewWithRoles([]string{"op"}, nil, nil, scoped)
	if id, ok := a.TenantForBearer("Bearer acme-tok"); !ok || id != "acme" {
		t.Errorf("scoped derive = %q,%v; want acme,true", id, ok)
	}
	if _, ok := a.TenantForBearer("Bearer op"); ok {
		t.Error("a global/operator token must not derive a tenant")
	}
	if _, ok := a.TenantForBearer("Bearer nope"); ok {
		t.Error("an unknown token must not derive a tenant")
	}

	// EffectiveTenantHTTP: trust-header honors the client header; derive-only
	// ignores it for a scoped token and uses the binding.
	reqScoped := func(org string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/graphql", nil)
		r.Header.Set("Authorization", "Bearer acme-tok")
		r.Header.Set(tenant.HeaderOrgID, org)
		return r
	}
	if id, _ := a.EffectiveTenantHTTP(reqScoped("evil")); id != "evil" {
		t.Errorf("trust-header effective tenant = %q, want the header 'evil'", id)
	}
	a.SetTenantTrustMode(true)
	if id, _ := a.EffectiveTenantHTTP(reqScoped("evil")); id != "acme" {
		t.Errorf("derive-only effective tenant = %q, want derived 'acme' (header ignored)", id)
	}
	// A global/operator token still selects the tenant by header in derive-only.
	rOp := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	rOp.Header.Set("Authorization", "Bearer op")
	rOp.Header.Set(tenant.HeaderOrgID, "other")
	if id, _ := a.EffectiveTenantHTTP(rOp); id != "other" {
		t.Errorf("derive-only operator token = %q, want header 'other' (cross-tenant)", id)
	}

	// EffectiveTenantGRPC: derive-only locks a scoped token's tenant.
	md := metadata.Pairs("authorization", "Bearer acme-tok", tenant.HeaderOrgID, "evil")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if id, locked, ok := a.EffectiveTenantGRPC(ctx); !ok || !locked || id != "acme" {
		t.Errorf("derive-only gRPC = %q,locked=%v,ok=%v; want acme,true,true", id, locked, ok)
	}

	// Anti-spoofing through the middleware: a scoped token presenting a spoofed
	// X-Scope-OrgID for a tenant it is not bound to is forbidden under
	// trust-header but routed to its own tenant (200) under derive-only.
	spoof := func(deriveOnly bool) int {
		x := NewWithRoles(nil, nil, nil, scoped)
		x.SetTenantTrustMode(deriveOnly)
		r := httptest.NewRequest(http.MethodGet, "/graphql", nil)
		r.Header.Set("Authorization", "Bearer acme-tok")
		r.Header.Set(tenant.HeaderOrgID, "victim")
		rec := httptest.NewRecorder()
		x.HTTPMiddleware(nil)(okHandler()).ServeHTTP(rec, r)
		return rec.Code
	}
	if code := spoof(false); code != http.StatusForbidden {
		t.Errorf("trust-header: spoofed header should be 403, got %d", code)
	}
	if code := spoof(true); code != http.StatusOK {
		t.Errorf("derive-only: spoofed header ignored, should be 200, got %d", code)
	}
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

// TestTokenRoles pins the 0.7.0 token roles: a read-only token works on the HTTP
// read surface but not on OTLP ingest; an ingest-only token the reverse; a full
// token on both.
func TestTokenRoles(t *testing.T) {
	a := NewWithRoles([]string{"full"}, []string{"reader"}, []string{"writer"}, nil)

	readOK := func(token string) bool {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		a.HTTPMiddleware(nil)(okHandler()).ServeHTTP(rec, req)
		return rec.Code == http.StatusOK
	}
	ui := a.UnaryInterceptor()
	ingestOK := func(token string) bool {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
		_, err := ui(ctx, nil, nil, func(context.Context, any) (any, error) { return "ok", nil })
		return err == nil
	}

	cases := []struct {
		token            string
		wantRead, wantIn bool
	}{
		{"full", true, true},
		{"reader", true, false},
		{"writer", false, true},
		{"nope", false, false},
	}
	for _, c := range cases {
		if got := readOK(c.token); got != c.wantRead {
			t.Errorf("%s on read surface = %v, want %v", c.token, got, c.wantRead)
		}
		if got := ingestOK(c.token); got != c.wantIn {
			t.Errorf("%s on ingest surface = %v, want %v", c.token, got, c.wantIn)
		}
	}
}

// TestCanWrite pins the 0.7.0 write capability the middleware tags onto the
// request context (annotate_entity gate): a full or tenant-scoped token may
// write, a read-only token may not, and with auth disabled every caller may.
func TestCanWrite(t *testing.T) {
	probe := func(a *Authenticator, token, tenantID string) (writable, served bool) {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writable = CanWrite(r.Context())
			served = true
			w.WriteHeader(http.StatusOK)
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if tenantID != "" {
			req.Header.Set("X-Scope-OrgID", tenantID)
		}
		a.HTTPMiddleware(nil)(next).ServeHTTP(rec, req)
		return writable, served
	}

	withRoles := NewWithRoles([]string{"full"}, []string{"reader"}, nil, map[string][]string{"acme": {"scoped"}})
	if w, served := probe(withRoles, "full", ""); !served || !w {
		t.Errorf("full token: writable=%v served=%v, want writable served", w, served)
	}
	if w, served := probe(withRoles, "scoped", "acme"); !served || !w {
		t.Errorf("scoped token: writable=%v served=%v, want writable served", w, served)
	}
	if w, served := probe(withRoles, "reader", ""); !served || w {
		t.Errorf("read-only token: writable=%v served=%v, want NOT writable but served", w, served)
	}

	// Auth disabled: no decision recorded, so every caller may write.
	if CanWrite(context.Background()) != true {
		t.Error("absent decision must default to writable (auth disabled)")
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

func TestPerTenantRBAC(t *testing.T) {
	a := NewWithRoles(nil, nil, nil, map[string][]string{"acme": {"acme-full"}}).
		WithScopedRoleTokens(
			map[string][]string{"acme": {"acme-read"}},
			map[string][]string{"acme": {"acme-ingest"}},
		)

	// Read-only scoped token: reads its tenant only, never ingests, never another tenant, never writes.
	if !a.allowedForTenant("acme-read", "acme", surfaceRead) {
		t.Error("read-scoped token must be allowed to read its tenant")
	}
	if a.allowedForTenant("acme-read", "acme", surfaceIngest) {
		t.Error("read-scoped token must NOT ingest")
	}
	if a.allowedForTenant("acme-read", "other", surfaceRead) {
		t.Error("read-scoped token must NOT touch another tenant")
	}
	if a.canWriteHeader("Bearer acme-read") {
		t.Error("read-scoped token must not be write-capable")
	}

	// Ingest-only scoped token: ingests its tenant only, never reads.
	if !a.allowedForTenant("acme-ingest", "acme", surfaceIngest) {
		t.Error("ingest-scoped token must be allowed to ingest its tenant")
	}
	if a.allowedForTenant("acme-ingest", "acme", surfaceRead) {
		t.Error("ingest-scoped token must NOT read")
	}

	// Full scoped token: both surfaces of its tenant, and write-capable.
	if !a.allowedForTenant("acme-full", "acme", surfaceRead) || !a.allowedForTenant("acme-full", "acme", surfaceIngest) {
		t.Error("full scoped token must read and ingest its tenant")
	}
	if !a.canWriteHeader("Bearer acme-full") {
		t.Error("full scoped token must be write-capable")
	}

	// derive-only: a scoped token of any role derives its tenant.
	a.SetTenantTrustMode(true)
	for _, tok := range []string{"acme-read", "acme-ingest", "acme-full"} {
		if id, ok := a.TenantForBearer("Bearer " + tok); !ok || id != "acme" {
			t.Errorf("TenantForBearer(%s) = %q,%v; want acme,true", tok, id, ok)
		}
	}

	// A role-scoped-only configuration still enforces auth.
	if !NewWithRoles(nil, nil, nil, nil).WithScopedRoleTokens(map[string][]string{"acme": {"r"}}, nil).Enabled() {
		t.Error("a role-scoped-only config must enable auth")
	}
}
