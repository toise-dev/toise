package ops

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	Healthz()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body = %q, want it to contain ok", rec.Body.String())
	}
}

func TestReadyzReady(t *testing.T) {
	rec := httptest.NewRecorder()
	Readyz(func() error { return nil }, nil)(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestReadyzSaysWhetherNotWhy replaces a test that asserted the opposite, and the
// reversal is the point rather than a detail.
//
// /readyz is deliberately unauthenticated: a load balancer and an orchestrator
// must reach it without credentials, which is why it exists. So everything it
// writes, it writes to strangers. It used to return the reason, and in this
// server the reason is `fmt.Errorf("tenant %s: %w", tenant, storeErr)` — an
// anonymous caller learned a customer identifier and an internal failure mode,
// exactly when nobody was watching the instance.
//
// The status code is the probe's contract. The reason goes to the log.
func TestReadyzSaysWhetherNotWhy(t *testing.T) {
	// The real shape of this server's readiness error, not a placeholder: a
	// generic "store down" would let this test pass while the tenant leaked.
	boom := fmt.Errorf("tenant %s: %w", "acme-industries", errors.New("pebble: background error: no space left on device"))

	var logged []error
	rec := httptest.NewRecorder()
	h := Readyz(func() error { return boom }, func(err error) { logged = append(logged, err) })
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"acme-industries", "pebble", "no space left"} {
		if strings.Contains(body, leak) {
			t.Errorf("the body discloses %q to an unauthenticated caller: %q", leak, body)
		}
	}
	// And it still tells the operator where to look, or the status code alone
	// sends them reading source.
	if !strings.Contains(body, "not ready") {
		t.Errorf("body = %q, want it to say it is not ready", body)
	}
	// The reason is not discarded — that would trade a disclosure for a blind
	// spot, which is the same trade the other way round.
	if len(logged) != 1 || logged[0] == nil || !strings.Contains(logged[0].Error(), "acme-industries") {
		t.Fatalf("the reason did not reach the reporter: %v", logged)
	}
}

// TestReadyzReportsTransitionsOnly guards the reason reaching the log without a
// probe polled every few seconds flooding it, and guards the recovery line,
// which is the one an operator actually waits for.
func TestReadyzReportsTransitionsOnly(t *testing.T) {
	var state error
	var reports []string
	h := Readyz(func() error { return state }, func(err error) {
		if err == nil {
			reports = append(reports, "recovered")
			return
		}
		reports = append(reports, err.Error())
	})
	call := func() {
		h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	}

	call() // ready, no transition from the initial ready state
	state = errors.New("store down")
	call()
	call()
	call() // still down: must not re-report
	state = nil
	call()
	call() // ready again: one line, not two

	want := []string{"store down", "recovered"}
	if len(reports) != len(want) {
		t.Fatalf("reports = %v, want exactly %v — a probe polled every few seconds would otherwise fill the log with one unchanging line", reports, want)
	}
	for i := range want {
		if reports[i] != want[i] {
			t.Errorf("report %d = %q, want %q", i, reports[i], want[i])
		}
	}

	// A reason that CHANGES is news, even while still not ready: a second tenant
	// failing is not the same event as the first one still failing.
	reports = nil
	state = errors.New("store down")
	call()
	state = errors.New("another store down")
	call()
	if len(reports) != 2 {
		t.Errorf("reports = %v, want both reasons: a changed reason is a new fact", reports)
	}
}
