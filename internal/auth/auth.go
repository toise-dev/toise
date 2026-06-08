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
)

// Authenticator validates bearer tokens. The zero/empty value is disabled
// (everything passes), so callers can wire it unconditionally.
type Authenticator struct {
	tokens [][]byte // accepted tokens; nil/empty means auth disabled
}

// New builds an Authenticator accepting the given tokens. Empty/blank tokens are
// ignored; with none left, the Authenticator is disabled.
func New(tokens []string) *Authenticator {
	a := &Authenticator{}
	for _, t := range tokens {
		if t = strings.TrimSpace(t); t != "" {
			a.tokens = append(a.tokens, []byte(t))
		}
	}
	return a
}

// Enabled reports whether any token is configured (i.e. auth is enforced).
func (a *Authenticator) Enabled() bool { return len(a.tokens) > 0 }

// valid reports whether token matches an accepted one, in constant time.
func (a *Authenticator) valid(token string) bool {
	got := []byte(token)
	ok := false
	for _, want := range a.tokens {
		if subtle.ConstantTimeCompare(got, want) == 1 {
			ok = true
		}
	}
	return ok
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
			if !a.validHeader(r.Header.Get("Authorization")) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
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
		return status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	}
	return nil
}
