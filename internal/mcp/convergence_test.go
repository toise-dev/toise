package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// convergenceFixture builds the shape a real fleet produces constantly: two
// machines that dial the same destination and never talk to each other. Two
// replicas meeting at a shared object store is the canonical case (ADR 0029:
// nodes never talk to each other; they meet at the object store).
//
//	hostA <-runs_on- svcA -depends_on-> ep <-depends_on- svcB -runs_on-> hostB
//	hostA <-runs_on- svcA2
func convergenceFixture() *Server {
	mkHost := func(id, name string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: "host",
			Identity: []model.KeyValue{{Key: "host.name", Value: model.StringValue(name)}}}
	}
	mkSvc := func(id, inst string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: "service.instance",
			Identity: []model.KeyValue{{Key: "service.instance.id", Value: model.StringValue(inst)}}}
	}
	ep := model.Entity{ID: "01EP_STORE", Type: "network.endpoint", Identity: []model.KeyValue{
		{Key: "server.address", Value: model.StringValue("217.182.253.243")},
		{Key: "server.port", Value: model.IntValue(9000)},
		{Key: "network.transport", Value: model.StringValue("tcp")},
	}}
	hostA, hostB := mkHost("01HOST_A", "a"), mkHost("01HOST_B", "b")
	svcA, svcA2, svcB := mkSvc("01SVC_A", "a1"), mkSvc("01SVC_A2", "a2"), mkSvc("01SVC_B", "b1")
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			hostA.ID: hostA, hostB.ID: hostB, ep.ID: ep,
			svcA.ID: svcA, svcA2.ID: svcA2, svcB.ID: svcB,
		},
		deleted: map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "r1", Type: "runs_on", From: svcA.ID, To: hostA.ID, Structural: true},
			{ID: "r2", Type: "runs_on", From: svcA2.ID, To: hostA.ID, Structural: true},
			{ID: "r3", Type: "runs_on", From: svcB.ID, To: hostB.ID, Structural: true},
			{ID: "r4", Type: "depends_on", From: svcA.ID, To: ep.ID, Structural: true},
			{ID: "r5", Type: "depends_on", From: svcB.ID, To: ep.ID, Structural: true},
		},
	}
	s := New(g, &fakeStore{})
	s.now = func() time.Time { return time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC) }
	return s
}

