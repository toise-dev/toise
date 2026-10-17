package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// perTenantFixture is a counter shaped like the ingest counters that motivated
// #463: one series per tenant, so one unfiltered answer names every client.
func perTenantFixture() *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "toise_test_records_total",
		Help: "Records per tenant, for the scope test.",
	}, []string{"tenant", "result"})
	c.WithLabelValues("1", "handled").Add(11)
	c.WithLabelValues("3", "handled").Add(33)
	c.WithLabelValues("5", "handled").Add(55)
	return c
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestScopedHandlerServesOnlyOneTenant is the fix for #463: a tenant-scoped
// caller must not be able to count the other clients on the instance.
func TestScopedHandlerServesOnlyOneTenant(t *testing.T) {
	h := ScopedHandler(func(*http.Request) (string, bool) { return "3", false },
		NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"), perTenantFixture())
	body := scrape(t, h)

	if !strings.Contains(body, `toise_test_records_total{result="handled",tenant="3"} 33`) {
		t.Errorf("own tenant's series missing:\n%s", body)
	}
	for _, other := range []string{`tenant="1"`, `tenant="5"`} {
		if strings.Contains(body, "toise_test_records_total{result=\"handled\","+other) {
			t.Errorf("another tenant's series served (%s):\n%s", other, body)
		}
	}
	// The server-wide families go with them: entity and relation counts, store
	// size and the Go runtime are facts about every tenant at once, and their
	// mere presence tells a scoped reader that others exist.
	for _, leak := range []string{"toise_entities ", "toise_relations ", "toise_store_disk_bytes", "go_goroutines"} {
		if strings.Contains(body, leak) {
			t.Errorf("server-wide series %q served to a scoped caller:\n%s", leak, body)
		}
	}
}

// TestScopedHandlerGlobalTokenSeesEverything protects the reason the tenant
// label is kept at all: the operator's single scrape is the only per-client
// volume signal operations has, and scoping it to nothing would destroy it.
func TestScopedHandlerGlobalTokenSeesEverything(t *testing.T) {
	h := ScopedHandler(func(*http.Request) (string, bool) { return "", true },
		NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"), perTenantFixture())
	body := scrape(t, h)

	for _, want := range []string{`tenant="1"`, `tenant="3"`, `tenant="5"`, "toise_entities ", "go_goroutines"} {
		if !strings.Contains(body, want) {
			t.Errorf("global scrape missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, scopeInfoName) {
		t.Errorf("%s belongs on scoped answers only, it would break a series that exists for some scrapes and not others", scopeInfoName)
	}
}

// TestScopedHandlerEmptyAnswerSaysSo is the rule this release exists for: an
// empty answer must be distinguishable from a broken one. A tenant with no
// series yet and an exporter that failed are otherwise the same empty body.
func TestScopedHandlerEmptyAnswerSaysSo(t *testing.T) {
	h := ScopedHandler(func(*http.Request) (string, bool) { return "nobody", false },
		NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"), perTenantFixture())
	body := scrape(t, h)

	if !strings.Contains(body, scopeInfoName+`{tenant="nobody"} 1`) {
		t.Errorf("an empty scoped answer must still name its scope:\n%s", body)
	}
	if strings.Contains(body, "toise_test_records_total{") {
		t.Errorf("no tenant series should match:\n%s", body)
	}
}

// TestScopedHandlerUnresolvedScopeGetsNothing covers the fail-closed case: the
// middleware authenticates before the handler runs, so an unresolvable scope
// means a token bound to several tenants. Serving it everything is the exact
// disclosure the fix removes.
func TestScopedHandlerUnresolvedScopeGetsNothing(t *testing.T) {
	h := ScopedHandler(func(*http.Request) (string, bool) { return "", false },
		NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"), perTenantFixture())
	body := scrape(t, h)

	if strings.Contains(body, "toise_test_records_total{") {
		t.Errorf("an unresolved scope must match no tenant series:\n%s", body)
	}
	if !strings.Contains(body, scopeInfoName+`{tenant=""} 1`) {
		t.Errorf("even the empty scope must be stated, not left to look like a failure:\n%s", body)
	}
}

// TestHandlerWithoutScopeIsUnchanged keeps the nil-scope path byte-compatible,
// because metrics_public rides on it for the length of an upgrade.
func TestHandlerWithoutScopeIsUnchanged(t *testing.T) {
	body := scrape(t, Handler(NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123"), perTenantFixture()))

	for _, want := range []string{`tenant="1"`, `tenant="5"`, "toise_entities ", "go_goroutines"} {
		if !strings.Contains(body, want) {
			t.Errorf("unscoped scrape missing %q:\n%s", want, body)
		}
	}
}
