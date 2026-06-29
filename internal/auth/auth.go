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
//
// Ingest and read auth are decoupled (#262): in ingest-mTLS-only mode the OTLP
// surface is authenticated by mutual TLS and requires no bearer, while the read
// surfaces (GraphQL, MCP) still require their per-client scoped tokens or OIDC.
package auth

import (
	"context"
	"crypto/sha256"
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

const (
	writableKey ctxKey = iota
	// resolvedTenantKey carries the tenant the auth layer resolved for this
	// request when it is not the client header — set by the middleware for an
	// OIDC identity (the tenant comes from a verified claim). EffectiveTenantHTTP
	// reads it so the tenant router routes to the claim's tenant.
	resolvedTenantKey
)

// OIDCVerifier verifies an OIDC/JWT bearer on the read surfaces and returns the
// tenant and role ("full"/"read"/"ingest") it grants. It is a narrow interface
// so this package need not depend on internal/oidc (or its go-oidc transitive
// deps). nil disables OIDC.
type OIDCVerifier interface {
	Verify(ctx context.Context, rawToken string) (tenant, role string, ok bool)
}

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
	scoped       map[string][][]byte // tenant id -> full-role tokens (both surfaces) valid only for that tenant
	scopedRead   map[string][][]byte // tenant id -> read-only tokens, that tenant's read surfaces only (per-tenant RBAC)
	scopedIngest map[string][][]byte // tenant id -> ingest-only tokens, that tenant's ingest only (per-tenant RBAC)
	onFailure    func()              // optional, observed on every rejected authentication
	// deriveOnly is the ADR 0028 anti-spoofing mode: when set, a tenant-scoped
	// token's tenant is derived from its binding and the client-supplied
	// X-Scope-OrgID / tenant.id is ignored. Off by default (trust-header).
	deriveOnly bool
	// oidc, when set, verifies an OIDC/JWT bearer on the read surfaces as a second
	// path after the static tokens (ADR 0028). nil = OIDC off.
	oidc OIDCVerifier
	// ingestMTLSOnly decouples ingest auth from read auth (#262): when set, the
	// OTLP ingest surface is authenticated by mutual TLS at the transport, so the
	// gRPC interceptors neither require nor consult a bearer on ingest — while the
	// HTTP read surfaces still require their scoped tokens / OIDC. The operator
	// asserts the ingest listener runs RequireAndVerifyClientCert (cmd validates
	// mTLS is actually configured before enabling this).
	ingestMTLSOnly bool
}

// SetOIDC attaches an OIDC verifier for the read surfaces (ADR 0028); returns a
// for chaining. nil (the default) leaves OIDC off. Set before serving.
func (a *Authenticator) SetOIDC(v OIDCVerifier) *Authenticator {
	a.oidc = v
	return a
}

// SetTenantTrustMode switches the ADR 0028 anti-spoofing derivation on
// (derive-only) or off (trust-header, the default). Set before serving; it is
// not synchronized against concurrent use.
func (a *Authenticator) SetTenantTrustMode(deriveOnly bool) { a.deriveOnly = deriveOnly }

// SetIngestMTLSOnly decouples the ingest surface's auth from the read surface
// (#262): with v true, OTLP ingest is authenticated by mutual TLS and the gRPC
// path does not require a bearer, while GraphQL/MCP still require their scoped
// tokens. Returns a for chaining; set before serving.
func (a *Authenticator) SetIngestMTLSOnly(v bool) *Authenticator { a.ingestMTLSOnly = v; return a }

