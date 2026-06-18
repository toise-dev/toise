// Package auth provides optional bearer-token authentication for Toise's data
// surfaces — the HTTP query surfaces (GraphQL, MCP, debug UI) and the OTLP/gRPC
// ingest. It is off when no tokens are configured, preserving the phase-1
// trusted-network default (ADR 0014 → ADR 0024). Tokens are secrets and are
// sourced from the environment only (ADR 0023).
//
// Tokens carry a role: a full token (auth_tokens) works on both surfaces, a
// read-only token (read_tokens) only on the HTTP query surfaces, an ingest-only
// token (ingest_tokens) only on OTLP ingest — least privilege for a producer
// that should never read, or a dashboard that should never write (0.7.0).
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

// ctxKey is the private type for context values this package sets.
type ctxKey int

const writableKey ctxKey = iota

// CanWrite reports whether the caller in ctx may perform a write (e.g. annotate
// an entity). Writes require a full or tenant-scoped token; a read-only token
// cannot. It defaults to true when no decision was recorded — with auth disabled
// (trusted network) every caller may write.
func CanWrite(ctx context.Context) bool {
	v, ok := ctx.Value(writableKey).(bool)
	return !ok || v
}

// surface is the data surface a token is being checked against.
type surface int

const (
	surfaceRead   surface = iota // the HTTP query surfaces (GraphQL, MCP, debug UI)
	surfaceIngest                // OTLP/gRPC ingest
)

// Authenticator validates bearer tokens and authorizes them per tenant and per
// surface. Global tokens are valid for every tenant; a tenant-scoped token is
// authorized only for its own tenant (#104). Role-restricted global tokens are
// valid on one surface only. The zero/empty value is disabled (everything
// passes), so callers can wire it unconditionally.
type Authenticator struct {
	tokens       [][]byte            // global, both surfaces, every tenant
	readTokens   [][]byte            // global, read surface only
	ingestTokens [][]byte            // global, ingest surface only
	scoped       map[string][][]byte // tenant id -> tokens (both surfaces) valid only for that tenant
	onFailure    func()              // optional, observed on every rejected authentication
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

// New builds an Authenticator accepting the given global, full-role tokens.
func New(tokens []string) *Authenticator {
	return NewWithTenantTokens(tokens, nil)
}

// NewWithTenantTokens builds an Authenticator with global full-role tokens plus
// tenant-scoped tokens. A scoped token authenticates, but is authorized only for
// its tenant (#104).
func NewWithTenantTokens(global []string, scoped map[string][]string) *Authenticator {
	return NewWithRoles(global, nil, nil, scoped)
}

// NewWithRoles builds an Authenticator with full-role global tokens (both), plus
// read-only and ingest-only global tokens, plus full-role tenant-scoped tokens.
// Blank tokens are ignored; with none left, the Authenticator is disabled.
func NewWithRoles(both, readOnly, ingestOnly []string, scoped map[string][]string) *Authenticator {
	a := &Authenticator{}
	a.tokens = toBytes(both)
	a.readTokens = toBytes(readOnly)
	a.ingestTokens = toBytes(ingestOnly)
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

func toBytes(toks []string) [][]byte {
	var out [][]byte
	for _, t := range toks {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, []byte(t))
		}
	}
	return out
}

// Enabled reports whether any token is configured (i.e. auth is enforced).
func (a *Authenticator) Enabled() bool {
	return len(a.tokens) > 0 || len(a.readTokens) > 0 || len(a.ingestTokens) > 0 || len(a.scoped) > 0
}

// matchAny reports, in constant time per candidate, whether token equals any of
// the set (no early return, so timing does not leak which matched).
func matchAny(token []byte, set [][]byte) bool {
	ok := false
	for _, want := range set {
		if subtle.ConstantTimeCompare(token, want) == 1 {
			ok = true
		}
	}
	return ok
}

// roleSet returns the global role-restricted set for the surface.
func (a *Authenticator) roleSet(s surface) [][]byte {
	if s == surfaceIngest {
		return a.ingestTokens
	}
	return a.readTokens
}

// valid reports whether token is accepted on the surface (authentication only):
// a full token, or a role token matching the surface, or any tenant-scoped
// token. Per-tenant authorization is allowedForTenant.
func (a *Authenticator) valid(token string, s surface) bool {
	got := []byte(token)
	full := matchAny(got, a.tokens)
	role := matchAny(got, a.roleSet(s))
	scoped := false
	for _, toks := range a.scoped {
		if matchAny(got, toks) {
			scoped = true
		}
	}
	return full || role || scoped
}

// allowedForTenant reports whether token may touch tenantID on the surface:
// global tokens (full, or role matching the surface) may touch every tenant; a
// scoped token only its own.
func (a *Authenticator) allowedForTenant(token, tenantID string, s surface) bool {
	got := []byte(token)
	full := matchAny(got, a.tokens)
	role := matchAny(got, a.roleSet(s))
	scoped := matchAny(got, a.scoped[tenantID])
	return full || role || scoped
}

// bearer extracts the token from an "Authorization: Bearer <token>" header
// value, or "" if the header is not a bearer.
func bearer(h string) string {
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func (a *Authenticator) validHeader(h string, s surface) bool {
	t := bearer(h)
	return t != "" && a.valid(t, s)
}

func (a *Authenticator) headerAllowedForTenant(h, tenantID string, s surface) bool {
	t := bearer(h)
	return t != "" && a.allowedForTenant(t, tenantID, s)
}

// canWriteHeader reports whether the bearer is a write-capable token: a full
// global token or any tenant-scoped token. Read-only tokens are not.
func (a *Authenticator) canWriteHeader(h string) bool {
	t := bearer(h)
	if t == "" {
		return false
	}
	got := []byte(t)
	if matchAny(got, a.tokens) {
		return true
	}
	for _, toks := range a.scoped {
		if matchAny(got, toks) {
			return true
		}
	}
	return false
}

// AllowedForTenantGRPC reports whether the bearer carried in ctx's gRPC metadata
// may ingest into tenantID. With auth disabled it always allows. The ingest
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
	return a.headerAllowedForTenant(vals[0], tenantID, surfaceIngest)
}

// HTTPMiddleware wraps next, requiring a valid read-surface bearer token on every
// request whose path is not in public. When auth is disabled it is a
// pass-through.
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
			if !a.validHeader(h, surfaceRead) {
				a.failed()
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Authenticated for reads; now authorize the token for the request's
			// tenant. An invalid tenant header passes through — the tenant router
			// rejects it with a 400 and better context.
			if id, ok := tenant.FromHTTP(r); ok && !a.headerAllowedForTenant(h, id, surfaceRead) {
				a.failed()
				http.Error(w, "token not authorized for this tenant", http.StatusForbidden)
				return
			}
			// Tag the request so write handlers (annotate) can reject a read-only
			// token: full/scoped tokens may write, read-only tokens may not.
			ctx := context.WithValue(r.Context(), writableKey, a.canWriteHeader(h))
			next.ServeHTTP(w, r.WithContext(ctx))
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

// checkContext validates the ingest-surface bearer token in the gRPC metadata.
func (a *Authenticator) checkContext(ctx context.Context) error {
	if !a.Enabled() {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	if len(vals) == 0 || !a.validHeader(vals[0], surfaceIngest) {
		a.failed()
		return status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	}
	return nil
}
