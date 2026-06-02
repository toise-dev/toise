package demo

import (
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

func runScenario(t *testing.T) (*projection.Graph, *store.Store, time.Time) {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	graph := projection.New()
	clk := NewClock()
	eng := change.New(graph, st, change.WithClock(clk.Now))
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if _, err := Run(eng, clk, start); err != nil {
		t.Fatalf("run scenario: %v", err)
	}
	return graph, st, start
}

func TestScenarioFinalGraph(t *testing.T) {
	graph, _, _ := runScenario(t)

	// Live entities at the end: host, nginx, postgres, eth0, two addresses
	// (10.0.2.7 + gateway 10.0.2.1), the default route, two listeners.
	if got := graph.EntityCount(); got != 9 {
		t.Errorf("EntityCount = %d, want 9", got)
	}

	// nginx survived a restart as one logical entity: identity is its executable
	// name, the pid is a descriptive attribute that the restart updated to 1010.
	var nginx []model.Entity
	for _, e := range graph.ListEntities(model.TypeProcess) {
		for _, kv := range e.Identity {
			if kv.Key == "process.executable.name" && kv.Value.Str() == "nginx" {
				nginx = append(nginx, e)
			}
		}
	}
	if len(nginx) != 1 {
		t.Fatalf("want exactly one nginx process, got %d", len(nginx))
	}
	if pid := attrInt(nginx[0], "process.pid"); pid != 1010 {
		t.Errorf("nginx pid attribute = %d, want 1010 (updated on restart)", pid)
	}

	// dockerd crashed and is gone; the old addresses/gateway are gone.
	assertAbsent(t, graph, model.TypeProcess, "process.executable.name", "dockerd")
	assertAbsent(t, graph, model.TypeNetworkAddress, "network.address", "10.0.1.5")
	assertAbsent(t, graph, model.TypeNetworkAddress, "network.address", "10.0.1.1")
}

func TestScenarioCoversEveryChangeType(t *testing.T) {
	_, st, start := runScenario(t)
	events, err := st.ReadByTimeRange(start.Add(-time.Hour), start.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		switch {
		case ev.Entity != nil:
			seen[ev.Entity.ChangeType.String()] = true
		case ev.Relation != nil:
			seen[ev.Relation.ChangeType.String()] = true
		}
	}
	// Exact matching (ADR 0018) retires fuzzy entity.identity_changed; the engine
	// never emits it, and the scenario covers the other eight change types.
	if seen[model.EntityIdentityChanged.String()] {
		t.Error("engine should not emit entity.identity_changed under exact matching")
	}
	for _, ct := range []model.ChangeType{
		model.EntityCreated, model.EntityDeleted,
		model.EntityAttributeUpdated, model.EntityStateChanged, model.EntityUnchanged,
		model.RelationAdded, model.RelationRemoved, model.RelationAttributeChanged,
	} {
		if !seen[ct.String()] {
			t.Errorf("scenario never produced a %s event", ct)
		}
	}
}

func TestScenarioLateRecordedFact(t *testing.T) {
	graph, st, _ := runScenario(t)
	ifaces := graph.ListEntities(model.TypeNetworkInterface)
	if len(ifaces) != 1 {
		t.Fatalf("want one interface, got %d", len(ifaces))
	}
	events, err := st.ReadByEntity(ifaces[0].ID)
	if err != nil {
		t.Fatalf("read eth0 history: %v", err)
	}
	var lateFound bool
	for _, ev := range events {
		if ev.Entity == nil || ev.Entity.ChangeType != model.EntityStateChanged {
			continue
		}
		if ev.Entity.RecordedAt.After(ev.Entity.EventTime) {
			lateFound = true // the eth0-down fact was recorded after it became true
		}
	}
	if !lateFound {
		t.Error("expected a late-recorded eth0 state change (recorded_at after event_time) for asKnownAt audit")
	}
}

func attrInt(e model.Entity, key string) int64 {
	for _, kv := range e.Attributes {
		if kv.Key == key {
			return kv.Value.Int()
		}
	}
	return -1
}

func assertAbsent(t *testing.T, g *projection.Graph, typ, key, val string) {
	t.Helper()
	for _, e := range g.ListEntities(typ) {
		for _, kv := range e.Identity {
			if kv.Key == key && kv.Value.Str() == val {
				t.Errorf("%s %s=%s should be absent (deleted) but is live", typ, key, val)
			}
		}
	}
}
