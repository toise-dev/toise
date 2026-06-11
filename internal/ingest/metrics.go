package ingest

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the hot-path ingest counters: unlike the scrape-time state
// collector (internal/metrics, #44), these count events that cannot be
// reconstructed from live state — export outcomes, per-record results, tenant
// rejections, dropped attribute values. Without them a stalled or erroring
// producer is invisible until the liveness sweep starts deleting its entities
// (#113). A nil *Metrics is valid and counts nothing.
type Metrics struct {
	exports          *prometheus.CounterVec // outcome: ok|error
	records          *prometheus.CounterVec // result: handled|ignored|rejected
	droppedValues    prometheus.Counter
	tenantRejections prometheus.Counter
	unknownTypes     prometheus.Counter
}

// NewMetrics builds the ingest counters, pre-registering every label value so
// the series exist (at zero) from the first scrape.
func NewMetrics() *Metrics {
	m := &Metrics{
		exports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_ingest_exports_total",
			Help: "OTLP export requests, by outcome (ok, error).",
		}, []string{"outcome"}),
		records: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_ingest_records_total",
			Help: "Log records seen at ingest, by result (handled entity events, ignored non-entity records, rejected contract violations).",
		}, []string{"result"}),
		droppedValues: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_ingest_attr_values_dropped_total",
			Help: "Non-scalar attribute values dropped at the ingest boundary.",
		}),
		tenantRejections: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_ingest_tenant_rejections_total",
			Help: "Exports rejected for an invalid tenant id (X-Scope-OrgID metadata or tenant.id resource attribute).",
		}),
		unknownTypes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_ingest_unknown_type_records_total",
			Help: "Records accepted with an entity type outside the built-in registry (accept_unknown_types).",
		}),
	}
	for _, v := range []string{"ok", "error"} {
		m.exports.WithLabelValues(v)
	}
	for _, v := range []string{"handled", "ignored", "rejected"} {
		m.records.WithLabelValues(v)
	}
	return m
}

// Collectors returns the underlying collectors for registration on a
// Prometheus registry (alongside the scrape-time state collector).
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.exports, m.records, m.droppedValues, m.tenantRejections, m.unknownTypes}
}

func (m *Metrics) export(ok bool) {
	if m == nil {
		return
	}
	outcome := "ok"
	if !ok {
		outcome = "error"
	}
	m.exports.WithLabelValues(outcome).Inc()
}

func (m *Metrics) addRecords(result string, n int) {
	if m == nil || n == 0 {
		return
	}
	m.records.WithLabelValues(result).Add(float64(n))
}

func (m *Metrics) addDroppedValues(n int) {
	if m == nil || n == 0 {
		return
	}
	m.droppedValues.Add(float64(n))
}

func (m *Metrics) unknownTypeAccepted() {
	if m == nil {
		return
	}
	m.unknownTypes.Inc()
}

func (m *Metrics) tenantRejected() {
	if m == nil {
		return
	}
	m.tenantRejections.Inc()
}
