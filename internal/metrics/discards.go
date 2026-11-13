package metrics

import "github.com/prometheus/client_golang/prometheus"

// Discards counts edges given up on because an endpoint never arrived within
// the reconciliation window.
//
// It exists because a production tenant discarded 47,327 `attached_to` edges in
// ten days — every one of them, because the probe that emits their target was
// enabled nowhere — and the graph said nothing about it. No relation, no target
// entity, no sign a thing had been attempted. The only trace was a WARN line,
// found because someone was told which log to search.
//
// The labels are the three fields that log line already carried, and they are
// what makes the figure diagnose itself: relation type, and the type at each
// end. `attached_to / container / network.segment` names the producer's shape
// and the missing half at once, with nobody needing to know where to look.
//
// Plus the tenant, which 0.22 shipped without and the bench's GO noted as a
// reserve: a figure that cannot say which client it is about sends an operator
// to the logs to find out, which is the errand this instrument exists to spare
// them. The attribution comes from the registry — an engine serves one tenant
// and holds no handle on which.
//
// Cardinality is bounded by the registry, which is small and closed, times the
// tenants, which are few and administered.
type Discards struct {
	dropped *prometheus.CounterVec // tenant, relation_type, from_type, to_type
	count   prometheus.Counter
}

// NewDiscards builds the discard instruments.
func NewDiscards() *Discards {
	return &Discards{
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_discarded_relations_total",
			Help: "Edges dropped because an endpoint never arrived within the reconciliation window, by relation type and the type at each end. A sustained rate means a producer asserts one half of a pair whose other half nothing emits — the edge is re-made and re-discarded every cycle, and the graph shows none of it.",
		}, []string{"tenant", "relation_type", "from_type", "to_type"}),
		// Deliberately NOT labeled by tenant. Its whole purpose is to exist at
		// zero from the first scrape, so that an alert on an increase needs no
		// series to appear first — and a tenant-labeled series cannot have that
		// property, since it only comes into being once that tenant has
		// discarded something. Instance-wide here, per-tenant above: the two
		// answer different questions and only one of them can be answered by an
		// absent series.
		count: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_discarded_relations_count",
			Help: "How many edges this instance has discarded since start, across every tenant. Published as zero too, so alerting on an increase needs no series to exist first; use toise_discarded_relations_total for the per-tenant breakdown.",
		}),
	}
}

// Collectors returns the underlying collectors for registration.
func (d *Discards) Collectors() []prometheus.Collector {
	return []prometheus.Collector{d.dropped, d.count}
}

// Dropped records one discarded edge. A nil *Discards records nothing, so a
// caller without metrics needs no guard.
func (d *Discards) Dropped(tenantID, relationType, fromType, toType string) {
	if d == nil {
		return
	}
	d.dropped.WithLabelValues(tenantOrUnknown(tenantID), relationType, fromType, toType).Inc()
	d.count.Inc()
}

// tenantOrUnknown keeps a row rather than dropping it when the tenant is empty,
// for the reason Promises.Refused keeps an unnamed producer: a figure that is
// hard to attribute is still a figure, and the one case worth seeing is exactly
// the one whose provenance went missing. An empty tenant should be impossible —
// the registry always has an id — so a row under "unknown" is also the only
// signal that it happened.
func tenantOrUnknown(tenantID string) string {
	if tenantID == "" {
		return "unknown"
	}
	return tenantID
}
