package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

type fakeGraph struct{}

func (fakeGraph) EntityCount() int            { return 3 }
func (fakeGraph) RelationCount() int          { return 2 }
func (fakeGraph) CountByType() map[string]int { return map[string]int{"host": 2, "db": 1} }

type fakeStore struct{}

func (fakeStore) Sequence() uint64         { return 10 }
func (fakeStore) DiskUsage() uint64        { return 4096 }
func (fakeStore) PrunedEvents() uint64     { return 0 }
func (fakeStore) PrunedBytes() uint64      { return 0 }
func (fakeStore) SnapshotSeq() uint64      { return 0 }
func (fakeStore) SnapshotsWritten() uint64 { return 0 }
func (fakeStore) PruneHorizon() time.Time  { return time.Unix(1700000000, 0) }

func TestCollector(t *testing.T) {
	c := NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123")

	want := `
# HELP toise_entities Number of live entities in the projection.
# TYPE toise_entities gauge
toise_entities 3
# HELP toise_events_total Total number of change events appended to the log.
# TYPE toise_events_total counter
toise_events_total 10
# HELP toise_relations Number of live relations in the projection.
# TYPE toise_relations gauge
toise_relations 2
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"toise_entities", "toise_relations", "toise_events_total"); err != nil {
		t.Errorf("scalar metrics mismatch: %v", err)
	}

	buildInfo := `
# HELP toise_build_info Build information, value is always 1.
# TYPE toise_build_info gauge
toise_build_info{commit="abc123",version="1.2.3"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(buildInfo), "toise_build_info"); err != nil {
		t.Errorf("build_info mismatch: %v", err)
	}

	if n := testutil.CollectAndCount(c, "toise_entities_by_type"); n != 2 {
		t.Errorf("entities_by_type series = %d, want 2 (host, db)", n)
	}

	horizon := `
# HELP toise_store_prune_horizon_seconds Unix time of the latest retention cutoff applied (0 if never pruned); the oldest instant an as-of read can answer completely.
# TYPE toise_store_prune_horizon_seconds gauge
toise_store_prune_horizon_seconds 1.7e+09
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(horizon), "toise_store_prune_horizon_seconds"); err != nil {
		t.Errorf("prune_horizon mismatch: %v", err)
	}
}

// TestByTypeCardinalityCap pins #115: producers control entity types, so the
// by-type label is capped — top types reported individually, the tail folded
// into "other".
func TestByTypeCardinalityCap(t *testing.T) {
	counts := make(map[string]int, maxTypeSeries+10)
	for i := 0; i < maxTypeSeries+10; i++ {
		counts[fmt.Sprintf("type%03d", i)] = i + 1
	}
	desc := prometheus.NewDesc("test_by_type", "", []string{"type"}, nil)
	ch := make(chan prometheus.Metric, maxTypeSeries+2)
	emitByType(ch, desc, counts)
	close(ch)

	var series int
	var other float64
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		series++
		if len(d.GetLabel()) == 1 && d.GetLabel()[0].GetValue() == "other" {
			other = d.GetGauge().GetValue()
		}
	}
	if series != maxTypeSeries+1 {
		t.Errorf("series = %d, want %d (top-N plus other)", series, maxTypeSeries+1)
	}
	// the 10 smallest counts (1..10) fold into other
	if other != 55 {
		t.Errorf("other = %v, want 55 (sum of the folded tail)", other)
	}
}

// TestMaintenanceObserve pins #143: each maintenance pass records its outcome
// and duration, per op and tenant — the signal the aggregate gauges hide.
func TestMaintenanceObserve(t *testing.T) {
	m := NewMaintenance()
	if err := m.Observe("sweep", "acme", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.Observe("prune", "acme", func() error { return fmt.Errorf("disk full") }); err == nil {
		t.Fatal("Observe must propagate the op error")
	}
	if v := testutil.ToFloat64(m.runs.WithLabelValues("sweep", "ok", "acme")); v != 1 {
		t.Errorf("sweep ok runs = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.runs.WithLabelValues("prune", "error", "acme")); v != 1 {
		t.Errorf("prune error runs = %v, want 1", v)
	}
	// nil receiver: observes nothing, still runs fn.
	ran := false
	var nilM *Maintenance
	if err := nilM.Observe("sweep", "x", func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("nil Maintenance must run fn (ran=%v err=%v)", ran, err)
	}
}

// TestStartTimeIsThisProcessAndNotAnother pins the reason toise_start_time_seconds
// exists at all.
//
// A rule asking "has this server been up long enough that an absence means
// something" was written against process_start_time_seconds, which EVERY process
// in a tenant publishes. Aggregating that reads the oldest unrelated process, so
// the guard was silently always true — measured in production at 6155 s of
// apparent uptime against 239 s of real uptime on one park, and over 22 series on
// another. The workaround joined to toise_build_info, which in turn joins on an
// empty label set wherever the collection carries none of job/instance/target,
// and then matches every process again.
//
// So the value must come from Toise, be published without labels to aggregate
// over, and move when the process restarts. The test asserts the first two and
// the third by construction: two collectors built a moment apart do not report
// the same instant unless the value is not this process's.
func TestStartTimeIsThisProcessAndNotAnother(t *testing.T) {
	before := time.Now().Unix()
	c := NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc123")

	if n := testutil.CollectAndCount(c, "toise_start_time_seconds"); n != 1 {
		t.Fatalf("toise_start_time_seconds series = %d, want exactly 1 (no labels to aggregate over)", n)
	}

	reg := prometheus.NewPedanticRegistry()
	if rerr := reg.Register(c); rerr != nil {
		t.Fatalf("registering the collector: %v", rerr)
	}
	families, gerr := reg.Gather()
	if gerr != nil {
		t.Fatalf("gathering: %v", gerr)
	}
	var value float64
	var found bool
	for _, f := range families {
		if f.GetName() != "toise_start_time_seconds" {
			continue
		}
		found = true
		m := f.GetMetric()
		if len(m) != 1 {
			t.Fatalf("metric count = %d, want 1", len(m))
		}
		if len(m[0].GetLabel()) != 0 {
			t.Errorf("toise_start_time_seconds carries labels %v; it must carry none, so min() over it is unambiguous", m[0].GetLabel())
		}
		value = m[0].GetGauge().GetValue()
	}
	if !found {
		t.Fatal("toise_start_time_seconds was not gathered")
	}
	after := time.Now().Unix()
	if value < float64(before) || value > float64(after) {
		t.Errorf("start time = %v, want within [%d, %d]: it must be THIS process's start, not another's", value, before, after)
	}
}
