package ingest

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

// readCounters collects every ingest counter into "name|label|label" keys.
func readCounters(t *testing.T, m *Metrics) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, c := range m.Collectors() {
		ch := make(chan prometheus.Metric, 64)
		c.Collect(ch)
		close(ch)
		for pm := range ch {
			var d dto.Metric
			if err := pm.Write(&d); err != nil {
				t.Fatal(err)
			}
			key := pm.Desc().String()
			for _, l := range d.GetLabel() {
				key += "|" + l.GetValue()
			}
			out[key] = d.GetCounter().GetValue()
		}
	}
	return out
}

// counter reads one series. Prometheus renders label values in the label's
// alphabetical order, so records is keyed "<result>|<tenant>", not the order the
// CounterVec declares.
func counter(t *testing.T, counts map[string]float64, name string, labels ...string) float64 {
	t.Helper()
	suffix := ""
	for _, l := range labels {
		suffix += "|" + l
	}
	for k, v := range counts {
		if strings.Contains(k, name) && strings.HasSuffix(k, suffix) {
			return v
		}
	}
	t.Fatalf("no series %s%s among %v", name, suffix, counts)
	return 0
}

// TestIngestCountersAreAttributedToTheirTenant is the defect #405 named: one OTLP
// stream can carry several tenants, and before this the per-record counters were
// export-wide totals. An operator on a shared server could see that something had
// been refused and had no way to learn whose — which is the one question those
// counters exist to answer.
func TestIngestCountersAreAttributedToTheirTenant(t *testing.T) {
	base := t.TempDir()
	var mu sync.Mutex
	engines := map[string]*change.Engine{}
	engineFor := func(id string) (*change.Engine, error) {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := engines[id]; ok {
			return e, nil
		}
		st, err := store.Open(filepath.Join(base, id), store.DefaultConfig())
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = st.Close() })
		engines[id] = change.New(projection.New(), st, change.WithClock(func() time.Time { return t0 }))
		return engines[id], nil
	}

	m := NewMetrics()
	rec := NewRoutedReceiver(engineFor, nil, m, false, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := plogotlp.NewGRPCClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// One export, two tenants. acme sends a valid host. globex sends a valid host
	// AND a record whose entity type is not registered, which is rejected.
	ld := plog.NewLogs()
	rlA := ld.ResourceLogs().AppendEmpty()
	rlA.Resource().Attributes().PutStr(tenant.ResourceAttr, "acme")
	entityRecord(rlA.ScopeLogs().AppendEmpty(), evEntityState, model.TypeHost, map[string]string{"host.id": "h-acme"}, nil)

	rlB := ld.ResourceLogs().AppendEmpty()
	rlB.Resource().Attributes().PutStr(tenant.ResourceAttr, "globex")
	slB := rlB.ScopeLogs().AppendEmpty()
	entityRecord(slB, evEntityState, model.TypeHost, map[string]string{"host.id": "h-globex"}, nil)
	entityRecord(slB, evEntityState, "not.a.registered.type", map[string]string{"x": "1"}, nil)

	if _, err := client.Export(ctx, plogotlp.NewExportRequestFromLogs(ld)); err != nil {
		t.Fatalf("export: %v", err)
	}

	counts := readCounters(t, m)
	const records = "toise_ingest_records_total"

	if v := counter(t, counts, records, "rejected", "globex"); v != 1 {
		t.Errorf("globex rejected = %v, want 1", v)
	}
	// The point of the change: acme must not wear globex's rejection.
	if v := counter(t, counts, records, "rejected", "acme"); v != 0 {
		t.Errorf("acme rejected = %v, want 0 — a rejection was attributed to the wrong tenant", v)
	}
	if v := counter(t, counts, records, "handled", "acme"); v != 1 {
		t.Errorf("acme handled = %v, want 1", v)
	}
	if v := counter(t, counts, records, "handled", "globex"); v != 1 {
		t.Errorf("globex handled = %v, want 1", v)
	}
}

