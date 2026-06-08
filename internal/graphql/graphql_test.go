package graphql_test

import (
	"context"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/graphql"
	"github.com/toise-dev/toise/internal/graphql/resolvers"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

var t0 = time.Unix(1_700_000_000, 0).UTC()

func kv(k, v string) model.KeyValue { return model.KeyValue{Key: k, Value: model.StringValue(v)} }

type stack struct {
	res    *resolvers.Resolver
	engine *change.Engine
	hostID model.EntityID
	procID model.EntityID
}

func newStack(t *testing.T) *stack {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g := projection.New()
	eng := change.New(g, st, change.WithClock(func() time.Time { return t0 }))

	hostObs := change.EntityObservation{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}, Attributes: []model.KeyValue{kv("status", "up")}, EventTime: t0}
	he, _ := eng.ObserveEntity(hostObs)
	// a second observation: status flip -> a state change in history
	hostObs.Attributes = []model.KeyValue{kv("status", "down")}
	hostObs.EventTime = t0.Add(time.Minute)
	_, _ = eng.ObserveEntity(hostObs)

	pe, _ := eng.ObserveEntity(change.EntityObservation{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}, EventTime: t0})
	_, _, _ = eng.ObserveRelation(change.RelationObservation{
		Type:      model.RelRunsOn,
		From:      change.EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "100")}},
		To:        change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}},
		EventTime: t0,
	})

	res := &resolvers.Resolver{Graph: g, Store: st, Engine: eng, Now: func() time.Time { return t0.Add(2 * time.Minute) }}
	return &stack{res: res, engine: eng, hostID: he.Entity.Entity.ID, procID: pe.Entity.Entity.ID}
}

func (s *stack) client(t *testing.T) *client.Client {
	return client.New(graphql.NewHandler(s.res, graphql.Config{}))
}

func TestIntrospectionToggle(t *testing.T) {
	s := newStack(t)
	const q = `{ __schema { queryType { name } } }`
	var resp struct {
		Schema struct {
			QueryType struct{ Name string }
		} `json:"__schema"`
	}

	on := client.New(graphql.NewHandler(s.res, graphql.Config{}))
	if err := on.Post(q, &resp); err != nil {
		t.Errorf("introspection should work by default: %v", err)
	}

	off := client.New(graphql.NewHandler(s.res, graphql.Config{DisableIntrospection: true}))
	if err := off.Post(q, &resp); err == nil {
		t.Error("introspection query should be rejected when DisableIntrospection is set")
	}
}

func TestEntityQuery(t *testing.T) {
	s := newStack(t)
	c := s.client(t)
	var resp struct {
		Entity struct {
			ID         string
			Type       string
			Attributes []struct {
				Key, Value string
				Type       string
			}
		}
	}
	c.MustPost(`query($id:ID!){ entity(id:$id){ id type attributes{ key value type } } }`, &resp, client.Var("id", string(s.hostID)))
	if resp.Entity.Type != model.TypeHost {
		t.Fatalf("type = %q, want host", resp.Entity.Type)
	}
	if len(resp.Entity.Attributes) != 1 || resp.Entity.Attributes[0].Key != "status" || resp.Entity.Attributes[0].Value != "down" {
		t.Errorf("attributes = %+v", resp.Entity.Attributes)
	}
}

