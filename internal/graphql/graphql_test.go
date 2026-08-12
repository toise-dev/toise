package graphql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/graphql"
	"github.com/toise-dev/toise/internal/graphql/generated"
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

// TestAnnotateEntityMutation pins the 0.7.0 annotate surface on GraphQL: the
// annotateEntity mutation merges an overlay and the Entity.annotations field
// surfaces it. The overlay is never producer truth (no event-log roundtrip).
func TestAnnotateEntityMutation(t *testing.T) {
	s := newStack(t)
	annStore, err := annotations.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open annotations: %v", err)
	}
	t.Cleanup(func() { _ = annStore.Close() })
	s.res.Annotations = annStore
	c := s.client(t)

	var mut struct {
		AnnotateEntity struct {
			Values    []struct{ Key, Value string }
			Author    *string
			UpdatedAt *string
		}
	}
	c.MustPost(
		`mutation($id:ID!){ annotateEntity(id:$id, annotations:[{key:"owner",value:"sre"},{key:"ticket",value:"OPS-1"}]){ values{ key value } author updatedAt } }`,
		&mut, client.Var("id", string(s.hostID)))
	if len(mut.AnnotateEntity.Values) != 2 {
		t.Fatalf("want 2 annotation entries, got %+v", mut.AnnotateEntity.Values)
	}
	if mut.AnnotateEntity.UpdatedAt == nil || *mut.AnnotateEntity.UpdatedAt == "" {
		t.Error("updatedAt must be set")
	}

	var q struct {
		Entity struct {
			Annotations *struct {
				Values []struct{ Key, Value string }
			}
		}
	}
	c.MustPost(`query($id:ID!){ entity(id:$id){ annotations{ values{ key value } } } }`, &q, client.Var("id", string(s.hostID)))
	if q.Entity.Annotations == nil || len(q.Entity.Annotations.Values) != 2 {
		t.Fatalf("annotations not surfaced on entity: %+v", q.Entity.Annotations)
	}
	if q.Entity.Annotations.Values[0].Key != "owner" {
		t.Errorf("entries must be sorted by key, got %+v", q.Entity.Annotations.Values)
	}

	// An entity with no annotation surfaces null.
	var none struct {
		Entity struct {
			Annotations *struct{ Values []struct{ Key string } }
		}
	}
	c.MustPost(`query($id:ID!){ entity(id:$id){ annotations{ values{ key } } } }`, &none, client.Var("id", string(s.procID)))
	if none.Entity.Annotations != nil {
		t.Errorf("un-annotated entity must surface null, got %+v", none.Entity.Annotations)
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

// TestEntitiesAttributeMatch pins the EntityFilter.match parity with the MCP
// find_entities tool: AND-matching on identifying and descriptive attributes,
// with the filtered count reflected in totalCount.
func TestEntitiesAttributeMatch(t *testing.T) {
	s := newStack(t)
	c := s.client(t)
	var resp struct {
		Entities struct {
			TotalCount int
			Edges      []struct {
				Node struct{ ID, Type string }
			}
		}
	}

	// Descriptive attribute: only the host carries status=down (current state).
	c.MustPost(`{ entities(filter: {match: [{key:"status", value:"down"}]}) { totalCount edges { node { id type } } } }`, &resp)
	if resp.Entities.TotalCount != 1 || len(resp.Entities.Edges) != 1 || resp.Entities.Edges[0].Node.Type != model.TypeHost {
		t.Fatalf("status=down match = %+v, want exactly the host", resp.Entities)
	}

	// Identifying attribute matches too.
	c.MustPost(`{ entities(filter: {match: [{key:"host.id", value:"h1"}]}) { totalCount } }`, &resp)
	if resp.Entities.TotalCount != 1 {
		t.Fatalf("host.id=h1 match total = %d, want 1", resp.Entities.TotalCount)
	}

	// AND with type, and a miss: the host's current status is down, not up.
	c.MustPost(`{ entities(filter: {type:"host", match: [{key:"status", value:"up"}]}) { totalCount } }`, &resp)
	if resp.Entities.TotalCount != 0 {
		t.Fatalf("status=up match total = %d, want 0 (current state is down)", resp.Entities.TotalCount)
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
	ch, err := s.res.Subscription().EntityChanged(ctx, nil)
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

// TestEntitiesAsOf pins the GraphQL half of #135: asOf folds the log to the
// requested instant, mirroring the MCP tools' as_of.
func TestEntitiesAsOf(t *testing.T) {
	s := newStack(t)
	c := s.client(t)

	var resp struct {
		Entities struct {
			TotalCount int
			Edges      []struct {
				Node struct {
					ID         string
					Attributes []struct{ Key, Value string }
				}
			}
		}
	}
	// Before anything existed.
	q := fmt.Sprintf(`{ entities(asOf: %q) { totalCount edges { node { id } } } }`,
		t0.Add(-time.Second).Format(time.RFC3339))
	if err := c.Post(q, &resp); err != nil {
		t.Fatalf("asOf pre-genesis: %v", err)
	}
	if resp.Entities.TotalCount != 0 {
		t.Fatalf("pre-genesis totalCount = %d, want 0", resp.Entities.TotalCount)
	}

	// Thirty seconds in: both entities exist, the status flip (t0+1m) has not
	// happened yet — the host must still read "up".
	q = fmt.Sprintf(`{ entities(asOf: %q) { totalCount edges { node { id attributes { key value } } } } }`,
		t0.Add(30*time.Second).Format(time.RFC3339))
	if err := c.Post(q, &resp); err != nil {
		t.Fatalf("asOf mid-history: %v", err)
	}
	if resp.Entities.TotalCount != 2 {
		t.Fatalf("mid-history totalCount = %d, want 2", resp.Entities.TotalCount)
	}
	for _, e := range resp.Entities.Edges {
		for _, a := range e.Node.Attributes {
			if a.Key == "status" && a.Value != "up" {
				t.Fatalf("as-of host status = %q, want the pre-flip value up", a.Value)
			}
		}
	}

	// Live (no asOf): the flip is visible.
	if err := c.Post(`{ entities { edges { node { attributes { key value } } } } }`, &resp); err != nil {
		t.Fatal(err)
	}
	down := false
	for _, e := range resp.Entities.Edges {
		for _, a := range e.Node.Attributes {
			if a.Key == "status" && a.Value == "down" {
				down = true
			}
		}
	}
	if !down {
		t.Fatal("live read must see the status flip")
	}

	// Malformed asOf is a clear error.
	if err := c.Post(`{ entities(asOf: "yesterday") { totalCount } }`, &resp); err == nil {
		t.Fatal("invalid asOf must error")
	}

	// Pre-epoch asOf is refused (#165): the time index is unsigned, so a
	// pre-1970 instant would cover the whole log and answer with the full
	// CURRENT graph dressed up as ancient history.
	err := c.Post(`{ entities(asOf: "1950-01-01T00:00:00Z") { totalCount } }`, &resp)
	if err == nil || !strings.Contains(err.Error(), "1970") {
		t.Fatalf("pre-epoch asOf = %v, want a pre-1970 rejection", err)
	}
}

// TestSubscriptionFiltersAndGapSignal pins #138: server-side filters deliver
// only matching events, and a consumer that falls behind learns it in-band —
// the next delivered event carries the count of drops, never a silent gap.
func TestSubscriptionFiltersAndGapSignal(t *testing.T) {
	s := newStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Filter: only host entity events.
	hostType := "host"
	ch, err := s.res.Subscription().EntityChanged(ctx, &generated.ChangeFilter{EntityType: &hostType})
	if err != nil {
		t.Fatal(err)
	}
	// One matching event, one non-matching (process), one relation (never on
	// this stream).
	if _, oerr := s.engine.ObserveEntity(change.EntityObservation{Type: model.TypeHost,
		Identity: []model.KeyValue{kv("host.id", "h-sub")}, EventTime: t0}); oerr != nil {
		t.Fatal(oerr)
	}
	if _, oerr := s.engine.ObserveEntity(change.EntityObservation{Type: model.TypeProcess,
		Identity: []model.KeyValue{kv("pid", "777")}, EventTime: t0}); oerr != nil {
		t.Fatal(oerr)
	}
	got := <-ch
	if got.Entity == nil || got.Entity.Type != "host" {
		t.Fatalf("filtered stream delivered %+v, want the host event", got)
	}
	if got.Dropped != 0 {
		t.Fatalf("first delivery dropped = %d, want 0", got.Dropped)
	}
	select {
	case ev := <-ch:
		t.Fatalf("filter leaked a non-matching event: %+v", ev)
	default:
	}

	// Gap signal: fill the 16-slot buffer without reading, overflow it, then
	// drain — the next delivered event reports exactly the drops.
	const overflow = 3
	for i := 0; i < 16+overflow; i++ {
		if _, oerr := s.engine.ObserveEntity(change.EntityObservation{Type: model.TypeHost,
			Identity: []model.KeyValue{kv("host.id", fmt.Sprintf("h-flood-%d", i))}, EventTime: t0}); oerr != nil {
			t.Fatal(oerr)
		}
	}
	for i := 0; i < 16; i++ {
		if ev := <-ch; ev.Dropped != 0 {
			t.Fatalf("buffered event %d dropped = %d, want 0", i, ev.Dropped)
		}
	}
	if _, oerr := s.engine.ObserveEntity(change.EntityObservation{Type: model.TypeHost,
		Identity: []model.KeyValue{kv("host.id", "h-after-gap")}, EventTime: t0}); oerr != nil {
		t.Fatal(oerr)
	}
	after := <-ch
	if after.Dropped != overflow {
		t.Fatalf("post-gap event dropped = %d, want %d (the gap must be announced)", after.Dropped, overflow)
	}

	// Structural-only relation filter.
	structural := true
	rch, err := s.res.Subscription().RelationChanged(ctx, &generated.ChangeFilter{StructuralOnly: &structural})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, oerr := s.engine.ObserveRelation(change.RelationObservation{
		Type:      model.RelRunsOn,
		From:      change.EndpointRef{Type: model.TypeProcess, Identity: []model.KeyValue{kv("pid", "777")}},
		To:        change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h-sub")}},
		EventTime: t0}); oerr != nil {
		t.Fatal(oerr)
	}
	rev := <-rch
	if rev.Relation == nil || rev.Relation.Type != "runs_on" {
		t.Fatalf("structural stream = %+v, want the runs_on add", rev)
	}
}

// TestCanonicalQuery pins the ADR 0020 identity overlay on GraphQL, which until
// now only MCP exposed: a consumer reading over GraphQL had to reimplement the
// same_as walk and pick its own threshold, and two implementations of "is this
// the same machine?" is exactly the disagreement the overlay exists to prevent.
func TestCanonicalQuery(t *testing.T) {
	s := newStack(t)
	c := s.client(t)

	alias, err := s.engine.ObserveEntity(change.EntityObservation{
		Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1-alias")}, EventTime: t0})
	if err != nil {
		t.Fatal(err)
	}
	aliasID := alias.Entity.Entity.ID

	const q = `query($id:ID!,$asOf:String){ canonical(id:$id,asOf:$asOf){ aliases{ id type label } links{ from to confidence basis } } }`
	var resp struct {
		Canonical *struct {
			Aliases []struct{ ID, Type, Label string }
			Links   []struct {
				From, To, Basis string
				Confidence      float64
			}
		}
	}

	// No same_as yet: an entity that is only itself has no group.
	c.MustPost(q, &resp, client.Var("id", string(s.hostID)))
	if resp.Canonical != nil {
		t.Fatalf("canonical = %+v, want null before any same_as edge", resp.Canonical)
	}

	if _, _, oerr := s.engine.ObserveRelation(change.RelationObservation{
		Type: model.RelSameAs,
		From: change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}},
		To:   change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1-alias")}},
		Attributes: []model.KeyValue{
			{Key: "confidence", Value: model.DoubleValue(0.97)},
			{Key: "basis", Value: model.StringValue("hyperv-kvp")},
		},
		EventTime: t0.Add(5 * time.Minute),
	}); oerr != nil {
		t.Fatal(oerr)
	}

	c.MustPost(q, &resp, client.Var("id", string(s.hostID)))
	if resp.Canonical == nil {
		t.Fatal("canonical = null, want the alias group")
	}
	if len(resp.Canonical.Aliases) != 1 || resp.Canonical.Aliases[0].ID != string(aliasID) {
		t.Fatalf("aliases = %+v, want just the alias (never the entity queried)", resp.Canonical.Aliases)
	}
	if got := resp.Canonical.Aliases[0].Label; got != "host host.id=h1-alias" {
		t.Errorf("label = %q, want the identity rendering", got)
	}
	if len(resp.Canonical.Links) != 1 || resp.Canonical.Links[0].Confidence != 0.97 {
		t.Fatalf("links = %+v, want the supporting edge with its confidence", resp.Canonical.Links)
	}
	if got := resp.Canonical.Links[0].Basis; got != "hyperv-kvp" {
		t.Errorf("basis = %q, want the plain producer value", got)
	}

	// The group is symmetric: asking from the alias returns the host.
	resp.Canonical = nil
	c.MustPost(q, &resp, client.Var("id", string(aliasID)))
	if resp.Canonical == nil || len(resp.Canonical.Aliases) != 1 || resp.Canonical.Aliases[0].ID != string(s.hostID) {
		t.Fatalf("from the alias, canonical = %+v, want the host", resp.Canonical)
	}

	// asOf reads the belief as it stood then — the reason this is a top-level
	// query rather than a field on Entity.
	resp.Canonical = nil
	c.MustPost(q, &resp, client.Var("id", string(s.hostID)), client.Var("asOf", t0.Add(time.Minute).Format(time.RFC3339)))
	if resp.Canonical != nil {
		t.Fatalf("canonical asOf before the edge = %+v, want null", resp.Canonical)
	}

	// An unknown entity is null, not an error.
	resp.Canonical = nil
	c.MustPost(q, &resp, client.Var("id", "no-such-entity"))
	if resp.Canonical != nil {
		t.Fatalf("canonical of an unknown id = %+v, want null", resp.Canonical)
	}
}

// TestCanonicalThresholdGate: a weak belief stays in the graph and collapses
// nothing. GraphQL must gate on the same configured confidence as MCP.
func TestCanonicalThresholdGate(t *testing.T) {
	s := newStack(t)
	if _, err := s.engine.ObserveEntity(change.EntityObservation{
		Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1-weak")}, EventTime: t0}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.engine.ObserveRelation(change.RelationObservation{
		Type:       model.RelSameAs,
		From:       change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1")}},
		To:         change.EndpointRef{Type: model.TypeHost, Identity: []model.KeyValue{kv("host.id", "h1-weak")}},
		Attributes: []model.KeyValue{{Key: "confidence", Value: model.DoubleValue(0.5)}},
		EventTime:  t0,
	}); err != nil {
		t.Fatal(err)
	}

	const q = `query($id:ID!){ canonical(id:$id){ aliases{ id } } }`
	var resp struct {
		Canonical *struct{ Aliases []struct{ ID string } }
	}

	client.New(graphql.NewHandler(s.res, graphql.Config{})).MustPost(q, &resp, client.Var("id", string(s.hostID)))
	if resp.Canonical != nil {
		t.Fatalf("canonical = %+v, want null: 0.5 is below the default 0.9", resp.Canonical)
	}

	// The same edge collapses for a deployment that lowered the bar.
	s.res.IdentityThreshold = 0.4
	client.New(graphql.NewHandler(s.res, graphql.Config{})).MustPost(q, &resp, client.Var("id", string(s.hostID)))
	if resp.Canonical == nil || len(resp.Canonical.Aliases) != 1 {
		t.Fatalf("canonical = %+v, want the alias at threshold 0.4", resp.Canonical)
	}
}
