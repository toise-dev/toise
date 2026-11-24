package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func TestFindPathAcceptsFromConvergence(t *testing.T) {
	ent := func(id, typ, k, v string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: typ,
			Identity: []model.KeyValue{{Key: k, Value: model.StringValue(v)}}}
	}
	build := func(rels ...model.Relation) *Server {
		es := []model.Entity{
			ent("01HOST_S1", "host", "host.id", "s1"),
			ent("01HOST_S2", "host", "host.id", "s2"),
			ent("01HOST_C", "host", "host.id", "c"),
			ent("01LST1", "service.listener", "service.listener.id", "lst1"),
			ent("01LST2", "service.listener", "service.listener.id", "lst2"),
			ent("01LST", "service.listener", "service.listener.id", "lst"),
			ent("01ADDR", "network.address", "network.address.ip", "10.0.0.7"),
			ent("01ADDR1", "network.address", "network.address.ip", "10.0.0.8"),
			ent("01ADDR2", "network.address", "network.address.ip", "10.0.0.9"),
			ent("01IF_C", "network.interface", "network.interface.name", "eth0"),
		}
		m := map[model.EntityID]model.Entity{}
		for _, e := range es {
			m[e.ID] = e
		}
		s := New(&fakeGraph{entities: m, deleted: map[model.EntityID]bool{}, relations: rels}, &fakeStore{})
		s.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
		return s
	}
	reach := func(t *testing.T, s *Server, from, to string) bool {
		t.Helper()
		_, out, err := s.findPath(context.Background(), nil, FindPathInput{FromID: from, ToID: to})
		if err != nil {
			t.Fatalf("findPath: %v", err)
		}
		return out.Reachable
	}

	t.Run("two listeners sharing one client address are not linked", func(t *testing.T) {
		s := build(
			model.Relation{ID: "r1", Type: "runs_on", From: "01LST1", To: "01HOST_S1", Structural: true},
			model.Relation{ID: "r2", Type: "runs_on", From: "01LST2", To: "01HOST_S2", Structural: true},
			model.Relation{ID: "a1", Type: "accepts_from", From: "01LST1", To: "01ADDR"},
			model.Relation{ID: "a2", Type: "accepts_from", From: "01LST2", To: "01ADDR"},
		)
		if reach(t, s, "01HOST_S1", "01HOST_S2") {
			t.Error("hostS1 -> hostS2 was reported reachable through two accepts_from edges on one address; that is a convergence, not a path")
		}
	})

	t.Run("two client addresses of one listener are not linked", func(t *testing.T) {
		s := build(
			model.Relation{ID: "a1", Type: "accepts_from", From: "01LST", To: "01ADDR1"},
			model.Relation{ID: "a2", Type: "accepts_from", From: "01LST", To: "01ADDR2"},
		)
		if reach(t, s, "01ADDR1", "01ADDR2") {
			t.Error("addr1 -> addr2 was reported reachable through two accepts_from edges on one listener; that is a convergence, not a path")
		}
	})

	t.Run("client host to its address to the listener is a real path", func(t *testing.T) {
		s := build(
			model.Relation{ID: "h1", Type: "has_interface", From: "01HOST_C", To: "01IF_C", Structural: true},
			model.Relation{ID: "b1", Type: "bound_to", From: "01ADDR", To: "01IF_C", Structural: true},
			model.Relation{ID: "a1", Type: "accepts_from", From: "01LST", To: "01ADDR"},
			model.Relation{ID: "r1", Type: "runs_on", From: "01LST", To: "01HOST_S1", Structural: true},
		)
		if !reach(t, s, "01HOST_C", "01HOST_S1") {
			t.Error("hostC -> hostS was refused; arriving by bound_to and leaving by accepts_from is a real path")
		}
		if !reach(t, s, "01HOST_S1", "01HOST_C") {
			t.Error("hostS -> hostC was refused; arriving by accepts_from and leaving by bound_to is the same real path read the other way")
		}
	})
}

func TestImpactOfDoesNotCrossAcceptsFrom(t *testing.T) {
	ent := func(id, typ, k, v string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: typ,
			Identity: []model.KeyValue{{Key: k, Value: model.StringValue(v)}}}
	}
	m := map[model.EntityID]model.Entity{}
	for _, e := range []model.Entity{
		ent("01HOST_S", "host", "host.id", "s"),
		ent("01LST", "service.listener", "service.listener.id", "lst"),
		ent("01ADDR", "network.address", "network.address.ip", "10.0.0.7"),
	} {
		m[e.ID] = e
	}
	s := New(&fakeGraph{
		entities: m,
		deleted:  map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "r1", Type: "runs_on", From: "01LST", To: "01HOST_S", Structural: true},
			{ID: "a1", Type: "accepts_from", From: "01LST", To: "01ADDR"},
		},
	}, &fakeStore{})
	s.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }

	_, out, err := s.impactOf(context.Background(), nil, ImpactOfInput{EntityID: "01HOST_S"})
	if err != nil {
		t.Fatalf("impactOf: %v", err)
	}
	for _, imp := range out.Impacted {
		if imp.ID == "01ADDR" {
			t.Errorf("impact_of hostS listed the client address %s; accepts_from records a third party's address and must not propagate impact", imp.ID)
		}
	}
}
