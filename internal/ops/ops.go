// Package ops provides the operational HTTP endpoints — liveness and readiness
// probes — that orchestrators and uptime monitors target directly, so a
// deployment no longer needs a synthetic httpcheck against a UI page. See #44.
package ops

import (
	"net/http"
	"sync"
)

// Healthz is a liveness probe: it returns 200 as long as the HTTP server is
// serving requests. It does not check dependencies — use Readyz for that.
func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}

// Readyz is a readiness probe: 200 when ready() reports nil, 503 otherwise.
// ready bundles the dependency checks (projection rebuilt, store reachable) so
// the handler stays transport-only.
//
// The body says only WHETHER, never why. This endpoint is deliberately
// unauthenticated — a load balancer and an orchestrator must reach it without
// credentials, and that is the whole point of it existing — so everything it
// writes is written to strangers. It used to return the reason, and the reason
// carried the failing tenant's identifier and the storage engine's error text:
// an anonymous caller learned a customer name and an internal failure mode
// exactly when nobody was watching the instance. The status code is the probe's
// contract; the reason belongs where it can be read with authorization.
//
// onChange is called with the reason whenever readiness FLIPS — with the error
// when it breaks, with nil when it recovers — so the detail reaches the log
// without a probe polled every few seconds flooding it. The transition is also
// the more useful line: an operator wants to know when it started, not that it
// is still true.
func Readyz(ready func() error, onChange func(error)) http.HandlerFunc {
	var mu sync.Mutex
	var last string
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		err := ready()
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		mu.Lock()
		changed := reason != last
		last = reason
		mu.Unlock()
		if changed && onChange != nil {
			onChange(err)
		}
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			// Where to look, not what is wrong: useful to the operator running
			// curl, and nothing to the stranger running the same command.
			_, _ = w.Write([]byte("not ready (the reason is in the server log and in /metrics)\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	}
}
