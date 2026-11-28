package auth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/metadata"
)

func grpcCtxWithBearer(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func TestReloadAddsTokenForNewTenant(t *testing.T) {
	a := NewWithRoles([]string{"fake-global-token"}, nil, nil, nil).WithScopedRoleTokens(nil, map[string][]string{"acme": {"fake-ingest-token-1"}})

	if a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-2"), "beta") {
		t.Fatal("tenant beta should be refused before reload")
	}

	err := a.ReloadScopedTokens(nil, nil, map[string][]string{
		"acme": {"fake-ingest-token-1"},
		"beta": {"fake-ingest-token-2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-2"), "beta") {
		t.Error("tenant beta should be allowed after reload")
	}
	if a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-2"), "acme") {
		t.Error("beta token should still be refused for acme")
	}
	if !a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-1"), "acme") {
		t.Error("acme's old token should still be allowed for acme")
	}
}

func TestReloadRemovedTokenRefusedNextRequest(t *testing.T) {
	a := NewWithRoles([]string{"fake-global-token"}, nil, nil, nil).WithScopedRoleTokens(nil, map[string][]string{"acme": {"fake-ingest-token-1", "fake-ingest-token-2"}})

	if err := a.ReloadScopedTokens(nil, nil, map[string][]string{"acme": {"fake-ingest-token-1"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-2"), "acme") {
		t.Error("removed ingest token should be refused immediately after reload")
	}
	if !a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-1"), "acme") {
		t.Error("remaining ingest token should still be allowed")
	}

	// Read surface: scoped (not global) read tokens removed via reload.
	b := NewWithRoles([]string{"fake-global-token"}, nil, nil, nil).
		WithScopedRoleTokens(map[string][]string{"acme": {"fake-read-token-1", "fake-read-token-2"}}, nil)

	mw := b.HTTPMiddleware(nil)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := mw(inner)

	doRequest := func(token string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://fake.example.invalid/api/v1/fake-data", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Scope-OrgID", "acme")
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := doRequest("fake-read-token-2"); code != http.StatusOK {
		t.Fatalf("scoped read token should be accepted before reload, got %d", code)
	}

	if err := b.ReloadScopedTokens(nil, map[string][]string{"acme": {"fake-read-token-1"}}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if code := doRequest("fake-read-token-2"); code != http.StatusUnauthorized {
		t.Errorf("removed scoped read token should get 401 on next request, got %d", code)
	}
	if code := doRequest("fake-read-token-1"); code != http.StatusOK {
		t.Errorf("remaining scoped read token should get 200, got %d", code)
	}
}

func TestReloadRefusesToToggleAuthentication(t *testing.T) {
	a := NewWithRoles(nil, nil, nil, nil).WithScopedRoleTokens(nil, map[string][]string{"acme": {"fake-ingest-token-1"}})
	if !a.Enabled() {
		t.Fatal("authenticator should be enabled via scoped ingest tokens")
	}

	err := a.ReloadScopedTokens(nil, nil, nil)
	if err == nil {
		t.Fatal("reload that would disable authentication should return an error")
	}
	if !a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-ingest-token-1"), "acme") {
		t.Error("old token should still be accepted after failed reload")
	}

	errOff := err
	b := New(nil)
	if b.Enabled() {
		t.Fatal("authenticator with nothing configured should be disabled")
	}
	err = b.ReloadScopedTokens(nil, nil, map[string][]string{"acme": {"fake-ingest-token-1"}})
	if err == nil {
		t.Fatal("reload that would enable authentication should return an error")
	}
	if b.Enabled() {
		t.Error("failed reload must not enable authentication")
	}

	if containsToken(err) || containsToken(errOff) {
		t.Error("error text must not contain any token literal")
	}
}

func containsToken(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "fake-ingest-token-1") || strings.Contains(msg, "fake-global-token") || strings.Contains(msg, "fake-read-token-1") || strings.Contains(msg, "fake-tok-a") || strings.Contains(msg, "fake-tok-i") || strings.Contains(msg, "fake-ingest-token-2") || strings.Contains(msg, "fake-read-token-2")
}

func TestReloadPreservesTokenHashesOnly(t *testing.T) {
	plain := "fake-plain-token-x"
	a := NewWithRoles([]string{"fake-global-token"}, nil, nil, map[string][]string{"acme": {plain}})
	if err := a.ReloadScopedTokens(nil, nil, map[string][]string{"acme": {"fake-plain-token-y"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sets := a.loadSets()
	checkMap := func(m map[string][][]byte, label string) {
		t.Helper()
		for tenant, toks := range m {
			for i, tok := range toks {
				if len(tok) != sha256.Size {
					t.Errorf("%s[%s][%d]: length %d, want %d", label, tenant, i, len(tok), sha256.Size)
				}
				if string(tok) == plain {
					t.Errorf("%s[%s][%d] stores plaintext token", label, tenant, i)
				}
			}
		}
	}
	checkMap(sets.full, "full")
	checkMap(sets.read, "read")
	checkMap(sets.ingest, "ingest")
}

func TestReloadNeverShowsMixedState(t *testing.T) {
	a := NewWithRoles([]string{"fake-global-token"}, nil, nil, nil)

	setA := map[string][]string{"acme": {"fake-tok-a"}}
	setB := map[string][]string{}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CanWrite(r.Context()) {
			w.Header().Set("X-Can-Write", "1")
		} else {
			w.Header().Set("X-Can-Write", "0")
		}
		w.WriteHeader(http.StatusOK)
	})
	h := a.HTTPMiddleware(nil)(inner)

	doRequest := func() (int, bool) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://fake.example.invalid/api/v1/fake-data", nil)
		req.Header.Set("Authorization", "Bearer fake-tok-a")
		req.Header.Set("X-Scope-OrgID", "acme")
		h.ServeHTTP(rec, req)
		canWrite := rec.Header().Get("X-Can-Write") == "1"
		return rec.Code, canWrite
	}

	var failures atomic.Int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				code, canWrite := doRequest()
				if code == http.StatusForbidden {
					if failures.Add(1) <= 3 {
						t.Errorf("observed 403 (mixed state)")
					}
				} else if code == http.StatusOK && !canWrite {
					if failures.Add(1) <= 3 {
						t.Errorf("observed (200, canWrite=false) (mixed state)")
					}
				} else if code != http.StatusOK && code != http.StatusUnauthorized {
					if failures.Add(1) <= 3 {
						t.Errorf("unexpected status %d", code)
					}
				}
				// Ingest surface under -race.
				_ = a.AllowedForTenantGRPC(grpcCtxWithBearer("fake-tok-i"), "acme")
			}
		}()
	}

	for i := 0; i < 3000; i++ {
		var err error
		if i%2 == 0 {
			err = a.ReloadScopedTokens(setA, nil, nil)
		} else {
			err = a.ReloadScopedTokens(setB, nil, nil)
		}
		if err != nil {
			t.Fatalf("reload %d failed: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()

	if n := failures.Load(); n > 0 {
		t.Errorf("%d mixed-state observations", n)
	}
}
