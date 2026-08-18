package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"", Default, true},
		{"  ", Default, true},
		{"acme", "acme", true},
		{"Acme-Corp_1.2", "Acme-Corp_1.2", true},
		{"a/b", "", false},                    // path separator
		{"..", "", false},                     // traversal
		{".", "", false},                      // traversal
		{"../etc", "", false},                 // traversal
		{"a b", "", false},                    // space
		{"tenant!", "", false},                // punctuation
		{string(make([]byte, 65)), "", false}, // too long
	}
	for _, c := range cases {
		got, ok := Sanitize(c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("Sanitize(%q) = (%q, %v), want (%q, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	if got := FromContext(context.Background()); got != Default {
		t.Errorf("empty context = %q, want %q", got, Default)
	}
	ctx := NewContext(context.Background(), "acme")
	if got := FromContext(ctx); got != "acme" {
		t.Errorf("FromContext = %q, want acme", got)
	}
}

func TestFromHTTP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	if id, ok := FromHTTP(r); !ok || id != Default {
		t.Errorf("no header = (%q,%v), want default,true", id, ok)
	}
	r.Header.Set(HeaderOrgID, "acme")
	if id, ok := FromHTTP(r); !ok || id != "acme" {
		t.Errorf("with header = (%q,%v), want acme,true", id, ok)
	}
	r.Header.Set(HeaderOrgID, "../bad")
	if _, ok := FromHTTP(r); ok {
		t.Error("invalid header should be rejected")
	}
}

func TestFromGRPC(t *testing.T) {
	if id, ok := FromGRPC(context.Background()); !ok || id != Default {
		t.Errorf("no metadata = (%q,%v), want default,true", id, ok)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-scope-orgid", "acme"))
	if id, ok := FromGRPC(ctx); !ok || id != "acme" {
		t.Errorf("with metadata = (%q,%v), want acme,true", id, ok)
	}
	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-scope-orgid", "a/b"))
	if _, ok := FromGRPC(bad); ok {
		t.Error("invalid metadata should be rejected")
	}
}

// TestFromHTTPWithQuery pins the debug-UI resolver: a form cannot set a header,
// so ?tenant= wins over X-Scope-OrgID, both are sanitized, and absence of both
// still yields the default tenant.
func TestFromHTTPWithQuery(t *testing.T) {
	mk := func(target, header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if header != "" {
			r.Header.Set(HeaderOrgID, header)
		}
		return r
	}
	if id, ok := FromHTTPWithQuery(mk("/?tenant=acme", "other")); !ok || id != "acme" {
		t.Errorf("query must win over the header: got %q ok=%v", id, ok)
	}
	if id, ok := FromHTTPWithQuery(mk("/", "acme")); !ok || id != "acme" {
		t.Errorf("header fallback: got %q ok=%v", id, ok)
	}
	if id, ok := FromHTTPWithQuery(mk("/", "")); !ok || id != Default {
		t.Errorf("no header, no query: got %q ok=%v, want the default tenant", id, ok)
	}
	if _, ok := FromHTTPWithQuery(mk("/?tenant=..%2Fescape", "")); ok {
		t.Error("an invalid query tenant must be refused, not defaulted")
	}
}
