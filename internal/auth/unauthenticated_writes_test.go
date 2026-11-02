package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// probeWrite runs a request through the middleware and reports what the handler
// saw: whether it was served at all, and whether it was allowed to write.
func probeWrite(a *Authenticator, token, tenantID string) (served, writable bool, code int) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served, writable = true, CanWrite(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenantID != "" {
		req.Header.Set("X-Scope-OrgID", tenantID)
	}
	a.HTTPMiddleware(nil)(next).ServeHTTP(rec, req)
	return served, writable, rec.Code
}

// TestUnauthenticatedWritesGate covers the defect this change closes: with no
// token configured the middleware used to hand the request straight through,
// recording no decision, and CanWrite's absent-decision default made every
// anonymous caller a writer. The request must still be SERVED — reads stay open
// on a trusted network — but writing is now a decision, not an accident.
func TestUnauthenticatedWritesGate(t *testing.T) {
	disabled := NewWithRoles(nil, nil, nil, nil)
	if disabled.Enabled() {
		t.Fatal("an authenticator with no token must be disabled")
	}

	served, writable, code := probeWrite(disabled, "", "")
	if !served || code != http.StatusOK {
		t.Fatalf("auth off must still serve reads: served=%v code=%d", served, code)
	}
	if writable {
		t.Error("auth off must refuse writes by default, not grant them")
	}

	served, writable, code = probeWrite(disabled.SetUnauthenticatedWrites(true), "", "")
	if !served || code != http.StatusOK || !writable {
		t.Errorf("the opt-in must restore the trusted-network write: served=%v writable=%v code=%d", served, writable, code)
	}
}

// TestScopedTokenCannotWriteAnotherTenant pins the half of the isolation that
// already held before this change, so that it cannot be lost later: a token
// bound to one tenant is refused outright on another, in trust-header mode —
// where the client names the tenant itself and could otherwise aim elsewhere.
func TestScopedTokenCannotWriteAnotherTenant(t *testing.T) {
	a := NewWithRoles(nil, nil, nil, map[string][]string{
		"acme":   {"acme-tok"},
		"globex": {"globex-tok"},
	})
	if a.deriveOnly {
		t.Fatal("this test must run in trust-header mode, the permissive one")
	}

	served, writable, code := probeWrite(a, "acme-tok", "acme")
	if !served || !writable || code != http.StatusOK {
		t.Fatalf("a scoped token must write its own tenant: served=%v writable=%v code=%d", served, writable, code)
	}
	served, writable, code = probeWrite(a, "acme-tok", "globex")
	if served || writable || code != http.StatusForbidden {
		t.Errorf("a scoped token must be refused on another tenant: served=%v writable=%v code=%d (want 403)", served, writable, code)
	}
}

// TestUnauthenticatedWritesAllowedNeedsNothingAuthenticating covers the hole a
// reviewer found in the first shape of this gate: the stdio transport recorded
// the opt-in alone, so an operator who had configured tokens — asking for
// writes to carry a credential — still had an unauthenticated local transport
// that could annotate. A transport that cannot present a credential must not
// become the way around that choice.
//
// Both the middleware and the stdio wiring ask this one method, so a drift
// between the two transports is not possible by construction rather than by
// a test asserting they agree.
func TestUnauthenticatedWritesAllowedNeedsNothingAuthenticating(t *testing.T) {
	cases := []struct {
		name string
		a    *Authenticator
		want bool
	}{
		{"opt-in, nothing configured", NewWithRoles(nil, nil, nil, nil).SetUnauthenticatedWrites(true), true},
		{"no opt-in, nothing configured", NewWithRoles(nil, nil, nil, nil), false},
		{"opt-in but a full token exists", NewWithRoles([]string{"t"}, nil, nil, nil).SetUnauthenticatedWrites(true), false},
		{"opt-in but a read token exists", NewWithRoles(nil, []string{"t"}, nil, nil).SetUnauthenticatedWrites(true), false},
		{"opt-in but an ingest token exists", NewWithRoles(nil, nil, []string{"t"}, nil).SetUnauthenticatedWrites(true), false},
		{"opt-in but a tenant token exists", NewWithRoles(nil, nil, nil, map[string][]string{"acme": {"t"}}).SetUnauthenticatedWrites(true), false},
		{"opt-in but a scoped read token exists", NewWithRoles(nil, nil, nil, nil).
			WithScopedRoleTokens(map[string][]string{"acme": {"t"}}, nil).SetUnauthenticatedWrites(true), false},
		// OIDC declared but its verifier not built yet. This is the case a recette
		// caught: the verifier comes from a network discovery against the issuer,
		// so a caller asking before it completes was told nothing authenticated —
		// and on an OIDC-only deployment the stdio transport then granted writes.
		{"opt-in but OIDC is declared, verifier not built", NewWithRoles(nil, nil, nil, nil).
			SetUnauthenticatedWrites(true).SetOIDCDeclared(true), false},
		{"opt-in but an OIDC verifier is installed", NewWithRoles(nil, nil, nil, nil).
			SetUnauthenticatedWrites(true).SetOIDC(stubVerifier{}), false},
	}
	for _, c := range cases {
		if got := c.a.UnauthenticatedWritesAllowed(); got != c.want {
			t.Errorf("%s: UnauthenticatedWritesAllowed = %v, want %v", c.name, got, c.want)
		}
	}
}

// stubVerifier stands in for a built OIDC verifier; it is never consulted here,
// only its presence is.
type stubVerifier struct{}

func (stubVerifier) Verify(context.Context, string) (string, string, bool) { return "", "", false }

// TestEnabledFollowsTheDeclarationNotTheVerifier pins the ordering hazard
// directly. Enabled() answers "is a credential required". Answering it from the
// verifier made it mean "can a credential be checked right now", which is false
// for the whole window between configuration and discovery — and callers wired
// inside that window took it as "nothing authenticates".
func TestEnabledFollowsTheDeclarationNotTheVerifier(t *testing.T) {
	a := NewWithRoles(nil, nil, nil, nil)
	if a.Enabled() {
		t.Fatal("nothing configured: auth must be off")
	}
	if !a.SetOIDCDeclared(true).Enabled() {
		t.Error("OIDC declared must enforce auth before its verifier exists")
	}
	// Setting the verifier sets the declaration with it, in BOTH directions, so
	// the pair cannot hold "declared, never verifiable" — a state that would
	// refuse every bearer OIDC exists to accept, turning a permissive bug into a
	// total refusal with no diagnostic.
	b := NewWithRoles(nil, nil, nil, nil).SetOIDC(stubVerifier{})
	if !b.Enabled() || !b.oidcDeclared {
		t.Error("installing a verifier must also declare OIDC")
	}
	c := NewWithRoles(nil, nil, nil, nil).SetOIDCDeclared(true).SetOIDC(nil)
	if c.oidcDeclared || c.Enabled() {
		t.Error("clearing the verifier must clear the declaration: declared with nothing to verify refuses everything")
	}
}
