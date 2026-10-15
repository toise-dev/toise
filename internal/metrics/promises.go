package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Promises counts liveness intervals a producer declared and the engine refused
// as impossible.
//
// It exists for the reason the exposure conditions exist: the refusal is
// already logged, and a WARN line wakes nobody. A producer that declares a
// 153-year cadence has a defect its operator cannot see from inside the graph —
// the entity simply ends up with no cadence, which reads like an honest absence
// of promise — so the only way anyone learns of it is a series to alert on.
//
// The count is published unlabeled and always, including as zero, so a clean
// fleet is a visible fact rather than an absent series: absence would otherwise
// read the same as a failed scrape.
type Promises struct {
	refused *prometheus.CounterVec // producer, entity_type
	count   prometheus.Counter
	worst   *prometheus.GaugeVec // producer, entity_type

	mu   sync.Mutex
	seen map[[2]string]float64
}

// NewPromises builds the promise instruments.
func NewPromises() *Promises {
	return &Promises{
		refused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "toise_refused_liveness_promises_total",
			Help: "Liveness intervals refused as impossible, by producer and entity type. A producer appearing here has a defect: the entities it asserts carry no cadence at all, which a reader cannot distinguish from an honest absence of promise.",
		}, []string{"producer", "entity_type"}),
		count: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "toise_refused_liveness_promises_count",
			Help: "How many liveness promises this instance has refused since start. Published as zero too, so alerting on an increase needs no series to exist first.",
		}),
		worst: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "toise_refused_liveness_promise_seconds",
			Help: "The LARGEST interval refused from this producer and entity type, in seconds, since start. The magnitude is the diagnosis: a few times the bound is a misconfiguration, a thousand times over is a unit error.",
		}, []string{"producer", "entity_type"}),
		seen: map[[2]string]float64{},
	}
}

// Collectors returns the underlying collectors for registration.
func (p *Promises) Collectors() []prometheus.Collector {
	return []prometheus.Collector{p.refused, p.count, p.worst}
}

// Refused records one refusal. A nil *Promises records nothing, so a caller
// without metrics needs no guard.
//
// An empty producer is reported as "unknown" rather than dropped: the
// production case that motivated this carried no producer id either, and losing
// the row would have hidden the worst instance there was.
//
// The gauge keeps the largest value seen rather than the latest, mirrored here
// because Prometheus gauges cannot be read back. A later, smaller refusal from
// the same producer must not hide the magnitude that identifies the cause.
func (p *Promises) Refused(producer, entityType string, declared time.Duration) {
	if p == nil {
		return
	}
	if producer == "" {
		producer = "unknown"
	}
	p.refused.WithLabelValues(producer, entityType).Inc()
	p.count.Inc()

	secs := declared.Seconds()
	key := [2]string{producer, entityType}
	p.mu.Lock()
	if secs > p.seen[key] {
		p.seen[key] = secs
		p.worst.WithLabelValues(producer, entityType).Set(secs)
	}
	p.mu.Unlock()
}
