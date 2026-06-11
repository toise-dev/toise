package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Maintenance instruments the per-tenant background loops (liveness sweep,
// heartbeat coalescing, retention pruning, snapshots): how often each op runs,
// whether it fails, and how long the last pass took. These carry a tenant
// label — the aggregate gauges sum across tenants, which is exactly what hides
// a single tenant's failing or slowing maintenance (#143). Cardinality is
// bounded by the open-tenant cap.
type Maintenance struct {
	runs     *prometheus.CounterVec // op, outcome, tenant
	duration *prometheus.GaugeVec   // op, tenant
}

// NewMaintenance builds the maintenance instruments.
func NewMaintenance() *Maintenance {
	return &Maintenance{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_maintenance_runs_total",
			Help: "Background maintenance passes, by op (sweep, coalesce, prune, snapshot), outcome (ok, error), and tenant.",
		}, []string{"op", "outcome", "tenant"}),
		duration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "toise_maintenance_last_duration_seconds",
			Help: "Duration of the most recent maintenance pass, by op and tenant.",
		}, []string{"op", "tenant"}),
	}
}

// Collectors returns the underlying collectors for registration.
func (m *Maintenance) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.runs, m.duration}
}

// Observe runs fn as one maintenance pass and records its outcome and
// duration. A nil *Maintenance observes nothing and just runs fn.
func (m *Maintenance) Observe(op, tenant string, fn func() error) error {
	if m == nil {
		return fn()
	}
	start := time.Now()
	err := fn()
	m.duration.WithLabelValues(op, tenant).Set(time.Since(start).Seconds())
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	m.runs.WithLabelValues(op, outcome, tenant).Inc()
	return err
}
