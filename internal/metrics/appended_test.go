package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type appendedStore struct {
	fakeStore
}

func (appendedStore) AppendedByChangeType() map[string]uint64 {
	return map[string]uint64{
		"entity.created":             4,
		"entity.deleted":             0,
		"entity.identity_changed":    0,
		"entity.attribute_updated":   2,
		"entity.state_changed":       1,
		"entity.unchanged":           5,
		"relation.added":             3,
		"relation.removed":           0,
		"relation.attribute_changed": 0,
		"unknown":                    0,
	}
}

func TestCollectorAppendedByChangeType(t *testing.T) {
	c := NewCollector(fakeGraph{}, appendedStore{}, "1.2.3", "abc123")

	want := `
# HELP toise_events_appended_total Events appended to the log since process start, by change type. Resets at restart.
# TYPE toise_events_appended_total counter
toise_events_appended_total{change_type="entity.attribute_updated"} 2
toise_events_appended_total{change_type="entity.created"} 4
toise_events_appended_total{change_type="entity.deleted"} 0
toise_events_appended_total{change_type="entity.identity_changed"} 0
toise_events_appended_total{change_type="entity.state_changed"} 1
toise_events_appended_total{change_type="entity.unchanged"} 5
toise_events_appended_total{change_type="relation.added"} 3
toise_events_appended_total{change_type="relation.attribute_changed"} 0
toise_events_appended_total{change_type="relation.removed"} 0
toise_events_appended_total{change_type="unknown"} 0
# HELP toise_events_total Total number of change events appended to the log.
# TYPE toise_events_total counter
toise_events_total 10
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"toise_events_appended_total", "toise_events_total"); err != nil {
		t.Errorf("appended metrics mismatch: %v", err)
	}
}
