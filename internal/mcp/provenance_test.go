package mcp

import (
	"context"
	"testing"
)

// TestEveryAnswerCarriesGraphMeta pins #346's settled priority: a read answer
// must state what the answering graph holds and how fresh it is. Three
// operator agents independently reported the same blocker — a source that
// does not declare its own scope and freshness cannot enter the arbitration
// between disagreeing sources, so it gets bypassed however correct it is.
// The fixture has 3 entities and 2 relations; every read tool must say so.
func TestEveryAnswerCarriesGraphMeta(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	check := func(name string, m GraphMeta) {
		t.Helper()
		if m.Entities != 3 || m.Relations != 2 {
			t.Errorf("%s: graph block says %d entities / %d relations, want 3/2", name, m.Entities, m.Relations)
		}
		if m.NewestEvent == "" {
			t.Errorf("%s: graph block carries no freshness; a silent source loses the arbitration by default", name)
		}
	}

	_, fe, err := s.findEntities(ctx, nil, FindEntitiesInput{Type: "host"})
	if err != nil {
		t.Fatalf("findEntities: %v", err)
	}
	check("find_entities", fe.Graph)

	_, ge, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	check("get_entity", ge.Graph)

	_, gn, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getNeighbors: %v", err)
	}
	check("get_neighbors", gn.Graph)

	_, rc, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h"})
	if err != nil {
		t.Fatalf("recentChanges: %v", err)
	}
	check("recent_changes", rc.Graph)

	_, ds, err := s.describeSchema(ctx, nil, DescribeSchemaInput{})
	if err != nil {
		t.Fatalf("describeSchema: %v", err)
	}
	check("describe_schema", ds.Graph)

	_, gd, err := s.graphDiff(ctx, nil, GraphDiffInput{Window: "24h"})
	if err != nil {
		t.Fatalf("graphDiff: %v", err)
	}
	check("graph_diff", gd.Graph)
}

// TestGraphMetaNamesThePastInstant pins the as_of half: an answer describing
// the graph as of a past instant must say so in the same block, so a reader
// never mistakes a time-travel view for the present.
func TestGraphMetaNamesThePastInstant(t *testing.T) {
	s := newTestServer()

	_, out, err := s.findEntities(context.Background(), nil, FindEntitiesInput{
		Type: "host", AsOf: "2026-05-29T13:00:00Z",
	})
	if err != nil {
		t.Fatalf("findEntities as_of: %v", err)
	}
	if out.Graph.AsOf != "2026-05-29T13:00:00Z" {
		t.Errorf("as_of answer does not name its instant: %q", out.Graph.AsOf)
	}

	_, live, err := s.findEntities(context.Background(), nil, FindEntitiesInput{Type: "host"})
	if err != nil {
		t.Fatalf("findEntities live: %v", err)
	}
	if live.Graph.AsOf != "" {
		t.Errorf("live answer claims a past instant: %q", live.Graph.AsOf)
	}
}