// TenantForBearer returns the single tenant a scoped bearer is bound to, and
// true, when the token is a per-client tenant-scoped token. A global (operator)
// token, an unknown token, or one bound to multiple tenants returns ("", false)
// — those keep header-based tenant selection even in derive-only mode.
func (a *Authenticator) TenantForBearer(h string) (string, bool) {
	t := bearer(h)
	if t == "" {
		return "", false
	}
	got := hashToken(t)
	if matchAny(got, a.tokens) || matchAny(got, a.readTokens) || matchAny(got, a.ingestTokens) {
		return "", false // a global/operator token is never derived
	}
	// A scoped token of any role (full/read/ingest) derives its tenant; collect
	// the distinct tenants it is bound to and derive only when there is exactly one.
	seen := map[string]struct{}{}
	for _, m := range []map[string][][]byte{a.scoped, a.scopedRead, a.scopedIngest} {
		for tid, toks := range m {
			if matchAny(got, toks) {
				seen[tid] = struct{}{}
			}
		}
	}
	if len(seen) == 1 {
		for tid := range seen {
			return tid, true
		}
	}
	return "", false // unknown, or ambiguous (bound to several tenants)
}

// TenantForBearerGRPC is TenantForBearer for the gRPC ingest metadata.
func (a *Authenticator) TenantForBearerGRPC(ctx context.Context) (string, bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return "", false
	}
	return a.TenantForBearer(vals[0])
}

// EffectiveTenantHTTP resolves the tenant an HTTP request targets, honoring the
// trust mode. An OIDC identity's tenant (resolved by the middleware from a
// verified claim) takes precedence; then, in derive-only, a scoped token's tenant
// is derived from its binding (the client X-Scope-OrgID is ignored); otherwise it
// falls back to the header. ok mirrors tenant.FromHTTP (false = invalid header).
func (a *Authenticator) EffectiveTenantHTTP(r *http.Request) (string, bool) {
	if id, ok := r.Context().Value(resolvedTenantKey).(string); ok && id != "" {
		return id, true
	}
	if a.deriveOnly && a.Enabled() {
		if id, ok := a.TenantForBearer(r.Header.Get("Authorization")); ok {
			return id, true
		}
	}
	return tenant.FromHTTP(r)
}

// EffectiveTenantGRPC resolves the ingest tenant from the gRPC metadata, honoring
// the trust mode. In derive-only a scoped token's tenant is derived and locked
// — the caller MUST then ignore any per-ResourceLogs tenant.id override, else
// that override is a spoofing channel. locked is false in trust-header mode or
// for a global token; ok mirrors tenant.FromGRPC.
func (a *Authenticator) EffectiveTenantGRPC(ctx context.Context) (id string, locked, ok bool) {
	if a.deriveOnly && a.Enabled() {
		if t, found := a.TenantForBearerGRPC(ctx); found {
			return t, true, true
		}
	}
	id, ok = tenant.FromGRPC(ctx)
	return id, false, ok
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
	a.scoped = hashScopedMap(scoped)
	return a
}

// WithScopedRoleTokens adds per-tenant role-scoped tokens — read-only and
// ingest-only — authorized only for their tenant and matching surface
// (per-tenant RBAC, ADR 0028). Full-role tenant tokens stay in scoped. Returns a
// for chaining; call before serving.
func (a *Authenticator) WithScopedRoleTokens(read, ingest map[string][]string) *Authenticator {
	a.scopedRead = hashScopedMap(read)
	a.scopedIngest = hashScopedMap(ingest)
	return a
}

// hashScopedMap hashes a tenant -> tokens map into tenant -> token hashes,
// dropping blanks. Returns nil when nothing is left (so Enabled stays accurate).
func hashScopedMap(m map[string][]string) map[string][][]byte {
	var out map[string][][]byte
	for tenantID, toks := range m {
		for _, t := range toks {
			if t = strings.TrimSpace(t); t == "" {
				continue
			}
			if out == nil {
				out = make(map[string][][]byte)
			}
			out[tenantID] = append(out[tenantID], hashToken(t))
		}
	}
	return out
}

// scopedRoleSet returns the role-scoped token map that grants surface s — read
// tokens for the read surface, ingest tokens for ingest. Full scoped tokens
// (a.scoped) grant both surfaces and are checked separately.
func (a *Authenticator) scopedRoleSet(s surface) map[string][][]byte {
	if s == surfaceIngest {
		return a.scopedIngest
	}
	return a.scopedRead
}

