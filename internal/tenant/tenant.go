// Package tenant resolves and carries a generic, vendor-neutral tenant id so the
// graph can be scoped per tenant (ADR 0025). The id comes from the X-Scope-OrgID
// request metadata (the Mimir/Loki/Tempo/VictoriaMetrics de-facto standard) or a
// tenant.id resource attribute, and falls back to "default" — so a single-tenant
// deployment that never sets one behaves exactly as before.
package tenant

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// Default is the tenant used when none is supplied.
const Default = "default"

// HeaderOrgID is the HTTP header carrying the tenant id. The lowercased form is the
// gRPC metadata key.
const HeaderOrgID = "X-Scope-OrgID"

// ResourceAttr is the OTLP resource attribute carrying the tenant id.
const ResourceAttr = "tenant.id"

const maxLen = 64

type ctxKey struct{}

// NewContext returns ctx carrying the tenant id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the tenant id stored in ctx, or Default if none.
func FromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKey{}).(string); ok && id != "" {
		return id
	}
	return Default
}

// Sanitize validates and normalizes a raw tenant id into a safe single
// path-segment id. An empty id resolves to Default. It returns ok=false for an id
// that is too long, is "." / "..", or contains anything outside [A-Za-z0-9._-]
// (notably path separators) — so a tenant id can safely name a store directory
// without traversal risk.
func Sanitize(raw string) (id string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Default, true
	}
	if len(raw) > maxLen || raw == "." || raw == ".." {
		return "", false
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return "", false
		}
	}
	return raw, true
}

// FromHTTP resolves the tenant from an HTTP request's X-Scope-OrgID header. ok is
// false only when a present header is invalid; an absent header yields Default.
func FromHTTP(r *http.Request) (string, bool) {
	return Sanitize(r.Header.Get(HeaderOrgID))
}

// FromGRPC resolves the tenant from gRPC metadata (x-scope-orgid). ok is false only
// when a present value is invalid; an absent one yields Default.
func FromGRPC(ctx context.Context) (string, bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(strings.ToLower(HeaderOrgID))
	if len(vals) == 0 {
		return Default, true
	}
	return Sanitize(vals[0])
}

// ErrNotAllowed marks a tenant refused by creation policy (auto-create off,
// not on the allowlist, or over the tenant cap). It is permanent for the
// caller — a retry cannot mint the tenant — so transports map it to their
// invalid-argument class rather than a retryable failure.
var ErrNotAllowed = errors.New("tenant not allowed")
