// Package auth provides optional bearer-token authentication for Toise's data
// surfaces — the HTTP query surfaces (GraphQL, MCP, debug UI) and the OTLP/gRPC
// ingest. It is off when no tokens are configured, preserving the phase-1
// trusted-network default (ADR 0014 → ADR 0024). Tokens are secrets and are
// sourced from the environment only (ADR 0023).
package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/toise-dev/toise/internal/tenant"
)

// Authenticator validates bearer tokens and authorizes them per tenant.
// Global tokens are valid for every tenant; a tenant-scoped token
// authenticates like any other but is authorized only for its own tenant
// (#104). The zero/empty value is disabled (everything passes), so callers
// can wire it unconditionally.
type Authenticator struct {
	tokens    [][]byte            // global tokens; valid for every tenant
	scoped    map[string][][]byte // tenant id -> tokens valid only for that tenant
	onFailure func()              // optional, observed on every rejected authentication
}

// OnFailure registers fn to run on every rejected authentication, HTTP or
// gRPC — the hook a metrics counter hangs off (#113). Set it before serving;
// it is not synchronized against concurrent use.
func (a *Authenticator) OnFailure(fn func()) { a.onFailure = fn }

func (a *Authenticator) failed() {
	if a.onFailure != nil {
		a.onFailure()
	}
}

// New builds an Authenticator accepting the given global tokens. Empty/blank
// tokens are ignored; with none left, the Authenticator is disabled.
func New(tokens []string) *Authenticator {
	return NewWithTenantTokens(tokens, nil)
}

// NewWithTenantTokens builds an Authenticator with global tokens plus
// tenant-scoped tokens. A scoped token authenticates, but is authorized only
// for its tenant: authentication establishes who, this establishes which
// tenants they may touch (#104).
func NewWithTenantTokens(global []string, scoped map[string][]string) *Authenticator {
	a := &Authenticator{}
	for _, t := range global {
		if t = strings.TrimSpace(t); t != "" {
			a.tokens = append(a.tokens, []byte(t))
		}
	}
	for tenantID, toks := range scoped {
		for _, t := range toks {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if a.scoped == nil {
				a.scoped = make(map[string][][]byte)
			}
			a.scoped[tenantID] = append(a.scoped[tenantID], []byte(t))
		}
	}
	return a
}

// Enabled reports whether any token is configured (i.e. auth is enforced).
func (a *Authenticator) Enabled() bool { return len(a.tokens) > 0 || len(a.scoped) > 0 }

// valid reports whether token matches any accepted token — global or scoped —
// in constant time. This is authentication only; per-tenant authorization is
// allowedForTenant.
func (a *Authenticator) valid(token string) bool {
	got := []byte(token)
	ok := false
	for _, want := range a.tokens {
		if subtle.ConstantTimeCompare(got, want) == 1 {
			ok = true
		}
	}
	for _, toks := range a.scoped {
		for _, want := range toks {
			if subtle.ConstantTimeCompare(got, want) == 1 {
				ok = true
			}
		}
	}
	return ok
}

// allowedForTenant reports whether token may touch tenantID: global tokens may
// touch every tenant, a scoped token only its own.
func (a *Authenticator) allowedForTenant(token, tenantID string) bool {
	got := []byte(token)
	ok := false
	for _, want := range a.tokens {
		if subtle.ConstantTimeCompare(got, want) == 1 {
			ok = true
		}
	}
	for _, want := range a.scoped[tenantID] {
		if subtle.ConstantTimeCompare(got, want) == 1 {
			ok = true
		}
	}
	return ok
}

// headerAllowedForTenant applies allowedForTenant to an
// "Authorization: Bearer <token>" header value.
func (a *Authenticator) headerAllowedForTenant(h, tenantID string) bool {
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	return a.allowedForTenant(strings.TrimSpace(h[len(prefix):]), tenantID)
}

// AllowedForTenantGRPC reports whether the bearer carried in ctx's gRPC
// metadata may touch tenantID. With auth disabled it always allows. The ingest
// receiver calls it per resolved tenant — including the per-ResourceLogs
// tenant.id override an interceptor cannot see (#104).
func (a *Authenticator) AllowedForTenantGRPC(ctx context.Context, tenantID string) bool {
	if !a.Enabled() {
		return true
	}
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return false
	}
	return a.headerAllowedForTenant(vals[0], tenantID)
}

// validHeader checks an "Authorization: Bearer <token>" header value.
func (a *Authenticator) validHeader(h string) bool {
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	return a.valid(strings.TrimSpace(h[len(prefix):]))
}

// HTTPMiddleware wraps next, requiring a valid bearer token on every request
// whose path is not in public. When auth is disabled it is a pass-through.
func (a *Authenticator) HTTPMiddleware(public map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !a.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			h := r.Header.Get("Authorization")
			if !a.validHeader(h) {
				a.failed()
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Authenticated; now authorize the token for the request's tenant.
			// An invalid tenant header passes through — the tenant router
			// rejects it with a 400 and better context.
			if id, ok := tenant.FromHTTP(r); ok && !a.headerAllowedForTenant(h, id) {
				a.failed()
				http.Error(w, "token not authorized for this tenant", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UnaryInterceptor authenticates unary gRPC calls (the OTLP Export is unary).
func (a *Authenticator) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := a.checkContext(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor authenticates streaming gRPC calls.
func (a *Authenticator) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := a.checkContext(ss.Context()); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// checkContext validates the bearer token carried in the gRPC metadata.
func (a *Authenticator) checkContext(ctx context.Context) error {
	if !a.Enabled() {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	if len(vals) == 0 || !a.validHeader(vals[0]) {
		a.failed()
		return status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	}
	return nil
}