// TestSeenTenantHasItsSeriesAtZero: a zero and a missing series do not mean the
// same thing to whoever reads them, and only one of them is an answer. A tenant
// that has been seen must carry all three result series even when nothing was
// rejected, so "nothing was refused for my tenant" is a measured statement.
func TestSeenTenantHasItsSeriesAtZero(t *testing.T) {
	m := NewMetrics()
	m.ensure("acme")
	counts := readCounters(t, m)
	for _, result := range recordResults {
		if v := counter(t, counts, "toise_ingest_records_total", result, "acme"); v != 0 {
			t.Errorf("acme %s = %v, want a zero series", result, v)
		}
	}
	for k := range counts {
		if strings.Contains(k, "toise_ingest_records_total") && strings.HasSuffix(k, "|globex") {
			t.Error("a tenant never seen must not have series")
		}
	}
}

// TestInertBeliefReasons pins #544 on the write side: the reason vocabulary, and
// the fact that a resolves_to carrying no confidence is NOT reported.
//
// The four reasons are deliberately the conformance kit's own words, so the
// producer-side check and the server-side counter cannot describe one defect two
// ways. The classification matches canonical.Confidence, which is what the
// overlay actually applies — a second copy of that rule is how a counter and a
// reader come to disagree about the same edge.
func TestInertBeliefReasons(t *testing.T) {
	desc := func(build func(m pcommon.Map)) pcommon.Map {
		m := pcommon.NewMap()
		build(m)
		return m
	}

	for _, tc := range []struct {
		name    string
		relType string
		build   func(m pcommon.Map)
		want    string
	}{
		{"same_as with a usable confidence says nothing", model.RelSameAs,
			func(m pcommon.Map) { m.PutDouble("confidence", 1.0) }, ""},
		{"same_as weak but readable says nothing", model.RelSameAs,
			func(m pcommon.Map) { m.PutDouble("confidence", 0.2) }, ""},
		{"same_as with no confidence", model.RelSameAs,
			func(m pcommon.Map) { m.PutStr("basis", "vm_id_match") }, "no_confidence"},
		{"same_as with a string confidence", model.RelSameAs,
			func(m pcommon.Map) { m.PutStr("confidence", "0.95") }, "confidence_not_numeric"},
		{"same_as above one", model.RelSameAs,
			func(m pcommon.Map) { m.PutDouble("confidence", 1.5) }, "confidence_out_of_range"},
		{"same_as below zero as an int", model.RelSameAs,
			func(m pcommon.Map) { m.PutInt("confidence", -2) }, "confidence_out_of_range"},
		{"resolves_to with a confidence nothing reads", model.RelResolvesTo,
			func(m pcommon.Map) { m.PutDouble("confidence", 1.0) }, "relation_has_no_reader"},
		{"resolves_to with only a basis is silent", model.RelResolvesTo,
			func(m pcommon.Map) { m.PutStr("basis", "lldp") }, ""},
	} {
		_, got := beliefAttributes(desc(tc.build), tc.relType)
		if got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}

	// A usable confidence is still carried onto the edge, whatever the reason:
	// ADR 0022 stores the value as the producer sent it, and reporting must never
	// become dropping.
	attrs, _ := beliefAttributes(desc(func(m pcommon.Map) {
		m.PutDouble("confidence", 1.5)
		m.PutStr("basis", "vm_id_match")
	}), model.RelSameAs)
	if len(attrs) != 2 {
		t.Errorf("an out-of-range confidence was dropped instead of stored: %+v", attrs)
	}
}

// TestInertBeliefSeriesAreZeroForASeenTenant keeps the same property the record
// results have: a tenant that has been seen and has no inert belief must read as
// a measured zero, not as an absent series. An absent series and a zero one mean
// very different things, and only one of them is a success.
func TestInertBeliefSeriesAreZeroForASeenTenant(t *testing.T) {
	m := NewMetrics()
	m.ensure("acme")
	counts := readCounters(t, m)
	// Label VALUES are rendered in the label NAME's alphabetical order:
	// reason, relation_type, tenant.
	for _, c := range inertBeliefCases {
		if v := counter(t, counts, "toise_inert_beliefs_total", c[1], c[0], "acme"); v != 0 {
			t.Errorf("acme %s/%s = %v, want a zero series", c[0], c[1], v)
		}
	}

	m.addInertBelief("acme", model.RelSameAs, "no_confidence")
	counts = readCounters(t, m)
	if v := counter(t, counts, "toise_inert_beliefs_total", "no_confidence", model.RelSameAs, "acme"); v != 1 {
		t.Errorf("after one inert belief = %v, want 1", v)
	}
}
