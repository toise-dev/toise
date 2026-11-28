package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ProducerSeen records the last time a producer was observed on this
// replica's clock, scoped to a tenant and a collection method.
type ProducerSeen struct {
	Tenant   string
	Producer string
	Scope    string
	LastSeen time.Time
}

// NewProducerLastSeen returns a prometheus.Collector exposing a gauge of the
// Unix timestamp at which each producer last spoke. The source function is
// invoked at scrape time; a nil source or empty result emits nothing.
func NewProducerLastSeen(src func() []ProducerSeen) prometheus.Collector {
	return &producerLastSeenCollector{src: src}
}

type producerLastSeenCollector struct {
	src func() []ProducerSeen
}

var producerLastSeenDesc = prometheus.NewDesc(
	"toise_producer_last_seen_timestamp_seconds",
	"Unix time at which a producer last spoke, by tenant, producer and collection method (scope is empty when unknown). Measured on this replica's clock at observation, so it is absent for a replica that only receives the shipped log. A silent producer is not necessarily down. Series leave when the speaker's last liveness reference is swept.",
	[]string{"tenant", "producer", "scope"},
	nil,
)

func (c *producerLastSeenCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- producerLastSeenDesc
}

func (c *producerLastSeenCollector) Collect(ch chan<- prometheus.Metric) {
	if c.src == nil {
		return
	}
	for _, s := range c.src() {
		ch <- prometheus.MustNewConstMetric(
			producerLastSeenDesc,
			prometheus.GaugeValue,
			float64(s.LastSeen.UnixNano())/1e9,
			s.Tenant,
			s.Producer,
			s.Scope,
		)
	}
}
