package mcp

import (
	"context"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

func endpointImpactGraph(t *testing.T) *projection.Graph {
	t.Helper()

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

	ent("svcA", model.TypeServiceInstance, model.KeyValue{Key: "service.instance.id", Value: sv("aaaa")})
	ent("svcC", model.TypeServiceInstance, model.KeyValue{Key: "service.instance.id", Value: sv("cccc")})
	ent("ep1", model.TypeNetworkEndpoint,
		model.KeyValue{Key: "server.address", Value: sv("10.0.0.5")},
		model.KeyValue{Key: "server.port", Value: sv("443")},
		model.KeyValue{Key: "network.transport", Value: sv("tcp")})
	ent("ep2", model.TypeNetworkEndpoint,
		model.KeyValue{Key: "server.address", Value: sv("203.0.113.9")},
		model.KeyValue{Key: "server.port", Value: sv("443")},
		model.KeyValue{Key: "network.transport", Value: sv("tcp")})
	ent("hb", model.TypeHost, model.KeyValue{Key: "host.id", Value: sv("hb-uuid")})
	entAttr("lisB", model.TypeServiceListener,
		[]model.KeyValue{{Key: "service.endpoint", Value: sv("hb-uuid:443/tcp")}},
		[]model.KeyValue{{Key: "listen.address", Value: sv("0.0.0.0")}})
	ent("ifB", model.TypeNetworkInterface, model.KeyValue{Key: "interface.name", Value: sv("eth0")})
	ent("addrB", model.TypeNetworkAddress, model.KeyValue{Key: "network.address", Value: sv("10.0.0.5")})

	rel("d1", model.RelDependsOn, "svcA", "ep1")
	rel("d2", model.RelDependsOn, "svcC", "ep2")
	rel("r1", model.RelRunsOn, "lisB", "hb")
	rel("r2", model.RelHasInterface, "hb", "ifB")
	rel("r3", model.RelBoundTo, "addrB", "ifB")

	return g
}

func impactedByID(out ImpactOfOutput) map[string]ImpactedEntity {
	m := make(map[string]ImpactedEntity, len(out.Impacted))
	for _, e := range out.Impacted {
		m[e.ID] = e
	}
	return m
}

func TestImpactOfFollowsEndpointResolutionToListener(t *testing.T) {
	g := endpointImpactGraph(t)
	s := New(g, &fakeStore{})

	_, out, err := s.impactOf(context.Background(), nil, ImpactOfInput{EntityID: "lisB"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 2 {
		t.Fatalf("Total = %d, want 2 (impacted: %d)", out.Total, len(out.Impacted))
	}
	byID := impactedByID(out)

	ep, ok := byID["ep1"]
	if !ok {
		t.Fatal("ep1 missing from impact_of(lisB)")
	}
	if ep.Via != "resolved_endpoint" {
		t.Errorf("ep1 Via = %q, want resolved_endpoint", ep.Via)
	}
	if ep.Depth != 1 {
		t.Errorf("ep1 Depth = %d, want 1", ep.Depth)
	}

	svc, ok := byID["svcA"]
	if !ok {
		t.Fatal("svcA missing from impact_of(lisB)")
	}
	if svc.Via != model.RelDependsOn {
		t.Errorf("svcA Via = %q, want depends_on", svc.Via)
	}
	if svc.Depth != 2 {
		t.Errorf("svcA Depth = %d, want 2", svc.Depth)
	}

	if out.Summary == "Nothing depends on" {
		t.Errorf("summary should not report nothing depends on, got %q", out.Summary)
	}
}

func TestImpactOfFollowsEndpointResolutionFromHost(t *testing.T) {
	g := endpointImpactGraph(t)
	s := New(g, &fakeStore{})

	_, out, err := s.impactOf(context.Background(), nil, ImpactOfInput{EntityID: "hb"})
	if err != nil {
		t.Fatal(err)
	}
	byID := impactedByID(out)
	if _, ok := byID["svcA"]; !ok {
		t.Fatalf("svcA missing from impact_of(hb); impacted: %+v", out.Impacted)
	}
}

func TestImpactOfIgnoresUnresolvedEndpoint(t *testing.T) {
	g := endpointImpactGraph(t)
	s := New(g, &fakeStore{})

	_, out, err := s.impactOf(context.Background(), nil, ImpactOfInput{EntityID: "lisB"})
	if err != nil {
		t.Fatal(err)
	}
	byID := impactedByID(out)
	for _, id := range []string{"ep2", "svcC"} {
		if _, ok := byID[id]; ok {
			t.Errorf("%s should not appear in impact_of(lisB)", id)
		}
	}
	if out.Total != 2 {
		t.Errorf("Total = %d, want 2", out.Total)
	}
}
