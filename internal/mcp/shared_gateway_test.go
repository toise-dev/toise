package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func TestFindPathSharedNextHopAddressIsAConvergence(t *testing.T) {
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			"01HOST_A":  {ID: "01HOST_A", Type: "host", Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("host-a")}}},
			"01HOST_B":  {ID: "01HOST_B", Type: "host", Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("host-b")}}},
			"01ROUTE_A": {ID: "01ROUTE_A", Type: "network.route", Identity: []model.KeyValue{{Key: "network.route.id", Value: model.StringValue("route-a")}}},
			"01ROUTE_B": {ID: "01ROUTE_B", Type: "network.route", Identity: []model.KeyValue{{Key: "network.route.id", Value: model.StringValue("route-b")}}},
			"01GW":      {ID: "01GW", Type: "network.address", Identity: []model.KeyValue{{Key: "network.address", Value: model.StringValue("192.0.2.1")}}},
			"01IF_GW":   {ID: "01IF_GW", Type: "network.interface", Identity: []model.KeyValue{{Key: "network.interface.name", Value: model.StringValue("if-gw")}}},
		},
		deleted: map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "01R1", Type: "has_route", From: "01HOST_A", To: "01ROUTE_A", Structural: true},
			{ID: "01R2", Type: "has_route", From: "01HOST_B", To: "01ROUTE_B", Structural: true},
			{ID: "01R3", Type: "next_hop_via", From: "01ROUTE_A", To: "01GW", Structural: true},
			{ID: "01R4", Type: "next_hop_via", From: "01ROUTE_B", To: "01GW", Structural: true},
			{ID: "01R5", Type: "bound_to", From: "01GW", To: "01IF_GW", Structural: true},
		},
	}
	s := New(g, &fakeStore{})
	s.now = func() time.Time { return time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC) }

	t.Run("two hosts sharing a next-hop address are not reachable", func(t *testing.T) {
		_, out, err := s.findPath(context.Background(), nil, FindPathInput{FromID: "01HOST_A", ToID: "01HOST_B"})
		if err != nil {
			t.Fatalf("findPath returned error: %v", err)
		}
		if out.Reachable {
			t.Errorf("expected out.Reachable to be false, got true")
		}
		foundGW := false
		for _, c := range out.Convergences {
			if c.Type == "network.address" && c.ID == "01GW" {
				foundGW = true
			}
		}
		if !foundGW {
			t.Fatalf("expected the shared gateway address among the convergences, got %+v", out.Convergences)
		}
		if !strings.Contains(out.ConvergenceNote, "same gateway address") {
			t.Errorf("expected convergence note to mention the same gateway address, got %q", out.ConvergenceNote)
		}
		if !strings.Contains(out.ConvergenceNote, "not evidence") {
			t.Errorf("expected convergence note to contain \"not evidence\", got %q", out.ConvergenceNote)
		}
	})

	t.Run("a host reaches the gateway address itself", func(t *testing.T) {
		_, out, err := s.findPath(context.Background(), nil, FindPathInput{FromID: "01HOST_A", ToID: "01GW"})
		if err != nil {
			t.Fatalf("findPath returned error: %v", err)
		}
		if !out.Reachable {
			t.Fatalf("expected HOST_A to 01GW to be reachable, got out=%+v", out)
		}
		if out.Hops != 2 {
			t.Errorf("expected 2 hops to the gateway address, got %d", out.Hops)
		}
		if out.ConvergenceNote != "" {
			t.Errorf("expected empty convergence note, got %q", out.ConvergenceNote)
		}
	})

	t.Run("a host crosses the address by changing relation type", func(t *testing.T) {
		_, out, err := s.findPath(context.Background(), nil, FindPathInput{FromID: "01HOST_A", ToID: "01IF_GW"})
		if err != nil {
			t.Fatalf("findPath returned error: %v", err)
		}
		if !out.Reachable {
			t.Fatalf("expected HOST_A to 01IF_GW to be reachable, got out=%+v", out)
		}
		if out.Hops != 3 {
			t.Errorf("expected 3 hops to the gateway interface, got %d", out.Hops)
		}
	})
}
