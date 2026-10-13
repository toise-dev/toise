package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// TestNeighborsStayOutOfABlastRadius pins the measurement that changed
// neighbor_of from a dependency into a bare observation.
//
// A host observes its ARP neighbors through its own interface. If that edge
// carried impact, impact_of on the host would walk has_interface and then every
// neighbor_of, and report each neighbor as impacted — on a host with a few
// hundred ARP entries, a host failure would claim a few hundred third-party
// devices. A printer does not stop working because the machine watching it died,
// and a blast radius that says otherwise is worse than no blast radius.
func TestNeighborsStayOutOfABlastRadius(t *testing.T) {
	host := model.Entity{ID: "01H", Type: "host",
		Identity: []model.KeyValue{{Key: "host.name", Value: model.StringValue("a")}}}
	iface := model.Entity{ID: "01I", Type: "network.interface",
		Identity: []model.KeyValue{{Key: "network.interface.name", Value: model.StringValue("eth0")}}}
	neighbor := func(id, mac string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: "network.neighbor", Identity: []model.KeyValue{
			{Key: "host.id", Value: model.StringValue("a")},
			{Key: "mac", Value: model.StringValue(mac)},
		}}
	}
	n1, n2 := neighbor("01N1", "AA-BB-CC-DD-EE-01"), neighbor("01N2", "AA-BB-CC-DD-EE-02")
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			host.ID: host, iface.ID: iface, n1.ID: n1, n2.ID: n2,
		},
		deleted: map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "r1", Type: "has_interface", From: host.ID, To: iface.ID, Structural: true},
			{ID: "r2", Type: "neighbor_of", From: n1.ID, To: iface.ID, Structural: true},
			{ID: "r3", Type: "neighbor_of", From: n2.ID, To: iface.ID, Structural: true},
		},
	}
	s := New(g, &fakeStore{})
	s.now = func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }

	_, out, err := s.impactOf(context.Background(), nil, ImpactOfInput{EntityID: "01H"})
	if err != nil {
		t.Fatalf("impactOf: %v", err)
	}
	for _, i := range out.Impacted {
		if i.Type == "network.neighbor" {
			t.Errorf("a host failing must not report an observed neighbor as impacted: %+v", i)
		}
	}
	// The interface itself is a real dependency and must stay.
	if out.Total != 1 || len(out.Impacted) != 1 || out.Impacted[0].Type != "network.interface" {
		t.Errorf("blast radius = %d %+v, want the interface alone", out.Total, out.ByType)
	}

	// And the other direction: the neighbor is not a dependency of anything.
	_, back, err := s.impactOf(context.Background(), nil, ImpactOfInput{EntityID: "01N1"})
	if err != nil {
		t.Fatalf("impactOf neighbor: %v", err)
	}
	if back.Total != 0 {
		t.Errorf("an unresolved neighbor failing affects nothing, got %d: %+v", back.Total, back.ByType)
	}
}