func toBytes(toks []string) [][]byte {
	var out [][]byte
	for _, t := range toks {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, hashToken(t))
		}
	}
	return out
}

// hashToken returns the SHA-256 of a token. Configured tokens are stored as
// hashes, never plaintext (ADR 0028): a heap dump never yields a usable token,
// and each request hashes the presented bearer and matches in constant time.
// SHA-256 (unsalted) fits high-entropy random tokens — unlike a low-entropy
// password there is no precomputation advantage to defend against.
func hashToken(t string) []byte {
	sum := sha256.Sum256([]byte(t))
	return sum[:]
}

// Enabled reports whether any token or OIDC is configured (i.e. auth is
// enforced). An OIDC-only deployment (no static tokens) still enforces auth.
func (a *Authenticator) Enabled() bool {
	return len(a.tokens) > 0 || len(a.readTokens) > 0 || len(a.ingestTokens) > 0 ||
		len(a.scoped) > 0 || len(a.scopedRead) > 0 || len(a.scopedIngest) > 0 || a.oidc != nil
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
	got := hashToken(token)
	full := matchAny(got, a.tokens)
	role := matchAny(got, a.roleSet(s))
	scoped := false
	for _, toks := range a.scoped {
		if matchAny(got, toks) {
			scoped = true
		}
	}
	// A role-scoped token grants only its matching surface.
	for _, toks := range a.scopedRoleSet(s) {
		if matchAny(got, toks) {
			scoped = true
		}
	}
	return full || role || scoped
}

// allowedForTenant reports whether token may touch tenantID on the surface:
// global tokens (full, or role matching the surface) may touch every tenant; a
// full scoped token only its own; a role-scoped token only its own and only on
// the matching surface (per-tenant RBAC, ADR 0028).
func (a *Authenticator) allowedForTenant(token, tenantID string, s surface) bool {
	got := hashToken(token)
	full := matchAny(got, a.tokens)
	role := matchAny(got, a.roleSet(s))
	scoped := matchAny(got, a.scoped[tenantID]) || matchAny(got, a.scopedRoleSet(s)[tenantID])
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
	got := hashToken(t)
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
	if a.ingestMTLSOnly {
		return true // ingest is gated by mutual TLS, not a per-tenant bearer (#262)
	}
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
			if a.validHeader(h, surfaceRead) {
				// Static token authenticated for reads; now authorize it for the
				// request's EFFECTIVE tenant — in derive-only that is the scoped
				// token's own tenant (so a scoped token is always allowed for it),
				// not the client header. An invalid tenant header passes through —
				// the tenant router rejects it with a 400 and better context.
				if id, ok := a.EffectiveTenantHTTP(r); ok && !a.allowedForTenant(bearer(h), id, surfaceRead) {
					a.failed()
					http.Error(w, "token not authorized for this tenant", http.StatusForbidden)
					return
				}
				// Tag the request so write handlers (annotate) can reject a
				// read-only token: full/scoped tokens may write, read-only cannot.
				ctx := context.WithValue(r.Context(), writableKey, a.canWriteHeader(h))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// Second path: a verified OIDC/JWT bearer authenticates with a
			// claim-derived tenant and role (ADR 0028). The tenant comes from the
			// claim (the client X-Scope-OrgID is ignored), flowed to the router via
			// resolvedTenantKey. An ingest-only role may not read.
			if a.oidc != nil {
				if tID, role, ok := a.oidc.Verify(r.Context(), bearer(h)); ok {
					if role == "ingest" {
						a.failed()
						http.Error(w, "token not authorized for this tenant", http.StatusForbidden)
						return
					}
					ctx := context.WithValue(r.Context(), resolvedTenantKey, tID)
					ctx = context.WithValue(ctx, writableKey, role == "full")
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			a.failed()
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	if a.ingestMTLSOnly {
		// Ingest is authenticated by mutual TLS at the transport (the gRPC server
		// runs RequireAndVerifyClientCert); no bearer is required or consulted on
		// this surface, decoupled from the read surfaces' tokens (#262).
		return nil
	}
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
