package ingest

import "github.com/prometheus/client_golang/prometheus"

// Metrics are the hot-path ingest counters: unlike the scrape-time state
// collector (internal/metrics, #44), these count events that cannot be
// reconstructed from live state — export outcomes, per-record results, tenant
// rejections, dropped attribute values. Without them a stalled or erroring
// producer is invisible until the liveness sweep starts deleting its entities
// (#113). A nil *Metrics is valid and counts nothing.
//
// The per-record counters carry the tenant, because on a multi-tenant server a
// bare total answers the wrong question: it says that something, somewhere, was
// refused, and the operator's question is whether THEIR tenant's data is
// getting in (#405). A total at zero still proves the absence for every tenant;
// it is a non-zero total that cannot be attributed.
type Metrics struct {
	// exports counts requests, not records, and a request is not per-tenant: one
	// OTLP stream can carry several tenants via the tenant.id resource attribute,
	// and a request rejected before tenant resolution has none at all. Labeling
	// it would invent an attribution the data does not support.
	exports *prometheus.CounterVec // outcome: ok|error
	records *prometheus.CounterVec // tenant, result: handled|ignored|rejected
	// droppedValues and unknownTypes are per-record, so they carry the tenant.
	droppedValues *prometheus.CounterVec // tenant
	unknownTypes  *prometheus.CounterVec // tenant
	// tenantRejections stays unlabeled ON PURPOSE. It counts ids that FAILED
	// validation, and on an open server that value is caller-supplied: labeling
	// it would hand an unauthenticated caller a way to mint unbounded series.
	tenantRejections prometheus.Counter
}

// NewMetrics builds the ingest counters, pre-registering every label value so
// the series exist (at zero) from the first scrape. Tenant-labeled series
// cannot be pre-registered here — the tenants are not known yet — so ensure
// registers them the first time a tenant is seen at ingest, which keeps the
// same property from that moment on: a tenant that has ever been seen has all
// its result series, and a zero is then a measured zero rather than a gap.
func NewMetrics() *Metrics {
	m := &Metrics{
		exports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_ingest_exports_total",
			Help: "OTLP export requests, by outcome (ok, error).",
		}, []string{"outcome"}),
		records: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_ingest_records_total",
			Help: "Log records seen at ingest, by result (handled entity events, ignored non-entity records, rejected contract violations).",
		}, []string{"tenant", "result"}),
		droppedValues: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_ingest_attr_values_dropped_total",
			Help: "Non-scalar attribute values dropped at the ingest boundary, by tenant.",
		}, []string{"tenant"}),
		tenantRejections: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_ingest_tenant_rejections_total",
			Help: "Exports rejected for an invalid tenant id (X-Scope-OrgID metadata or tenant.id resource attribute). Deliberately unlabeled: the rejected id is caller-supplied.",
		}),
		unknownTypes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_ingest_unknown_type_records_total",
			Help: "Records accepted with an entity type outside the built-in registry (accept_unknown_types), by tenant.",
		}, []string{"tenant"}),
	}
	for _, v := range []string{"ok", "error"} {
		m.exports.WithLabelValues(v)
	}
	return m
}

// recordResults are the values of the records counter's result label, listed so
// ensure can pre-register all of them for a tenant at once.
var recordResults = [...]string{"handled", "ignored", "rejected"}

// ensure creates a tenant's series at zero, so that a tenant which has been
// seen but has had nothing rejected reads as a measured zero instead of an
// absent series. A missing series and a zero one mean very different things to
// whoever reads the answer, and only one of them is a success.
func (m *Metrics) ensure(tenant string) {
	if m == nil || tenant == "" {
		return
	}
	for _, r := range recordResults {
		m.records.WithLabelValues(tenant, r)
	}
	m.droppedValues.WithLabelValues(tenant)
	m.unknownTypes.WithLabelValues(tenant)
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

func (m *Metrics) addRecords(tenant, result string, n int) {
	if m == nil || n == 0 {
		return
	}
	m.records.WithLabelValues(tenant, result).Add(float64(n))
}

func (m *Metrics) addDroppedValues(tenant string, n int) {
	if m == nil || n == 0 {
		return
	}
	m.droppedValues.WithLabelValues(tenant).Add(float64(n))
}

func (m *Metrics) unknownTypeAccepted(tenant string) {
	if m == nil {
		return
	}
	m.unknownTypes.WithLabelValues(tenant).Inc()
}

func (m *Metrics) tenantRejected() {
	if m == nil {
		return
	}
	m.tenantRejections.Inc()
}
