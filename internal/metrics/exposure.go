package metrics

import "github.com/prometheus/client_golang/prometheus"

// Condition is one configuration choice that leaves data readable or
// unauthenticated on the network: which surface, which address, and why.
type Condition struct {
	Surface string // listener, log_shipping
	Addr    string
	Reason  string // no_auth, no_tls, no_auth_no_tls, cleartext_public, cleartext
}

// Exposure publishes those conditions as metrics.
//
// They are already logged at startup, and that was not enough: an insecure
// shipping target and two unauthenticated listeners each sat in a production
// log for months because a WARN line wakes nobody. A gauge can be alerted on,
// which is the difference between a warning and a control.
//
// The total is published unlabeled and always, including as zero, so a clean
// deployment is a visible fact rather than an absent series — absence would
// otherwise read the same as a failed scrape.
type Exposure struct {
	conditions *prometheus.GaugeVec // surface, addr, reason
	total      prometheus.Gauge
}

// NewExposure builds the exposure instruments.
func NewExposure() *Exposure {
	return &Exposure{
		conditions: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "toise_insecure_config",
			Help: "One per configuration condition that leaves data readable or unauthenticated on the network, by surface (listener, log_shipping), address, and reason. Always 1 when present.",
		}, []string{"surface", "addr", "reason"}),
		total: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "toise_insecure_config_conditions",
			Help: "How many insecure configuration conditions this instance has. Zero is published too, so alerting on >0 needs no series to exist first.",
		}),
	}
}

// Collectors returns the underlying collectors for registration.
func (e *Exposure) Collectors() []prometheus.Collector {
	return []prometheus.Collector{e.conditions, e.total}
}

// Report publishes the conditions found at startup. Configuration does not
// change under a running process, so this is called once; calling it again
// replaces nothing and is not intended.
func (e *Exposure) Report(cs []Condition) {
	if e == nil {
		return
	}
	for _, c := range cs {
		e.conditions.WithLabelValues(c.Surface, c.Addr, c.Reason).Set(1)
	}
	e.total.Set(float64(len(cs)))
}
