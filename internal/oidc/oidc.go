// Package oidc verifies OIDC/JWT bearer tokens on Toise's read surfaces and maps
// their claims to a tenant id and an access role (ADR 0028). It is off unless an
// issuer is configured; the static bearer tokens (internal/auth) stay the
// baseline. JWT verification (signature via JWKS, issuer, audience, expiry) is
// delegated to github.com/coreos/go-oidc — never hand-rolled.
package oidc

import (
	"context"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/toise-dev/toise/internal/tenant"
)

// Role is the access role a verified token grants on the read surfaces.
type Role string

const (
	RoleFull   Role = "full"   // read + write (annotate)
	RoleRead   Role = "read"   // read only
	RoleIngest Role = "ingest" // ingest only — not valid on a read surface
)

// Config configures OIDC verification. Issuer empty disables OIDC.
type Config struct {
	Issuer      string // OIDC issuer URL (discovery); empty = OIDC off
	Audience    string // expected `aud` (the client/audience id)
	TenantClaim string // claim carrying the tenant id (default "tenant")
	RoleClaim   string // claim carrying the role; empty = every valid token is full
}

func (c Config) tenantClaim() string {
	if c.TenantClaim == "" {
		return "tenant"
	}
	return c.TenantClaim
}

// Verifier validates a JWT and extracts the tenant/role claims.
type Verifier struct {
	v   *gooidc.IDTokenVerifier
	cfg Config
}

// New builds a Verifier, discovering the issuer over the network. Returns
// (nil, nil) when cfg.Issuer is empty (OIDC disabled).
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, nil
	}
	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", cfg.Issuer, err)
	}
	return newFromVerifier(provider.Verifier(&gooidc.Config{ClientID: cfg.Audience}), cfg), nil
}

// newFromVerifier wires a Verifier around an already-built id-token verifier —
// the test seam (a verifier built from a static key set, no discovery).
func newFromVerifier(v *gooidc.IDTokenVerifier, cfg Config) *Verifier {
	return &Verifier{v: v, cfg: cfg}
}

// Verify validates rawToken and returns the tenant id (sanitized) and the role
// it grants ("full"/"read"/"ingest"). The role is "full" when no role claim is
// configured. ok=false on any verification, claim, or sanitization failure — the
// caller then falls through to the static-token path or rejects. The role is a
// plain string so internal/auth can depend on a narrow interface, not this
// package. Safe on a nil receiver (OIDC disabled).
func (vr *Verifier) Verify(ctx context.Context, rawToken string) (tenantID, role string, ok bool) {
	if vr == nil {
		return "", "", false
	}
	idt, err := vr.v.Verify(ctx, rawToken)
	if err != nil {
		return "", "", false
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return "", "", false
	}
	rawTenant, _ := claims[vr.cfg.tenantClaim()].(string)
	if rawTenant == "" {
		// An empty/absent tenant claim must NOT authenticate: tenant.Sanitize("")
		// resolves to the default tenant, which would silently grant default-tenant
		// access to any otherwise-valid JWT.
		return "", "", false
	}
	san, sanOK := tenant.Sanitize(rawTenant)
	if !sanOK || san == "" {
		return "", "", false // a non-canonical tenant claim is rejected, never coerced
	}
	out := RoleFull
	if vr.cfg.RoleClaim != "" {
		rs, _ := claims[vr.cfg.RoleClaim].(string)
		switch Role(rs) {
		case RoleFull, RoleRead, RoleIngest:
			out = Role(rs)
		default:
			// A configured role claim that is absent, empty, or unrecognized is a
			// hard reject — never a silent grant of full. Once the operator opts
			// into role claims, a token without a valid one is untrusted, not an
			// implicit admin.
			return "", "", false
		}
	}
	return san, string(out), true
}
