package mcp

import (
	"context"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// End-to-end on the read path against the REAL projection (not fakeGraph):
// build the interface model + a depends_on edge to an observed endpoint, then
// assert get_neighbors surfaces the endpoint with its resolved canonical
// listener (#184).
func TestGetNeighborsResolvesEndpointEndToEnd(t *testing.T) {
	g := projection.New()

	ent := func(id, typ string, ident ...model.KeyValue) {
		g.Apply(model.Event{Entity: &model.EntityEvent{
			EventID: id + "-ev", ChangeType: model.EntityCreated,
			Entity: model.Entity{ID: model.EntityID(id), Type: typ, Identity: ident},
		}})
	}
	entAttr := func(id, typ string, ident, attrs []model.KeyValue) {
		g.Apply(model.Event{Entity: &model.EntityEvent{
			EventID: id + "-ev", ChangeType: model.EntityCreated,
			Entity: model.Entity{ID: model.EntityID(id), Type: typ, Identity: ident, Attributes: attrs},
		}})
	}
	rel := func(id, typ, from, to string) {
		g.Apply(model.Event{Relation: &model.RelationEvent{
			EventID: id + "-ev", ChangeType: model.RelationAdded,
			Relation: model.Relation{ID: model.RelationID(id), Type: typ,
				From: model.EntityID(from), To: model.EntityID(to)},
		}})
	}

	ent("instA", model.TypeServiceInstance, model.KeyValue{Key: "service.instance.id", Value: sv("aaaa")})
	ent("ep1", model.TypeNetworkEndpoint,
		model.KeyValue{Key: "server.address", Value: sv("10.0.0.5")},
		model.KeyValue{Key: "server.port", Value: sv("443")},
		model.KeyValue{Key: "network.transport", Value: sv("tcp")})
	ent("hb", model.TypeHost, model.KeyValue{Key: "host.id", Value: sv("hb-uuid")})
	entAttr("lisB", model.TypeServiceListener,
		[]model.KeyValue{{Key: "service.endpoint", Value: sv("hb-uuid:443/tcp")}},
		[]model.KeyValue{{Key: "listen.address", Value: sv("0.0.0.0")}})
	ent("ifB", model.TypeNetworkInterface, model.KeyValue{Key: "interface.name", Value: sv("eth0")})
	ent("addrB", model.TypeNetworkAddress, model.KeyValue{Key: "network.address", Value: sv("10.0.0.5")})

	rel("d1", model.RelDependsOn, "instA", "ep1")
	rel("r1", model.RelRunsOn, "lisB", "hb")
	rel("r2", model.RelHasInterface, "hb", "ifB")
	rel("r3", model.RelBoundTo, "addrB", "ifB")

	s := New(g, &fakeStore{})
	_, out, err := s.getNeighbors(context.Background(), nil, GetNeighborsInput{
		EntityID: "instA", RelationType: model.RelDependsOn, Depth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 {
		t.Fatalf("want 1 neighbor, got %d", out.Count)
	}
	nb := out.Neighbors[0]
	if nb.Type != model.TypeNetworkEndpoint {
		t.Fatalf("neighbor type = %s, want network.endpoint", nb.Type)
	}
	if nb.ResolvedEntity == nil {
		t.Fatal("endpoint neighbor should carry a resolved_entity")
	}
	if nb.ResolvedEntity.ID != "lisB" {
		t.Fatalf("resolved to %s, want listener lisB", nb.ResolvedEntity.ID)
	}
}
