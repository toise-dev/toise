package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"
)

// TestIngestMTLSOnlyDecouplesSurfaces is the #262 lock: with ingest-mTLS-only
// set, read-surface tokens still gate GraphQL/MCP, but OTLP ingest requires no
// bearer (it is authenticated by mutual TLS at the transport). Without the flag,
// the same configuration would force a bearer on ingest — the coupling #262
// removes.
func TestIngestMTLSOnlyDecouplesSurfaces(t *testing.T) {
	// A deployment that protects reads with a read-only token and leaves ingest
	// to mutual TLS.
	a := NewWithRoles(nil, []string{"read-secret"}, nil, nil).SetIngestMTLSOnly(true)
	ui := a.UnaryInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }

	// Ingest: NO bearer is required — mTLS authenticated the producer.
	if _, err := ui(context.Background(), nil, nil, handler); err != nil {
		t.Errorf("ingest-mtls-only: bearer-less ingest must be accepted, got %v", err)
	}
	if !a.AllowedForTenantGRPC(context.Background(), "acme") {
		t.Error("ingest-mtls-only: per-tenant ingest must not require a bearer")
	}

	// Reads: the read-only token still gates the HTTP surfaces.
	mw := a.HTTPMiddleware(map[string]bool{"/healthz": true})
	for _, c := range []struct {
		name, header string
		want         int
	}{
		{"no token rejected", "", http.StatusUnauthorized},
		{"valid read token", "Bearer read-secret", http.StatusOK},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			mw(okHandler()).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("read surface status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

// TestIngestRequiresBearerWithoutMTLSFlag is the control: the same read-token
// configuration, WITHOUT ingest-mtls-only, still demands a bearer on ingest —
// proving the decoupling is opt-in and the default posture is unchanged.
func TestIngestRequiresBearerWithoutMTLSFlag(t *testing.T) {
	a := NewWithRoles(nil, []string{"read-secret"}, nil, nil) // no SetIngestMTLSOnly
	ui := a.UnaryInterceptor()
	handler := func(_ context.Context, _ any) (any, error) { return "ok", nil }

	if _, err := ui(context.Background(), nil, nil, handler); err == nil {
		t.Error("default posture: bearer-less ingest must be rejected when tokens are configured")
	}
	// A read-only token does not grant ingest either (role separation intact).
	rd := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer read-secret"))
	if _, err := ui(rd, nil, nil, handler); err == nil {
		t.Error("a read-only token must not authenticate ingest")
	}
}
