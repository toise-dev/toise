package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func findSeries(t *testing.T, reg *prometheus.Registry, name, outcome string) *dto.Metric {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "outcome" && lp.GetValue() == outcome {
					return m
				}
			}
		}
	}
	return nil
}

func TestConfigReloadsFreshRegistryHasAllOutcomesAtZero(t *testing.T) {
	t.Parallel()
	c := NewConfigReloads()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c.Collectors()...)
	got := labelsOf(t, reg, "toise_config_reloads_total")
	if len(got) != 3 {
		t.Fatalf("got %d series, want 3", len(got))
	}
	if sum := sumFamily(t, reg, "toise_config_reloads_total"); sum != 0 {
		t.Fatalf("sum = %v, want 0", sum)
	}
}

func TestConfigReloadsRecordIncrementsOnlyItsOutcome(t *testing.T) {
	t.Parallel()
	base := time.Unix(1000000000, 0)
	cases := []struct {
		outcome string
	}{
		{outcome: "applied"},
		{outcome: "partial"},
		{outcome: "rejected"},
	}
	for _, tc := range cases {
		c := NewConfigReloads()
		reg := prometheus.NewRegistry()
		reg.MustRegister(c.Collectors()...)
		c.Record(tc.outcome, base)
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatalf("Gather: %v", err)
		}
		total := 0
		nonZero := 0
		for _, mf := range mfs {
			if mf.GetName() != "toise_config_reloads_total" {
				continue
			}
			for _, m := range mf.GetMetric() {
				total++
				if m.GetCounter().GetValue() != 0 {
					nonZero++
				}
			}
		}
		if total != 3 || nonZero != 1 {
			t.Fatalf("outcome %q: %d series, %d non-zero, want 3 series and 1 non-zero", tc.outcome, total, nonZero)
		}
		m := findSeries(t, reg, "toise_config_reloads_total", tc.outcome)
		if m == nil {
			t.Fatalf("outcome %q: series not found", tc.outcome)
		}
		if m.GetCounter().GetValue() != 1 {
			t.Fatalf("outcome %q: value = %v, want 1", tc.outcome, m.GetCounter().GetValue())
		}
	}
}

func TestConfigReloadsUnknownOutcomeCountsAsRejected(t *testing.T) {
	t.Parallel()
	c := NewConfigReloads()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c.Collectors()...)
	c.Record("fake-unknown-outcome-1", time.Unix(1000000000, 0))
	m := findSeries(t, reg, "toise_config_reloads_total", "rejected")
	if m == nil {
		t.Fatal("rejected series not found")
	}
	if m.GetCounter().GetValue() != 1 {
		t.Fatalf("rejected value = %v, want 1", m.GetCounter().GetValue())
	}
	if got := labelsOf(t, reg, "toise_config_reloads_total"); len(got) != 3 {
		t.Fatalf("got %d series, want 3", len(got))
	}
}

func TestConfigReloadsGaugeTimestamp(t *testing.T) {
	t.Parallel()
	ts := time.Unix(2000000000, 0)
	cases := []struct {
		outcome string
		want    float64
	}{
		{outcome: "applied", want: float64(2000000000)},
		{outcome: "partial", want: float64(2000000000)},
		{outcome: "rejected", want: 0},
	}
	for _, tc := range cases {
		c := NewConfigReloads()
		reg := prometheus.NewRegistry()
		reg.MustRegister(c.Collectors()...)
		c.Record(tc.outcome, ts)
		m := findSeries(t, reg, "toise_config_last_reload_success_timestamp_seconds", "")
		if m == nil {
			mfs, err := reg.Gather()
			if err != nil {
				t.Fatalf("Gather: %v", err)
			}
			for _, mf := range mfs {
				if mf.GetName() == "toise_config_last_reload_success_timestamp_seconds" {
					for _, mm := range mf.GetMetric() {
						m = mm
					}
				}
			}
			if m == nil {
				t.Fatalf("outcome %q: gauge series not found", tc.outcome)
			}
		}
		if got := m.GetGauge().GetValue(); got != tc.want {
			t.Fatalf("outcome %q: gauge = %v, want %v", tc.outcome, got, tc.want)
		}
	}
}

func TestConfigReloadsNilReceiverNoOp(t *testing.T) {
	t.Parallel()
	var c *ConfigReloads
	c.Record("applied", time.Unix(1000000000, 0))
	if got := c.Collectors(); got != nil {
		t.Fatalf("Collectors on nil receiver = %v, want nil", got)
	}
}
