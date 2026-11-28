package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ConfigReloads tracks configuration reload attempts and the time of the last successful reload.
type ConfigReloads struct {
	reloads  *prometheus.CounterVec
	lastSucc prometheus.Gauge
}

func NewConfigReloads() *ConfigReloads {
	reloads := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "toise_config_reloads_total",
			Help: "Configuration reload attempts by outcome: applied, partial (applied with ignored keys), or rejected.",
		},
		[]string{"outcome"},
	)
	reloads.WithLabelValues("applied")
	reloads.WithLabelValues("partial")
	reloads.WithLabelValues("rejected")

	return &ConfigReloads{
		reloads: reloads,
		lastSucc: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "toise_config_last_reload_success_timestamp_seconds",
			Help: "Unix timestamp in seconds of the last configuration reload that was applied, in whole or in part.",
		}),
	}
}

func (c *ConfigReloads) Record(outcome string, at time.Time) {
	if c == nil {
		return
	}
	switch outcome {
	case "applied", "partial":
	case "rejected":
	default:
		outcome = "rejected"
	}
	c.reloads.WithLabelValues(outcome).Inc()
	if outcome == "applied" || outcome == "partial" {
		c.lastSucc.Set(float64(at.Unix()))
	}
}

func (c *ConfigReloads) Collectors() []prometheus.Collector {
	if c == nil {
		return nil
	}
	return []prometheus.Collector{c.reloads, c.lastSucc}
}
