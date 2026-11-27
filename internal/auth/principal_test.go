package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

// subjectOIDC is a local fake verifier that returns its configured subject.
type subjectOIDC struct {
	tenant, role, subject string
}

func (v subjectOIDC) Verify(_ context.Context, raw string) (string, string, string, bool) {
	if raw == "" {
		return "", "", "", false
	}
	return v.tenant, v.role, v.subject, true
}

func expectedTokenPrincipal(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return "token:" + hex.EncodeToString(sum[:])[:8]
}

func TestPrincipalStaticTokens(t *testing.T) {
	probe := func(a *Authenticator, token string) (principal string, code int) {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal = Principal(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		a.HTTPMiddleware(nil)(next).ServeHTTP(rec, req)
		return principal, rec.Code
	}

	a := NewWithRoles([]string{"full-token"}, []string{"reader-token"}, nil, nil)

	p, code := probe(a, "full-token")
	if code != http.StatusOK {
		t.Fatalf("full token status = %d, want 200", code)
	}
	want := expectedTokenPrincipal("full-token")
	if p != want {
		t.Errorf("full token principal = %q, want %q", p, want)
	}
	if p == "" {
		t.Error("full token principal must not be empty")
	}

	p, code = probe(a, "reader-token")
	if code != http.StatusOK {
		t.Fatalf("read-only token status = %d, want 200", code)
	}
	want = expectedTokenPrincipal("reader-token")
	if p != want {
		t.Errorf("read-only token principal = %q, want %q", p, want)
	}
}

func TestPrincipalUnauthenticated(t *testing.T) {
	a := New(nil)
	a.SetUnauthenticatedWrites(true)

	var principal string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal = Principal(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	rec := httptest.NewRecorder()
	a.HTTPMiddleware(nil)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled auth status = %d, want 200", rec.Code)
	}
	if principal != "unauthenticated" {
		t.Errorf("disabled auth principal = %q, want %q", principal, "unauthenticated")
	}
}

func TestPrincipalOIDC(t *testing.T) {
	run := func(v subjectOIDC) (string, int) {
		a := NewWithRoles(nil, nil, nil, nil).SetOIDC(v)
		var principal string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal = Principal(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
		req.Header.Set("Authorization", "Bearer tenant-a-jwt")
		rec := httptest.NewRecorder()
		a.HTTPMiddleware(nil)(next).ServeHTTP(rec, req)
		return principal, rec.Code
	}

	if p, code := run(subjectOIDC{"tenant-a", "read", "user-a"}); code != http.StatusOK || p != "oidc:user-a" {
		t.Errorf("oidc subject user-a = %q (code %d), want %q", p, code, "oidc:user-a")
	}
	if p, code := run(subjectOIDC{"tenant-a", "read", ""}); code != http.StatusOK || p != "oidc" {
		t.Errorf("oidc empty subject = %q (code %d), want %q", p, code, "oidc")
	}
}

func TestPrincipalContext(t *testing.T) {
	if got := Principal(context.Background()); got != "" {
		t.Errorf("Principal(background) = %q, want empty", got)
	}
	ctx := WithPrincipal(context.Background(), "user-a")
	if got := Principal(ctx); got != "user-a" {
		t.Errorf("Principal round trip = %q, want user-a", got)
	}
}
