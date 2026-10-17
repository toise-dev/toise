package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func scrapeReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// TestMetricsScope covers who may read which tenants' series on the scrape
// surface (#463). The cases that matter are the two extremes: a global token
// must keep the operator's single all-tenant scrape, and everything else must
// not be able to count the other clients.
func TestMetricsScope(t *testing.T) {
	a := NewWithRoles([]string{"op"}, []string{"reader"}, []string{"shipper"},
		map[string][]string{"acme": {"acme-full"}}).
		WithScopedRoleTokens(map[string][]string{"beta": {"beta-read"}}, nil)

	cases := []struct {
		name    string
		token   string
		wantID  string
		wantAll bool
		why     string
	}{
		{"global full token", "op", "", true,
			"the operator's scrape is the only per-client volume signal operations has"},
		{"global read-only token", "reader", "", true,
			"a global read token already reads every tenant's graph"},
		{"tenant-scoped full token", "acme-full", "acme", false, ""},
		{"tenant-scoped read token", "beta-read", "beta", false, ""},
		{"no bearer", "", "", false,
			"fails closed: the middleware rejects this before the handler, so reaching it means something is wrong"},
		{"unknown bearer", "nobody", "", false, "fails closed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, all := a.MetricsScope(scrapeReq(c.token))
			if id != c.wantID || all != c.wantAll {
				t.Errorf("MetricsScope = (%q, %v), want (%q, %v). %s", id, all, c.wantID, c.wantAll, c.why)
			}
		})
	}
}

// TestMetricsScopeIgnoresClientSuppliedTenant is why this does not reuse
// EffectiveTenantHTTP: that one falls back to the client's own header, which
// would let a scoped caller widen its scope by naming another tenant.
func TestMetricsScopeIgnoresClientSuppliedTenant(t *testing.T) {
	a := NewWithRoles(nil, nil, nil, map[string][]string{"acme": {"acme-full"}})

	r := scrapeReq("acme-full")
	r.Header.Set("X-Scope-OrgID", "victim")
	if id, all := a.MetricsScope(r); id != "acme" || all {
		t.Errorf("MetricsScope = (%q, %v), want (\"acme\", false): a header must not widen a token's scope", id, all)
	}

	q := httptest.NewRequest(http.MethodGet, "/metrics?tenant=victim", nil)
	q.Header.Set("Authorization", "Bearer acme-full")
	if id, all := a.MetricsScope(q); id != "acme" || all {
		t.Errorf("MetricsScope = (%q, %v), want (\"acme\", false): ?tenant= decided nothing here even before the fix", id, all)
	}
}

// TestMetricsScopeDisabledAuthIsOpen keeps the trusted-network default working:
// with no tokens configured there is nobody to scope to, and a scrape that
// suddenly returned nothing would be a silent monitoring outage.
func TestMetricsScopeDisabledAuthIsOpen(t *testing.T) {
	if id, all := NewWithRoles(nil, nil, nil, nil).MetricsScope(scrapeReq("")); id != "" || !all {
		t.Errorf("MetricsScope = (%q, %v), want (\"\", true) when auth is disabled", id, all)
	}
}

// TestMetricsScopeOIDCClaimIsOneTenant: a verified claim names one tenant, and
// the claim is resolved by the middleware into the request context.
func TestMetricsScopeOIDCClaimIsOneTenant(t *testing.T) {
	a := NewWithRoles([]string{"op"}, nil, nil, nil)
	r := scrapeReq("op").WithContext(context.WithValue(context.Background(), resolvedTenantKey, "claimed"))
	if id, all := a.MetricsScope(r); id != "claimed" || all {
		t.Errorf("MetricsScope = (%q, %v), want (\"claimed\", false): a claim-derived tenant is never widened", id, all)
	}
}
