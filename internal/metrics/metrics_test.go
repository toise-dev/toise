package metrics

import (
	"fmt"
	"strings"
	"testing"

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
