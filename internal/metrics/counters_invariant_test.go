package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// sumFamily adds up every series of one metric family on a registry. It fails
// the test when the family is absent, because an absent family and a family
// summing to zero are the distinction these instruments exist to preserve.
func sumFamily(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		var total float64
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				total += m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				total += m.GetGauge().GetValue()
			default:
				t.Fatalf("%s: unexpected metric kind %v", name, m)
			}
		}
		return total
	}
	t.Fatalf("metric family %q absent from the exposition", name)
	return 0
}

func labelsOf(t *testing.T, reg *prometheus.Registry, name string) [][]*dto.LabelPair {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		out := make([][]*dto.LabelPair, 0, len(mf.GetMetric()))
		for _, m := range mf.GetMetric() {
			out = append(out, m.GetLabel())
		}
		return out
	}
	t.Fatalf("metric family %q absent from the exposition", name)
	return nil
}

func hasLabel(pairs []*dto.LabelPair, name, value string) bool {
	for _, p := range pairs {
		if p.GetName() == name && p.GetValue() == value {
			return true
		}
	}
	return false
}

// TestLabeledVectorsSumToTheirUnlabeledCount is the executable form of the
// acceptance criterion the bench asked for, and it is stronger than comparing
// a dashboard's total across a version change: those are monotonic per-process
// counters, so the old and new series never coexist.
//
// Every increment goes to the labeled vector AND to the unlabeled count in the
// same call, so the two must agree at every instant on one instance. That makes
// the equality catch a lost row, a mis-filled label and a forgotten increment
// path — and it is what makes an empty tenant reported as "unknown" rather than
// DROPPED load-bearing: dropping the row would break this equality, which is
// precisely what turns a silent loss into a detectable one.
func TestLabeledVectorsSumToTheirUnlabeledCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := NewDiscards()
	p := NewPromises()
	reg.MustRegister(append(d.Collectors(), p.Collectors()...)...)

	// Zero before any incident: the property the _count companions exist for.
	for _, name := range []string{"toise_discarded_relations_count", "toise_refused_liveness_promises_count"} {
		if got := sumFamily(t, reg, name); got != 0 {
			t.Errorf("%s = %v before any incident, want 0 — published at zero is the point", name, got)
		}
	}

	d.Dropped("acme", "attached_to", "container", "network.segment")
	d.Dropped("acme", "attached_to", "container", "network.segment")
	d.Dropped("globex", "runs_on", "container", "host")
	d.Dropped("", "runs_on", "container", "host") // no tenant: kept as "unknown", never dropped

	p.Refused("acme", "agent-liar", "host", 9999*time.Hour)
	p.Refused("globex", "", "host", 5*time.Hour) // no producer: kept as "unknown"
	p.Refused("", "agent-liar", "host", time.Hour)

	for _, pair := range [][2]string{
		{"toise_discarded_relations_total", "toise_discarded_relations_count"},
		{"toise_refused_liveness_promises_total", "toise_refused_liveness_promises_count"},
	} {
		vec, count := sumFamily(t, reg, pair[0]), sumFamily(t, reg, pair[1])
		if math.Abs(vec-count) > 1e-9 {
			t.Errorf("sum(%s) = %v but %s = %v; every increment must reach both",
				pair[0], vec, pair[1], count)
		}
	}
}

// TestTenantLabelIsPresentAndNeverEmpty covers the bench's first criterion: the
// label must be there, and an absent tenant must still produce a row. A dropped
// row is the failure mode worth guarding, because it is invisible — the figure
// simply reads lower.
func TestTenantLabelIsPresentAndNeverEmpty(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := NewDiscards()
	p := NewPromises()
	reg.MustRegister(append(d.Collectors(), p.Collectors()...)...)

	d.Dropped("acme", "attached_to", "container", "network.segment")
	d.Dropped("", "runs_on", "container", "host")
	p.Refused("acme", "agent-liar", "host", 9999*time.Hour)
	p.Refused("", "agent-liar", "host", time.Hour)

	for _, name := range []string{
		"toise_discarded_relations_total",
		"toise_refused_liveness_promises_total",
		"toise_refused_liveness_promise_seconds",
	} {
		rows := labelsOf(t, reg, name)
		if len(rows) == 0 {
			t.Fatalf("%s: no series", name)
		}
		for _, pairs := range rows {
			if !hasLabel(pairs, "tenant", "acme") && !hasLabel(pairs, "tenant", "unknown") {
				t.Errorf("%s: a series carries neither tenant=acme nor tenant=unknown: %v", name, pairs)
			}
		}
		if !hasLabel(rows[0], "tenant", "acme") && !hasLabel(rows[0], "tenant", "unknown") {
			t.Errorf("%s: first series has no usable tenant label", name)
		}
	}

	// The _count companions stay unlabeled on purpose: a tenant-labeled series
	// cannot exist before that tenant has something to report, which is exactly
	// the property an alert on an increase relies on.
	for _, name := range []string{"toise_discarded_relations_count", "toise_refused_liveness_promises_count"} {
		for _, pairs := range labelsOf(t, reg, name) {
			if len(pairs) != 0 {
				t.Errorf("%s carries labels %v, want none — it is the instance-wide alert series", name, pairs)
			}
		}
	}
}

// TestWorstGaugeIsNotSuppressedAcrossTenants guards the keying of the mirror
// behind the worst-value gauge. Keyed without the tenant, one tenant's larger
// refusal would hide another's — the cross-tenant leak the label exists to
// remove, and the kind of defect that reads as a smaller number rather than as
// an error.
func TestWorstGaugeIsNotSuppressedAcrossTenants(t *testing.T) {
	reg := prometheus.NewRegistry()
	p := NewPromises()
	reg.MustRegister(p.Collectors()...)

	p.Refused("acme", "agent", "host", 1000*time.Hour) // the larger one lands first
	p.Refused("globex", "agent", "host", 2*time.Hour)

	rows := labelsOf(t, reg, "toise_refused_liveness_promise_seconds")
	seen := map[string]bool{}
	for _, pairs := range rows {
		for _, l := range pairs {
			if l.GetName() == "tenant" {
				seen[l.GetValue()] = true
			}
		}
	}
	for _, want := range []string{"acme", "globex"} {
		if !seen[want] {
			t.Errorf("tenant %q has no worst-value series; rows = %v", want, rows)
		}
	}
	if got := sumFamily(t, reg, "toise_refused_liveness_promise_seconds"); got != (1000*time.Hour + 2*time.Hour).Seconds() {
		t.Errorf("worst gauges sum to %v, want both tenants' values kept", got)
	}
}