func TestEntitiesPagination(t *testing.T) {
	s := newStack(t)
	c := s.client(t)
	var page1 struct {
		Entities struct {
			Edges []struct {
				Node struct{ ID string }
			}
			PageInfo struct {
				HasNextPage bool
				EndCursor   string
			}
			TotalCount int
		}
	}
	c.MustPost(`{ entities(first:1){ edges{ node{ id } } pageInfo{ hasNextPage endCursor } totalCount } }`, &page1)
	if page1.Entities.TotalCount != 2 {
		t.Fatalf("totalCount = %d, want 2", page1.Entities.TotalCount)
	}
	if len(page1.Entities.Edges) != 1 || !page1.Entities.PageInfo.HasNextPage {
		t.Fatalf("page1 = %+v", page1.Entities)
	}

	var page2 struct {
		Entities struct {
			Edges []struct {
				Node struct{ ID string }
			}
			PageInfo struct{ HasNextPage bool }
		}
	}
	c.MustPost(`query($a:String!){ entities(first:1, after:$a){ edges{ node{ id } } pageInfo{ hasNextPage } } }`, &page2, client.Var("a", page1.Entities.PageInfo.EndCursor))
	if len(page2.Entities.Edges) != 1 || page2.Entities.PageInfo.HasNextPage {
		t.Errorf("page2 = %+v", page2.Entities)
	}
	if page2.Entities.Edges[0].Node.ID == page1.Entities.Edges[0].Node.ID {
		t.Error("page2 returned the same node as page1")
	}
}

func TestRelationsQuery(t *testing.T) {
	s := newStack(t)
	c := s.client(t)
	var resp struct {
		Relations struct {
			Edges []struct {
				Node struct {
					Type       string
					Structural bool
				}
			}
			TotalCount int
		}
	}
	c.MustPost(`{ relations{ edges{ node{ type structural } } totalCount } }`, &resp)
	if resp.Relations.TotalCount != 1 || resp.Relations.Edges[0].Node.Type != model.RelRunsOn || !resp.Relations.Edges[0].Node.Structural {
		t.Errorf("relations = %+v", resp.Relations)
	}
}

func TestEntityHistory(t *testing.T) {
	s := newStack(t)
	c := s.client(t)
	var resp struct {
		EntityHistory struct {
			Edges []struct {
				Node struct{ ChangeType string }
			}
			TotalCount int
		}
	}
	c.MustPost(`query($id:ID!){ entityHistory(id:$id){ edges{ node{ changeType } } totalCount } }`, &resp, client.Var("id", string(s.hostID)))
	// the host timeline includes its 2 entity events plus the runs_on relation
	// added to it (relations are indexed under both endpoints)
	if resp.EntityHistory.TotalCount != 3 {
		t.Fatalf("history totalCount = %d, want 3 (created + state_changed + relation added)", resp.EntityHistory.TotalCount)
	}
	if resp.EntityHistory.Edges[0].Node.ChangeType != "ENTITY_CREATED" {
		t.Errorf("first history event = %s, want ENTITY_CREATED", resp.EntityHistory.Edges[0].Node.ChangeType)
	}
}

func TestRecentChanges(t *testing.T) {
	s := newStack(t)
	c := s.client(t)
	var resp struct {
		RecentChanges struct{ TotalCount int }
	}
	c.MustPost(`{ recentChanges(window:"1h"){ totalCount } }`, &resp)
	// host created + host state_changed + process created + relation added = 4
	if resp.RecentChanges.TotalCount != 4 {
		t.Errorf("recentChanges totalCount = %d, want 4", resp.RecentChanges.TotalCount)
	}
}

func TestComplexityLimit(t *testing.T) {
	s := newStack(t)
	c := client.New(graphql.NewHandler(s.res, graphql.Config{ComplexityLimit: 1}))
	var resp struct {
		Entities struct{ TotalCount int }
	}
	err := c.Post(`{ entities(first:50){ edges{ node{ id type attributes{ key value } } } totalCount } }`, &resp)
	if err == nil {
		t.Fatal("expected a complexity-limit error")
	}
}

func TestSubscriptionEntityChanged(t *testing.T) {
	s := newStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := s.res.Subscription().EntityChanged(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// trigger a new entity change
	_, _ = s.engine.ObserveEntity(change.EntityObservation{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h2")}, EventTime: t0})
	select {
	case ce := <-ch:
		if ce.ChangeType != "ENTITY_CREATED" {
			t.Errorf("subscription event = %s, want ENTITY_CREATED", ce.ChangeType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscription event")
	}
}
