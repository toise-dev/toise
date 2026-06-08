// Package ops provides the operational HTTP endpoints — liveness and readiness
// probes — that orchestrators and uptime monitors target directly, so a
// deployment no longer needs a synthetic httpcheck against a UI page. See #44.
package ops

import (
	"net/http"
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

// Readyz is a readiness probe: it returns 200 when ready() reports nil, and 503
// with the reason otherwise. ready bundles the dependency checks (projection
// rebuilt, store reachable) so the handler stays transport-only.
func Readyz(ready func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := ready(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready: " + err.Error() + "\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	}
}
