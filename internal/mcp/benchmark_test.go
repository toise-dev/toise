package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// benchGraph builds a real projection of n hosts, each running one process
// (a runs_on relation), so traversal and conversion are exercised on the same
// read model the server uses in production.
func benchGraph(n int) *projection.Graph {
	g := projection.New()
	for i := 0; i < n; i++ {
		hostID := model.EntityID(fmt.Sprintf("host-%06d", i))
		procID := model.EntityID(fmt.Sprintf("proc-%06d", i))
		g.Apply(model.Event{Entity: &model.EntityEvent{
			ChangeType: model.EntityCreated,
			Entity: model.Entity{ID: hostID, Type: model.TypeHost,
				Identity: []model.KeyValue{{Key: "host.name", Value: model.StringValue(string(hostID))}}},
		}})
		g.Apply(model.Event{Entity: &model.EntityEvent{
			ChangeType: model.EntityCreated,
			Entity: model.Entity{ID: procID, Type: model.TypeProcess,
				Identity: []model.KeyValue{{Key: "process.pid", Value: model.IntValue(int64(i))}}},
		}})
		g.Apply(model.Event{Relation: &model.RelationEvent{
			ChangeType: model.RelationAdded,
			Relation:   model.NewRelation(model.RelRunsOn, procID, hostID),
		}})
	}
	return g
}

// BenchmarkFindEntities measures filtering and rendering entities for the LLM.
// The phase-1 target for the GraphQL sibling read is ≤ 10 ms p99; the MCP layer
// adds only the conversion to output structs over the projection snapshot.
func BenchmarkFindEntities(b *testing.B) {
	s := New(benchGraph(10_000), &fakeStore{})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.findEntities(ctx, nil, FindEntitiesInput{Type: model.TypeHost, Limit: maxLimit}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetNeighbors measures a depth-2 traversal plus output conversion,
// against the ≤ 100 ms p99 target for getNeighbors(depth=2).
func BenchmarkGetNeighbors(b *testing.B) {
	s := New(benchGraph(10_000), &fakeStore{})
	ctx := context.Background()
	in := GetNeighborsInput{EntityID: "host-005000", Depth: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.getNeighbors(ctx, nil, in); err != nil {
			b.Fatal(err)
		}
	}
}
