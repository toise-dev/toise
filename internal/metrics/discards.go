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
// Cardinality is bounded by the registry, which is small and closed.
type Discards struct {
	dropped *prometheus.CounterVec // relation_type, from_type, to_type
	count   prometheus.Counter
}

// NewDiscards builds the discard instruments.
func NewDiscards() *Discards {
	return &Discards{
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_discarded_relations_total",
			Help: "Edges dropped because an endpoint never arrived within the reconciliation window, by relation type and the type at each end. A sustained rate means a producer asserts one half of a pair whose other half nothing emits — the edge is re-made and re-discarded every cycle, and the graph shows none of it.",
		}, []string{"relation_type", "from_type", "to_type"}),
		count: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_discarded_relations_count",
			Help: "How many edges this instance has discarded since start. Published as zero too, so alerting on an increase needs no series to exist first.",
		}),
	}
}

// Collectors returns the underlying collectors for registration.
func (d *Discards) Collectors() []prometheus.Collector {
	return []prometheus.Collector{d.dropped, d.count}
}

// Dropped records one discarded edge. A nil *Discards records nothing, so a
// caller without metrics needs no guard.
func (d *Discards) Dropped(relationType, fromType, toType string) {
	if d == nil {
		return
	}
	d.dropped.WithLabelValues(relationType, fromType, toType).Inc()
	d.count.Inc()
}