// TestFindPathRefusesToTransitATerminal pins #442: a shared endpoint is a
// convergence, not a route, and the convergence itself is the answer.
func TestFindPathRefusesToTransitATerminal(t *testing.T) {
	s := convergenceFixture()
	ctx := context.Background()

	_, out, err := s.findPath(ctx, nil, FindPathInput{FromID: "01HOST_A", ToID: "01HOST_B"})
	if err != nil {
		t.Fatalf("findPath: %v", err)
	}
	if out.Reachable {
		t.Fatalf("two hosts that only share an endpoint must not be reachable; got %d hops: %+v", out.Hops, out.Path)
	}
	if out.ConvergenceTotal != 1 || len(out.Convergences) != 1 {
		t.Fatalf("convergences = %+v (total %d), want the one shared endpoint", out.Convergences, out.ConvergenceTotal)
	}
	if got := out.Convergences[0]; got.Type != "network.endpoint" || got.ID != "01EP_STORE" {
		t.Errorf("convergence = %s/%s, want network.endpoint/01EP_STORE", got.Type, got.ID)
	}
	// The note must say what the convergence is NOT, in words: a bare flag is
	// one a consumer drops the day it ships.
	if !strings.Contains(out.ConvergenceNote, "not evidence") {
		t.Errorf("convergence_note must deny the inference in words, got %q", out.ConvergenceNote)
	}

	// Symmetric: the pair of dependents themselves.
	_, out, err = s.findPath(ctx, nil, FindPathInput{FromID: "01SVC_A", ToID: "01SVC_B"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Reachable || out.ConvergenceTotal != 1 {
		t.Errorf("dependents of one endpoint: reachable=%v convergences=%d, want false/1", out.Reachable, out.ConvergenceTotal)
	}
}

// TestFindPathKeepsLegitimateTraversals guards the fix against over-reach: a
// terminal is still a destination and still a start, and a junction that is NOT
// terminal still connects what meets on it.
func TestFindPathKeepsLegitimateTraversals(t *testing.T) {
	s := convergenceFixture()
	ctx := context.Background()

	// A terminal reached as the destination is a real path.
	_, out, err := s.findPath(ctx, nil, FindPathInput{FromID: "01HOST_A", ToID: "01EP_STORE"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reachable || out.Hops != 2 {
		t.Errorf("host to the endpoint it dials: reachable=%v hops=%d, want true/2", out.Reachable, out.Hops)
	}
	if out.ConvergenceNote != "" {
		t.Errorf("a found path must carry no convergence note, got %q", out.ConvergenceNote)
	}

	// A terminal as the START may be left: "who dials this endpoint" is a real
	// question, and the answer asserts nothing the producers did not observe.
	_, out, err = s.findPath(ctx, nil, FindPathInput{FromID: "01EP_STORE", ToID: "01HOST_A"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reachable || out.Hops != 2 {
		t.Errorf("from the endpoint to a dialer: reachable=%v hops=%d, want true/2", out.Reachable, out.Hops)
	}

	// Co-location on a shared HOST is not a convergence: the host is a thing both
	// services are part of, and the fix must not touch that.
	_, out, err = s.findPath(ctx, nil, FindPathInput{FromID: "01SVC_A", ToID: "01SVC_A2"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reachable || out.Hops != 2 {
		t.Errorf("two services on one host: reachable=%v hops=%d, want true/2", out.Reachable, out.Hops)
	}
}

// TestCrossingNeedsADifferentRelation pins the per-type crossing rule, and its
// last case is the one that keeps the rule honest.
//
// Some nodes are crossed by a real path and also sit at the center of a
// convergence, and the two have the SAME structural shape — the node is the
// source of both edges — so only the relation types tell them apart. A general
// rule was tried and refuted within the hour (a switch traversed enters and
// leaves by has_interface, both times, and that IS forwarding), which is why
// this is per type. The refutation is recorded in reference-path.md.
func TestCrossingNeedsADifferentRelation(t *testing.T) {
	// One host, one of its interfaces, the neighbor it saw, and the router
	// interface owning that MAC. The real L2 descent.
	ent := func(id, typ, k, v string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: typ,
			Identity: []model.KeyValue{{Key: k, Value: model.StringValue(v)}}}
	}
	build := func(rels ...model.Relation) *Server {
		es := []model.Entity{
			ent("01HOST", "host", "host.id", "h1"),
			ent("01IF_ETH0", "network.interface", "network.interface.name", "eth0"),
			ent("01IF_ETH1", "network.interface", "network.interface.name", "eth1"),
			ent("01NB", "network.neighbor", "mac", "AA-BB-CC-DD-EE-FF"),
			ent("01IF_RTR", "network.interface", "network.interface.name", "Vlan20"),
			ent("01DEV", "network.device", "network.device.id", "serial:9:FOC1"),
			ent("01IF_UP", "network.interface", "network.interface.name", "Ethernet1"),
			ent("01IF_DN", "network.interface", "network.interface.name", "Ethernet2"),
			ent("01BOND", "network.interface", "network.interface.name", "bond0"),
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

	t.Run("the real L2 descent crosses the neighbor", func(t *testing.T) {
		// eth0 <-neighbor_of- NB -resolves_to-> Vlan20 : different types, allowed.
		s := build(
			model.Relation{ID: "n1", Type: "neighbor_of", From: "01NB", To: "01IF_ETH0", Structural: true},
			model.Relation{ID: "n2", Type: "resolves_to", From: "01NB", To: "01IF_RTR"},
		)
		if !reach(t, s, "01IF_ETH0", "01IF_RTR") {
			t.Error("the descent eth0 -> neighbor -> router interface was refused; it is the point of resolves_to")
		}
	})

	t.Run("one MAC on two interfaces is not a link", func(t *testing.T) {
		// eth0 <-neighbor_of- NB -neighbor_of-> eth1 : same type, refused.
		s := build(
			model.Relation{ID: "n1", Type: "neighbor_of", From: "01NB", To: "01IF_ETH0", Structural: true},
			model.Relation{ID: "n2", Type: "neighbor_of", From: "01NB", To: "01IF_ETH1", Structural: true},
		)
		if reach(t, s, "01IF_ETH0", "01IF_ETH1") {
			t.Error("two interfaces that saw one MAC were reported as reachable; seeing the same neighbor does not link them")
		}
	})

	t.Run("two bond members are not a link", func(t *testing.T) {
		// eth0 -aggregated_into-> bond0 <-aggregated_into- eth1 : same type, refused.
		// Traffic goes UP into the logical interface and into the machine; it does
		// not cross from one member to another.
		s := build(
			model.Relation{ID: "a1", Type: "aggregated_into", From: "01IF_ETH0", To: "01BOND", Structural: true},
			model.Relation{ID: "a2", Type: "aggregated_into", From: "01IF_ETH1", To: "01BOND", Structural: true},
		)
		if reach(t, s, "01IF_ETH0", "01IF_ETH1") {
			t.Error("two members of one bond were reported as reachable through it; traffic does not cross between members")
		}
	})

	t.Run("a switch IS crossed by two has_interface — the refuted conjecture", func(t *testing.T) {
		// The negative control, and the reason the rule is per type rather than
		// general: this is the same structural shape as the two refusals above,
		// and it is real forwarding. A general rule would have cut the path at
		// every transit device.
		s := build(
			model.Relation{ID: "h1", Type: "has_interface", From: "01DEV", To: "01IF_UP", Structural: true},
			model.Relation{ID: "h2", Type: "has_interface", From: "01DEV", To: "01IF_DN", Structural: true},
		)
		if !reach(t, s, "01IF_UP", "01IF_DN") {
			t.Error("a switch is no longer crossed between two of its own ports; the per-type rule has become the general one that was refuted")
		}
	})
}
