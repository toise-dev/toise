// Package graphql wires the gqlgen executable schema, resolvers, transports,
// and guardrails into an http.Handler. See ADR 0010.
package graphql

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/graphql/resolvers"
)

// Config tunes the GraphQL server guardrails (patch 5).
type Config struct {
	// ComplexityLimit caps the analyzed complexity per request (default 1000).
	ComplexityLimit int
	// Timeout bounds each request (default 10s).
	Timeout time.Duration
	// AllowedOrigins is the WebSocket Origin allowlist. Same-origin requests and
	// non-browser clients (no Origin header) are always allowed; any other
	// browser Origin must appear here. Empty means same-origin only — this
	// prevents cross-site WebSocket hijacking.
	AllowedOrigins []string
	// DisableIntrospection turns the GraphQL introspection extension off (a
	// production hardening lever). The zero value keeps introspection on.
	DisableIntrospection bool
}

// originChecker enforces same-origin WebSocket connections plus an optional
// allowlist. Requests with no Origin header (non-browser clients) are allowed.
func originChecker(allowed []string) func(*http.Request) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[o] = struct{}{}
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		if _, ok := set[origin]; ok {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	}
}

// timeoutBody is returned (HTTP 503) when a request exceeds the timeout.
const timeoutBody = `{"errors":[{"message":"query timed out: narrow your selection, lower first:, or split it into smaller queries"}]}`

// NewHandler builds the GraphQL HTTP handler (POST, GET, and WebSocket
// subscriptions) backed by r, with introspection enabled, a complexity limit,
// and a per-request timeout.
func NewHandler(r *resolvers.Resolver, cfg Config) http.Handler {
	if cfg.ComplexityLimit <= 0 {
		cfg.ComplexityLimit = 1000
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: r}))
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
		Upgrader: websocket.Upgrader{
			CheckOrigin: originChecker(cfg.AllowedOrigins),
		},
	})
	srv.SetQueryCache(lru.New[*ast.QueryDocument](100))
	if !cfg.DisableIntrospection {
		srv.Use(extension.Introspection{})
	}
	srv.Use(extension.FixedComplexityLimit(cfg.ComplexityLimit))

	// The per-request timeout uses http.TimeoutHandler, whose ResponseWriter
	// does not implement http.Hijacker. A WebSocket subscription needs to hijack
	// the connection to upgrade it, so routing the upgrade through the timeout
	// makes every subscription fail ("response does not implement http.Hijacker").
	// A timeout is meaningless for a long-lived subscription anyway: bound only
	// the POST/GET request lifecycle, and let the upgrade reach srv untouched.
	timed := http.TimeoutHandler(srv, cfg.Timeout, timeoutBody)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if isWebSocketUpgrade(req) {
			srv.ServeHTTP(w, req)
			return
		}
		timed.ServeHTTP(w, req)
	})
}

// isWebSocketUpgrade reports whether req is a WebSocket upgrade handshake. The
// Connection header is a comma-separated token list (e.g. "keep-alive, Upgrade").
func isWebSocketUpgrade(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")
}
