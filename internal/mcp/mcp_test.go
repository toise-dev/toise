package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
)

// --- fakes -------------------------------------------------------------------

type fakeGraph struct {
	entities  map[model.EntityID]model.Entity
	deleted   map[model.EntityID]bool
	relations []model.Relation
	neighbors map[model.EntityID][]model.Entity
}

func (g *fakeGraph) GetEntity(id model.EntityID) (model.Entity, bool, bool) {
	e, ok := g.entities[id]
	return e, ok, g.deleted[id]
}

func (g *fakeGraph) ListEntities(typ string) []model.Entity {
	var out []model.Entity
	for id, e := range g.entities {
		if g.deleted[id] {
			continue
		}
		if typ != "" && e.Type != typ {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (g *fakeGraph) ListRelations(typ string, from, to model.EntityID) []model.Relation {
	var out []model.Relation
	for _, r := range g.relations {
		if typ != "" && r.Type != typ {
			continue
		}
		if from != "" && r.From != from {
			continue
		}
		if to != "" && r.To != to {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (g *fakeGraph) Neighbors(id model.EntityID, _ string, _ int) []model.Entity {
	return g.neighbors[id]
}

func (g *fakeGraph) CountByType() map[string]int {
	out := make(map[string]int)
	for id, e := range g.entities {
		if !g.deleted[id] {
			out[e.Type]++
		}
	}
	return out
}

func (g *fakeGraph) EntityCount() int {
	n := 0
	for id := range g.entities {
		if !g.deleted[id] {
			n++
		}
	}
	return n
}

func (g *fakeGraph) RelationCount() int { return len(g.relations) }

type fakeStore struct {
	byEntity map[model.EntityID][]model.Event
	byTime   []model.Event
}

func (s *fakeStore) ReadByEntity(id model.EntityID) ([]model.Event, error) {
	return s.byEntity[id], nil
}

func (s *fakeStore) ReadByTimeRange(start, end time.Time) ([]model.Event, error) {
	var out []model.Event
	for _, ev := range s.byTime {
		et, _ := eventTimes(ev)
		if !et.Before(start) && !et.After(end) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// --- fixture -----------------------------------------------------------------

func host(id, name string) model.Entity {
	return model.Entity{
		ID:         model.EntityID(id),
		Type:       "host",
		Identity:   []model.KeyValue{{Key: "host.name", Value: model.StringValue(name)}},
		Attributes: []model.KeyValue{{Key: "os.type", Value: model.StringValue("linux")}},
	}
}

func newFixture() (*fakeGraph, *fakeStore) {
	web := host("01HOST_WEB", "web-server-1")
	db := host("01HOST_DB", "db-server-1")
	proc := model.Entity{
		ID:       "01PROC_NGINX",
		Type:     "process",
		Identity: []model.KeyValue{{Key: "process.pid", Value: model.IntValue(4242)}},
	}
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			web.ID: web, db.ID: db, proc.ID: proc,
		},
		deleted: map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "rel-runs", Type: "runs_on", From: proc.ID, To: web.ID, Structural: true},
			{ID: "rel-conn", Type: "connected_to", From: web.ID, To: db.ID},
		},
		neighbors: map[model.EntityID][]model.Entity{
			web.ID: {proc, db},
		},
	}
	t0 := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	mk := func(eid string, ct model.ChangeType, e model.Entity, et, rt time.Time) model.Event {
		return model.Event{Entity: &model.EntityEvent{
			EventID: eid, ChangeType: ct, Entity: e, EventTime: et, RecordedAt: rt, SchemaVersion: "1.0",
		}}
	}
	hist := []model.Event{
		mk("ev1", model.EntityCreated, web, t0, t0),
		mk("ev2", model.EntityStateChanged, web, t0.Add(time.Hour), t0.Add(time.Hour)),
		// recorded late: known only well after it became true (audit view)
		mk("ev3", model.EntityAttributeUpdated, web, t0.Add(2*time.Hour), t0.Add(10*time.Hour)),
	}
	relEv := model.Event{Relation: &model.RelationEvent{
		EventID: "rev1", ChangeType: model.RelationAdded,
		Relation:  model.Relation{ID: "rel-runs", Type: "runs_on", From: proc.ID, To: web.ID, Structural: true},
		EventTime: t0.Add(30 * time.Minute), RecordedAt: t0.Add(30 * time.Minute), SchemaVersion: "1.0",
	}}
	st := &fakeStore{
		byEntity: map[model.EntityID][]model.Event{web.ID: hist},
		byTime:   append(append([]model.Event{}, hist...), relEv),
	}
	return g, st
}

func newTestServer() *Server {
	g, st := newFixture()
	s := New(g, st)
	s.now = func() time.Time { return time.Date(2026, 5, 29, 20, 0, 0, 0, time.UTC) }
	return s
}

// --- unit tests on handlers --------------------------------------------------

func TestFindEntities(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	_, out, err := s.findEntities(ctx, nil, FindEntitiesInput{Type: "host"})
	if err != nil {
		t.Fatalf("findEntities: %v", err)
	}
	if out.Total != 2 || len(out.Entities) != 2 {
		t.Fatalf("want 2 hosts, got total=%d len=%d", out.Total, len(out.Entities))
	}

	_, out, err = s.findEntities(ctx, nil, FindEntitiesInput{Type: "host", Match: map[string]string{"host.name": "web-server-1"}})
	if err != nil {
		t.Fatalf("findEntities match: %v", err)
	}
	if out.Total != 1 || out.Entities[0].Label != "host host.name=web-server-1" {
		t.Fatalf("unexpected match result: %+v", out)
	}

	// match against a descriptive attribute too
	_, out, _ = s.findEntities(ctx, nil, FindEntitiesInput{Match: map[string]string{"os.type": "linux"}})
	if out.Total != 2 {
		t.Fatalf("want 2 linux hosts, got %d", out.Total)
	}
}

func TestFindEntitiesLimitTruncates(t *testing.T) {
	s := newTestServer()
	_, out, err := s.findEntities(context.Background(), nil, FindEntitiesInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 3 || len(out.Entities) != 1 || !out.Truncated {
		t.Fatalf("want total=3 returned=1 truncated, got %+v", out)
	}
}

func TestGetEntity(t *testing.T) {
	s := newTestServer()
	_, out, err := s.getEntity(context.Background(), nil, GetEntityInput{ID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out.Entity.Type != "host" || out.Entity.Label != "host host.name=web-server-1" {
		t.Fatalf("unexpected entity: %+v", out.Entity)
	}

	if _, _, err := s.getEntity(context.Background(), nil, GetEntityInput{ID: "nope"}); err == nil {
		t.Fatal("expected error for unknown id")
	}
	if _, _, err := s.getEntity(context.Background(), nil, GetEntityInput{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestGetNeighborsDepthCap(t *testing.T) {
	s := newTestServer()
	if _, _, err := s.getNeighbors(context.Background(), nil, GetNeighborsInput{EntityID: "01HOST_WEB", Depth: 7}); err == nil {
		t.Fatal("expected error exceeding maxDepth")
	}
	_, out, err := s.getNeighbors(context.Background(), nil, GetNeighborsInput{EntityID: "01HOST_WEB", Depth: 2})
	if err != nil {
		t.Fatalf("getNeighbors: %v", err)
	}
	if out.Count != 2 {
		t.Fatalf("want 2 neighbors, got %d", out.Count)
	}
	if _, _, err := s.getNeighbors(context.Background(), nil, GetNeighborsInput{EntityID: "ghost"}); err == nil {
		t.Fatal("expected error for unknown entity")
	}
}

func TestEntityHistoryAsKnownAt(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	_, full, err := s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("entityHistory: %v", err)
	}
	if full.Count != 3 {
		t.Fatalf("want 3 changes, got %d", full.Count)
	}
	if full.Changes[0].EventID != "ev1" {
		t.Fatalf("want oldest-first, got %s first", full.Changes[0].EventID)
	}

	// audit cut-off before ev3 was recorded (recorded at t0+10h): ev3 hidden.
	known := "2026-05-29T15:00:00Z"
	_, audit, err := s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB", AsKnownAt: known})
	if err != nil {
		t.Fatalf("entityHistory audit: %v", err)
	}
	if audit.Count != 2 {
		t.Fatalf("audit view should hide the late-recorded ev3, got %d changes", audit.Count)
	}

	if _, _, err := s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB", Since: "nonsense"}); err == nil {
		t.Fatal("expected error for bad since")
	}
}

func TestRecentChanges(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	_, all, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h"})
	if err != nil {
		t.Fatalf("recentChanges: %v", err)
	}
	if all.Count != 4 {
		t.Fatalf("want 4 changes in 24h, got %d", all.Count)
	}
	if all.Changes[0].EventTime < all.Changes[len(all.Changes)-1].EventTime {
		t.Fatal("want newest-first ordering")
	}

	_, structural, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h", Kind: "structural"})
	if err != nil {
		t.Fatal(err)
	}
	if structural.Count != 1 || structural.Changes[0].Relation == nil {
		t.Fatalf("want 1 structural relation change, got %+v", structural)
	}

	if _, _, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "-5m"}); err == nil {
		t.Fatal("expected error for non-positive window")
	}
	if _, _, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "1h", Kind: "bogus"}); err == nil {
		t.Fatal("expected error for bad kind")
	}
}

func TestDescribeSchema(t *testing.T) {
	s := newTestServer()
	_, out, err := s.describeSchema(context.Background(), nil, DescribeSchemaInput{})
	if err != nil {
		t.Fatalf("describeSchema: %v", err)
	}
	if out.TotalEntities != 3 || out.TotalRelations != 2 {
		t.Fatalf("unexpected totals: %+v", out)
	}
	if len(out.EntityTypes) != 2 || out.EntityTypes[0].Type != "host" { // host (2) sorts before process (1)
		t.Fatalf("unexpected entity types: %+v", out.EntityTypes)
	}
	if out.Description == "" {
		t.Fatal("expected a natural-language description")
	}
}

func TestDescribeSchemaEmpty(t *testing.T) {
	s := New(&fakeGraph{entities: map[model.EntityID]model.Entity{}, deleted: map[model.EntityID]bool{}}, &fakeStore{})
	_, out, err := s.describeSchema(context.Background(), nil, DescribeSchemaInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalEntities != 0 || out.Description == "" {
		t.Fatalf("unexpected empty description: %+v", out)
	}
}

// --- end-to-end over the MCP protocol ---------------------------------------

func TestMCPRoundTrip(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ss, err := s.srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Wait() }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 6 {
		t.Fatalf("want 6 tools, got %d", len(tools.Tools))
	}

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "find_entities",
		Arguments: map[string]any{"type": "host"},
	})
	if err != nil {
		t.Fatalf("call find_entities: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	var got FindEntitiesOutput
	raw, _ := json.Marshal(res.StructuredContent)
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("decode structured content: %v", uerr)
	}
	if got.Total != 2 {
		t.Fatalf("want 2 hosts over the wire, got %d", got.Total)
	}

	// a tool error is surfaced as an MCP tool error, not a transport failure
	bad, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "get_neighbors",
		Arguments: map[string]any{"entity_id": "01HOST_WEB", "depth": 9},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !bad.IsError {
		t.Fatal("want IsError for depth exceeding the cap")
	}
}

// TestChangeBudgets pins the #115 result budget on the timeline tools:
// heartbeats excluded by default (opt-in), change_type filter, limit with
// total/truncated, and the per-type digest.
func TestChangeBudgets(t *testing.T) {
	g, st := newFixture()
	web := g.entities["01HOST_WEB"]
	t0 := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	// Flood the window with heartbeats, the dominant noise on a live instance.
	for i := 0; i < 60; i++ {
		hb := model.Event{Entity: &model.EntityEvent{
			EventID: fmt.Sprintf("hb%d", i), ChangeType: model.EntityUnchanged,
			Entity: web, EventTime: t0.Add(time.Duration(i) * time.Minute),
			RecordedAt: t0.Add(time.Duration(i) * time.Minute), SchemaVersion: "1.0",
		}}
		st.byTime = append(st.byTime, hb)
		st.byEntity[web.ID] = append(st.byEntity[web.ID], hb)
	}
	s := New(g, st)
	s.now = func() time.Time { return time.Date(2026, 5, 29, 20, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	// Default: heartbeats out, real changes in, digest reports the exclusion.
	_, out, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 4 || out.Total != 4 || out.Truncated {
		t.Fatalf("default: count=%d total=%d truncated=%v, want 4/4/false", out.Count, out.Total, out.Truncated)
	}
	if out.HeartbeatsExcluded != 60 {
		t.Errorf("HeartbeatsExcluded = %d, want 60", out.HeartbeatsExcluded)
	}
	for _, c := range out.Changes {
		if c.ChangeType == "entity.unchanged" {
			t.Fatal("heartbeat leaked into the default result")
		}
	}
	if len(out.ByChangeType) == 0 || out.ByChangeType[0].Count == 0 {
		t.Errorf("digest missing: %+v", out.ByChangeType)
	}

	// Opt-in heartbeats + limit: bounded with the budget reported.
	_, out, err = s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h", IncludeHeartbeats: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 10 || out.Total != 64 || !out.Truncated {
		t.Fatalf("heartbeats+limit: count=%d total=%d truncated=%v, want 10/64/true", out.Count, out.Total, out.Truncated)
	}

	// change_type filter; asking for entity.unchanged explicitly includes them.
	_, out, err = s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h", ChangeType: "entity.unchanged", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 60 || out.HeartbeatsExcluded != 0 {
		t.Fatalf("change_type=entity.unchanged: total=%d excluded=%d, want 60/0", out.Total, out.HeartbeatsExcluded)
	}
	if _, _, err := s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h", ChangeType: "bogus.type"}); err == nil {
		t.Fatal("expected error for invalid change_type")
	}

	// entity_history: same budget; truncation keeps the newest, oldest-first.
	_, hist, err := s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Count != 3 || hist.HeartbeatsExcluded != 60 {
		t.Fatalf("history default: count=%d excluded=%d, want 3/60", hist.Count, hist.HeartbeatsExcluded)
	}
	_, hist, err = s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB", IncludeHeartbeats: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if hist.Count != 5 || hist.Total != 63 || !hist.Truncated {
		t.Fatalf("history limit: count=%d total=%d truncated=%v, want 5/63/true", hist.Count, hist.Total, hist.Truncated)
	}
	if hist.Changes[0].EventTime > hist.Changes[len(hist.Changes)-1].EventTime {
		t.Fatal("history must stay oldest-first after truncation")
	}
	// the newest events are the ones kept: the tail holds ev2/ev3 (13:00/14:00),
	// the dropped head was the oldest heartbeats.
	if last := hist.Changes[len(hist.Changes)-1]; last.ChangeType != "entity.attribute_updated" {
		t.Fatalf("truncation must keep the newest changes, last = %s", last.ChangeType)
	}
	if first := hist.Changes[0]; first.ChangeType != "entity.unchanged" {
		t.Fatalf("kept slice should start at the newest heartbeats, first = %s", first.ChangeType)
	}
}
