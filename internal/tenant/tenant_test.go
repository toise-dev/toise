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
