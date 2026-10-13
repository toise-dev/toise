package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

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
//
// That is not enough on its own, and the gap is worth stating: if scraping
// fails the series vanishes, and an alert on `> 0` does not fire — it goes
// quiet. So the evaluation timestamp is published beside the total. A fresh
// timestamp proves the list was walked; a zero total proves only that something
// once wrote a zero. Alert on both: `conditions > 0`, and absence of the total.
//
// A DECLARATION is published beside the conditions, always and as 0 or 1. Some
// conditions this check reports cannot be read from inside the process: whether
// a listener is reachable depends on the port publication and on a proxy the
// process cannot see. The operator can say so, and then the claim is the thing
// that must be auditable — a setting whose only effect is to quiet a control is
// otherwise indistinguishable from the control working. So the declaration is
// exported rather than merely honored: a reviewer can see that a clean instance
// is clean because nothing is exposed, or clean because someone said so.
type Exposure struct {
	conditions   *prometheus.GaugeVec // surface, addr, reason
	declarations *prometheus.GaugeVec // declaration
	total        prometheus.Gauge
	lastCheck    prometheus.Gauge
	now          func() time.Time
}

// Declaration is a statement the operator makes about the perimeter, which the
// process cannot verify. Name is the setting; On is what it is set to.
type Declaration struct {
	Name string
	On   bool
}

// NewExposure builds the exposure instruments.
func NewExposure() *Exposure {
	return &Exposure{
		conditions: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "toise_insecure_config",
			Help: "One per configuration condition that leaves data readable or unauthenticated on the network, by surface (listener, log_shipping), address, and reason. Always 1 when present.",
		}, []string{"surface", "addr", "reason"}),
		declarations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "toise_config_declaration",
			Help: "One per operator declaration about the perimeter that this process cannot verify, by declaration. Published as 0 or 1 always, so a declaration that suppresses a condition is visible rather than inferred from a missing series.",
		}, []string{"declaration"}),
		total: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "toise_insecure_config_conditions",
			Help: "How many insecure configuration conditions this instance has. Zero is published too, so alerting on >0 needs no series to exist first.",
		}),
		lastCheck: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "toise_insecure_config_last_check_timestamp_seconds",
			Help: "When the conditions were last evaluated, in Unix seconds. A fresh value proves the list was walked; a zero total alone only proves something wrote a zero.",
		}),
		now: time.Now,
	}
}

// Collectors returns the underlying collectors for registration.
func (e *Exposure) Collectors() []prometheus.Collector {
	return []prometheus.Collector{e.conditions, e.declarations, e.total, e.lastCheck}
}

// Report publishes the conditions found at startup, and the declarations that
// were taken into account in finding them. Configuration does not change under a
// running process, so this is called once; calling it again replaces nothing and
// is not intended.
//
// Every declaration passed is published whether it is on or off: a reader asking
// "is this instance quiet because it is safe, or because someone said it was"
// needs the answer to exist, and a series that appears only when the declaration
// is set makes its absence mean both "not declared" and "not scraped".
func (e *Exposure) Report(cs []Condition, ds []Declaration) {
	if e == nil {
		return
	}
	for _, c := range cs {
		e.conditions.WithLabelValues(c.Surface, c.Addr, c.Reason).Set(1)
	}
	for _, d := range ds {
		v := 0.0
		if d.On {
			v = 1
		}
		e.declarations.WithLabelValues(d.Name).Set(v)
	}
	e.total.Set(float64(len(cs)))
	e.lastCheck.Set(float64(e.now().Unix()))
}
