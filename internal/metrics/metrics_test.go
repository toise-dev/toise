package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeGraph struct{}

func (fakeGraph) EntityCount() int            { return 3 }
func (fakeGraph) RelationCount() int          { return 2 }
func (fakeGraph) CountByType() map[string]int { return map[string]int{"host": 2, "db": 1} }

type fakeStore struct{}

func (fakeStore) Sequence() uint64     { return 10 }
func (fakeStore) DiskUsage() uint64    { return 4096 }
func (fakeStore) PrunedEvents() uint64 { return 0 }
func (fakeStore) PrunedBytes() uint64  { return 0 }

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
