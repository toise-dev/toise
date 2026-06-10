package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/store"
)

// TestAggregateSumAndMaxSemantics pins the /metrics aggregation across tenants:
// counters and gauges sum, but SnapshotSeq is a per-tenant reference sequence
// where a sum is meaningless — the aggregate must report the high-water mark.
func TestAggregateSumAndMaxSemantics(t *testing.T) {
	reg, err := registry.Open(t.TempDir(), store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	when := time.Unix(1_700_000_000, 0).UTC()
	ev := func(id string) model.Event {
		return model.Event{Entity: &model.EntityEvent{
			EventID:    model.NewEventID(),
			ChangeType: model.EntityCreated,
			Entity: model.Entity{ID: model.EntityID(id), Type: model.TypeHost,
				Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(id)}}},
			EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
		}}
	}
	for tenantID, ids := range map[string][]string{"alpha": {"a1"}, "beta": {"b1", "b2"}} {
		st, ferr := reg.For(tenantID)
		if ferr != nil {
			t.Fatalf("tenant %s: %v", tenantID, ferr)
		}
		for _, id := range ids {
			if aerr := st.Store.Append(ev(id)); aerr != nil {
				t.Fatalf("append: %v", aerr)
			}
			st.Graph.Apply(ev(id))
		}
		if werr := st.Store.WriteSnapshot(st.Store.Sequence(), st.Graph.SnapshotEvents(when)); werr != nil {
			t.Fatalf("snapshot: %v", werr)
		}
	}

	ag, as := aggregateGraph{reg}, aggregateStore{reg}
	if n := ag.EntityCount(); n != 3 {
		t.Errorf("EntityCount = %d, want 3 (sum across tenants)", n)
	}
	if n := ag.CountByType()[model.TypeHost]; n != 3 {
		t.Errorf("CountByType[host] = %d, want 3", n)
	}
	if n := as.Sequence(); n != 3 {
		t.Errorf("Sequence = %d, want 3 (sum: 1 + 2)", n)
	}
	// alpha snapshotted at seq 1, beta at seq 2: the aggregate is the highest
	// reference sequence, never the sum.
	if n := as.SnapshotSeq(); n != 2 {
		t.Errorf("SnapshotSeq = %d, want 2 (max, not sum)", n)
	}
	if n := as.SnapshotsWritten(); n != 2 {
		t.Errorf("SnapshotsWritten = %d, want 2 (sum)", n)
	}
}
