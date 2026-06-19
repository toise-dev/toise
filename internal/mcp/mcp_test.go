package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

func (g *fakeGraph) RelationsTouching(id model.EntityID, relType string) []model.Relation {
	var out []model.Relation
	for _, r := range g.relations {
		if relType != "" && r.Type != relType {
			continue
		}
		if r.From == id || r.To == id {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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
	horizon  time.Time
}

func (s *fakeStore) PruneHorizon() time.Time { return s.horizon }

func (s *fakeStore) ReadByEntity(_ context.Context, id model.EntityID) ([]model.Event, error) {
	return s.byEntity[id], nil
}

func (s *fakeStore) ReadByTimeRange(_ context.Context, start, end time.Time) ([]model.Event, error) {
	var out []model.Event
	for _, ev := range s.byTime {
		et, _ := ev.Times()
		if !et.Before(start) && !et.After(end) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *fakeStore) ScanByTimeRange(_ context.Context, start, end time.Time, fn func(model.Event) error) error {
	for _, ev := range s.byTime {
		et, _ := ev.Times()
		if !et.Before(start) && !et.After(end) {
			if err := fn(ev); err != nil {
				return err
			}
		}
	}
	return nil
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

// TestVerbosityCompact pins the 0.7.0 verbosity tiers: compact drops identity
// and attributes (cheap to scan), full (default) keeps them, unknown errors.
func TestVerbosityCompact(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	_, full, err := s.findEntities(ctx, nil, FindEntitiesInput{Type: "host"})
	if err != nil || len(full.Entities) == 0 {
		t.Fatalf("full find_entities: %v", err)
	}
	if len(full.Entities[0].Identity) == 0 || len(full.Entities[0].Attributes) == 0 {
		t.Fatal("full must carry identity and attributes")
	}

	_, comp, err := s.findEntities(ctx, nil, FindEntitiesInput{Type: "host", Verbosity: "compact"})
	if err != nil {
		t.Fatal(err)
	}
	e := comp.Entities[0]
	if len(e.Identity) != 0 || len(e.Attributes) != 0 {
		t.Errorf("compact must omit identity/attributes, got %+v", e)
	}
	if e.ID == "" || e.Type == "" || e.Label == "" {
		t.Errorf("compact must keep id/type/label, got %+v", e)
	}

	if _, _, err := s.findEntities(ctx, nil, FindEntitiesInput{Verbosity: "verbose"}); err == nil {
		t.Error("unknown verbosity must error")
	}

	_, ge, _ := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB", Verbosity: "compact"})
	if len(ge.Entity.Identity) != 0 {
		t.Error("get_entity compact must omit identity")
	}
	_, gn, _ := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "01HOST_WEB", MaxDepth: 1, Verbosity: "compact"})
	for _, nb := range gn.Neighbors {
		if len(nb.Identity) != 0 {
			t.Error("get_neighbors compact must omit neighbor identity")
		}
	}
}

func TestGetEntity(t *testing.T) {
	s := newTestServer()
	_, out, err := s.getEntity(context.Background(), nil, GetEntityInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("getEntity: %v", err)
	}
	if out.Entity.Type != "host" || out.Entity.Label != "host host.name=web-server-1" {
		t.Fatalf("unexpected entity: %+v", out.Entity)
	}

	if _, _, err := s.getEntity(context.Background(), nil, GetEntityInput{EntityID: "nope"}); err == nil {
		t.Fatal("expected error for unknown id")
	}
	if _, _, err := s.getEntity(context.Background(), nil, GetEntityInput{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestGetNeighborsDepthCap(t *testing.T) {
	s := newTestServer()
	if _, _, err := s.getNeighbors(context.Background(), nil, GetNeighborsInput{EntityID: "01HOST_WEB", MaxDepth: 7}); err == nil {
		t.Fatal("expected error exceeding maxDepth")
	}
	_, out, err := s.getNeighbors(context.Background(), nil, GetNeighborsInput{EntityID: "01HOST_WEB", MaxDepth: 2})
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

// get_neighbors digests like the other list tools: Total counts everything
// reachable within max_depth, the result is capped to the limit, and Truncated
// flags the difference (#166 P1).
func TestGetNeighborsLimitDigest(t *testing.T) {
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{"h": {ID: "h", Type: model.TypeHost}},
		deleted:  map[model.EntityID]bool{},
	}
	for i := 0; i < 60; i++ {
		pid := model.EntityID(fmt.Sprintf("p%02d", i))
		g.entities[pid] = model.Entity{ID: pid, Type: model.TypeProcess}
		g.relations = append(g.relations, model.Relation{
			ID: model.RelationID(fmt.Sprintf("r%02d", i)), Type: model.RelRunsOn, From: pid, To: "h",
		})
	}
	s := New(g, &fakeStore{})
	ctx := context.Background()

	_, out, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "h", MaxDepth: 1, Limit: 10})
	if err != nil {
		t.Fatalf("getNeighbors: %v", err)
	}
	if out.Total != 60 || out.Count != 10 || len(out.Neighbors) != 10 || !out.Truncated {
		t.Fatalf("limit=10: total=%d count=%d len=%d truncated=%v, want 60/10/10/true", out.Total, out.Count, len(out.Neighbors), out.Truncated)
	}

	// Default limit (50) still truncates 60.
	_, out, _ = s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "h", MaxDepth: 1})
	if out.Total != 60 || out.Count != 50 || !out.Truncated {
		t.Fatalf("default limit: total=%d count=%d truncated=%v, want 60/50/true", out.Total, out.Count, out.Truncated)
	}

	// A limit above the reachable set returns everything, not truncated.
	_, out, _ = s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "h", MaxDepth: 1, Limit: 200})
	if out.Total != 60 || out.Count != 60 || out.Truncated {
		t.Fatalf("limit=200: total=%d count=%d truncated=%v, want 60/60/false", out.Total, out.Count, out.Truncated)
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

	if _, _, err := s.recentChanges(ctx, nil, RecentChangesInput{}); err != nil {
		t.Fatalf("omitted window should default to 1h, got error: %v", err)
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
	// The governance vocabulary is constant: it must be advertised even on an
	// empty graph, so a consumer learns what it can filter on before any entity
	// exists.
	if len(out.GovernanceAttributes) == 0 {
		t.Error("describe_schema must advertise the governance vocabulary even when empty")
	}
	var sawSemconv, sawProvisional bool
	for _, g := range out.GovernanceAttributes {
		if g.Key == "" || g.Summary == "" {
			t.Errorf("governance attribute missing key/summary: %+v", g)
		}
		if g.Key == "service.criticality" && g.Semconv {
			sawSemconv = true
		}
		if g.Key == "entity.owner.team" && !g.Semconv {
			sawProvisional = true
		}
	}
	if !sawSemconv || !sawProvisional {
		t.Errorf("expected both a semconv key and an entity.* provisional key advertised, got %+v", out.GovernanceAttributes)
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
	if len(tools.Tools) != 12 {
		t.Fatalf("want 12 tools, got %d", len(tools.Tools))
	}

	// Resources and prompts are part of the same surface — exercise them over the
	// real transport too.
	rs, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if len(rs.Resources) != len(resourceCatalog) {
		t.Fatalf("want %d resources, got %d", len(resourceCatalog), len(rs.Resources))
	}
	rr, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "toise://schema"})
	if err != nil {
		t.Fatalf("read schema resource: %v", err)
	}
	if len(rr.Contents) == 0 || rr.Contents[0].MIMEType != "application/json" {
		t.Fatalf("unexpected schema resource: %+v", rr.Contents)
	}
	ps, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(ps.Prompts) != len(promptCatalog) {
		t.Fatalf("want %d prompts, got %d", len(promptCatalog), len(ps.Prompts))
	}
	gp, err := cs.GetPrompt(ctx, &mcpsdk.GetPromptParams{
		Name:      "investigate_incident",
		Arguments: map[string]string{"entity": "db-07"},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(gp.Messages) != 1 {
		t.Fatalf("want one prompt message, got %d", len(gp.Messages))
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
	if _, _, cerr := s.recentChanges(ctx, nil, RecentChangesInput{Window: "24h", ChangeType: "bogus.type"}); cerr == nil {
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

// TestGraphDiff pins the folded-diff semantics: net created/deleted/changed,
// transient flapping surfaced, heartbeat-only churn dropped, totals complete
// even when the lists are truncated.
func TestGraphDiff(t *testing.T) {
	g, _ := newFixture()
	t0 := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ent := func(id, name string) model.Entity { return host(id, name) }
	mkE := func(ct model.ChangeType, e model.Entity, at time.Time, keys ...string) model.Event {
		return model.Event{Entity: &model.EntityEvent{
			EventID: fmt.Sprintf("e-%s-%d", e.ID, at.Unix()), ChangeType: ct, Entity: e,
			EventTime: at, RecordedAt: at, SchemaVersion: "1.0", ChangedKeys: keys,
		}}
	}
	mkR := func(ct model.ChangeType, rel model.Relation, at time.Time) model.Event {
		return model.Event{Relation: &model.RelationEvent{
			EventID: fmt.Sprintf("r-%s-%d", rel.ID, at.Unix()), ChangeType: ct, Relation: rel,
			EventTime: at, RecordedAt: at, SchemaVersion: "1.0",
		}}
	}
	stable, flapper, goner, mutant := ent("E_STABLE", "stable"), ent("E_FLAP", "flap"), ent("E_GONE", "gone"), ent("E_MUT", "mutant")
	relAdd := model.Relation{ID: "R_NEW", Type: "runs_on", From: stable.ID, To: mutant.ID, Structural: true}
	relFlap := model.Relation{ID: "R_FLAP", Type: "connected_to", From: stable.ID, To: goner.ID}
	st := &fakeStore{byTime: []model.Event{
		// created and kept: net created (even with later updates)
		mkE(model.EntityCreated, stable, t0),
		mkE(model.EntityAttributeUpdated, stable, t0.Add(time.Minute), "os.type"),
		// created then deleted: transient
		mkE(model.EntityCreated, flapper, t0.Add(2*time.Minute)),
		mkE(model.EntityDeleted, flapper, t0.Add(3*time.Minute)),
		// pre-existing, deleted in window: net deleted
		mkE(model.EntityDeleted, goner, t0.Add(4*time.Minute)),
		// pre-existing, changed twice: net changed with the union of keys
		mkE(model.EntityStateChanged, mutant, t0.Add(5*time.Minute), "status"),
		mkE(model.EntityAttributeUpdated, mutant, t0.Add(6*time.Minute), "os.type"),
		// heartbeat-only churn: dropped from the diff
		mkE(model.EntityUnchanged, ent("E_HB", "hb"), t0.Add(7*time.Minute)),
		// relations: one net added, one transient
		mkR(model.RelationAdded, relAdd, t0.Add(8*time.Minute)),
		mkR(model.RelationAdded, relFlap, t0.Add(9*time.Minute)),
		mkR(model.RelationRemoved, relFlap, t0.Add(10*time.Minute)),
	}}
	s := New(g, st)
	s.now = func() time.Time { return t0.Add(time.Hour) }
	ctx := context.Background()

	_, out, err := s.graphDiff(ctx, nil, GraphDiffInput{Window: "2h"})
	if err != nil {
		t.Fatalf("graphDiff: %v", err)
	}
	want := DiffTotals{
		EntitiesCreated: 1, EntitiesDeleted: 1, EntitiesChanged: 1, EntitiesTransient: 1,
		RelationsAdded: 1, RelationsRemoved: 0, RelationsChanged: 0, RelationsTransient: 1,
	}
	if out.Totals != want {
		t.Fatalf("totals = %+v, want %+v", out.Totals, want)
	}
	if out.Truncated {
		t.Error("nothing should be truncated at default limit")
	}
	if len(out.EntitiesCreated) != 1 || out.EntitiesCreated[0].ID != "E_STABLE" {
		t.Errorf("created = %+v, want E_STABLE", out.EntitiesCreated)
	}
	if len(out.EntitiesTransient) != 1 || out.EntitiesTransient[0].ID != "E_FLAP" {
		t.Errorf("transient = %+v, want E_FLAP", out.EntitiesTransient)
	}
	if len(out.EntitiesDeleted) != 1 || out.EntitiesDeleted[0].ID != "E_GONE" {
		t.Errorf("deleted = %+v, want E_GONE", out.EntitiesDeleted)
	}
	if len(out.EntitiesChanged) != 1 {
		t.Fatalf("changed = %+v, want one (E_MUT)", out.EntitiesChanged)
	}
	ch := out.EntitiesChanged[0]
	if ch.ID != "E_MUT" || !ch.StateChanged || len(ch.ChangedKeys) != 2 {
		t.Errorf("changed entity = id %s stateChanged=%v keys=%v, want E_MUT/true/[os.type status]", ch.ID, ch.StateChanged, ch.ChangedKeys)
	}
	if len(out.RelationsAdded) != 1 || out.RelationsAdded[0].ID != "R_NEW" {
		t.Errorf("relations added = %+v, want R_NEW", out.RelationsAdded)
	}
	if len(out.RelationsTransient) != 1 || out.RelationsTransient[0].ID != "R_FLAP" {
		t.Errorf("relations transient = %+v, want R_FLAP", out.RelationsTransient)
	}
	if out.Summary == "" || !strings.Contains(out.Summary, "1 entities created") {
		t.Errorf("summary = %q", out.Summary)
	}

	// Truncation: limit 0 -> default; limit 1 keeps totals complete.
	_, out, err = s.graphDiff(ctx, nil, GraphDiffInput{Window: "2h", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Totals != want {
		t.Errorf("totals must be unaffected by limit: %+v", out.Totals)
	}

	// Bounds validation.
	if _, _, berr := s.graphDiff(ctx, nil, GraphDiffInput{}); berr == nil {
		t.Error("expected error with neither window nor from")
	}
	if _, _, berr := s.graphDiff(ctx, nil, GraphDiffInput{Window: "1h", From: "2026-05-29T12:00:00Z"}); berr == nil {
		t.Error("expected error with both window and from")
	}
	if _, _, berr := s.graphDiff(ctx, nil, GraphDiffInput{From: "2026-05-29T12:00:00Z", To: "2026-05-29T11:00:00Z"}); berr == nil {
		t.Error("expected error with to before from")
	}

	// Empty diff reads cleanly.
	_, out, err = s.graphDiff(ctx, nil, GraphDiffInput{From: "2020-01-01T00:00:00Z", To: "2020-01-02T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary != "No net change between the two instants." {
		t.Errorf("empty summary = %q", out.Summary)
	}
}

// TestFindPathAndNeighborEdges pins the traversal contract (#115): shortest
// path with per-hop edge facts, reachable=false as a first-class answer, and
// get_neighbors returning how each entity was reached.
func TestFindPathAndNeighborEdges(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// Fixture topology: proc -runs_on-> web -connected_to-> db.
	_, out, err := s.findPath(ctx, nil, FindPathInput{FromID: "01PROC_NGINX", ToID: "01HOST_DB"})
	if err != nil {
		t.Fatalf("findPath: %v", err)
	}
	if !out.Reachable || out.Hops != 2 {
		t.Fatalf("reachable=%v hops=%d, want true/2", out.Reachable, out.Hops)
	}
	if len(out.Path) != 3 || out.Path[0].Entity.ID != "01PROC_NGINX" || out.Path[2].Entity.ID != "01HOST_DB" {
		t.Fatalf("path = %+v, want proc -> web -> db", out.Path)
	}
	if out.Path[0].ViaRelation != "" {
		t.Error("the start hop must carry no via_relation")
	}
	if out.Path[1].ViaRelation != "runs_on" || out.Path[1].Direction != "outgoing" {
		t.Errorf("hop1 = %s/%s, want runs_on/outgoing", out.Path[1].ViaRelation, out.Path[1].Direction)
	}
	if out.Path[2].ViaRelation != "connected_to" || out.Path[2].Direction != "outgoing" {
		t.Errorf("hop2 = %s/%s, want connected_to/outgoing", out.Path[2].ViaRelation, out.Path[2].Direction)
	}

	// Direction flips when walking against the edges.
	_, back, err := s.findPath(ctx, nil, FindPathInput{FromID: "01HOST_DB", ToID: "01PROC_NGINX"})
	if err != nil {
		t.Fatal(err)
	}
	if !back.Reachable || back.Path[1].Direction != "incoming" {
		t.Errorf("reverse path hop1 direction = %s, want incoming", back.Path[1].Direction)
	}

	// Constrained to one relation type, db is no longer reachable from proc:
	// a first-class false, not an error.
	_, out, err = s.findPath(ctx, nil, FindPathInput{FromID: "01PROC_NGINX", ToID: "01HOST_DB", RelationType: "runs_on"})
	if err != nil {
		t.Fatalf("constrained findPath must not error: %v", err)
	}
	if out.Reachable {
		t.Error("runs_on-only path to db must be unreachable")
	}
	if out.MaxDepth == 0 {
		t.Error("output must echo the applied max_depth so false is interpretable")
	}

	// Same entity: zero hops.
	_, out, _ = s.findPath(ctx, nil, FindPathInput{FromID: "01HOST_WEB", ToID: "01HOST_WEB"})
	if !out.Reachable || out.Hops != 0 || len(out.Path) != 1 {
		t.Errorf("self path = reachable=%v hops=%d len=%d, want true/0/1", out.Reachable, out.Hops, len(out.Path))
	}

	// Unknown endpoints are errors (unlike unreachable).
	if _, _, gerr := s.findPath(ctx, nil, FindPathInput{FromID: "ghost", ToID: "01HOST_DB"}); gerr == nil {
		t.Error("unknown from_id must error")
	}

	// get_neighbors carries the edge facts.
	_, ns, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "01HOST_WEB", MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Count != 2 {
		t.Fatalf("neighbors = %d, want 2 (proc and db)", ns.Count)
	}
	for _, n := range ns.Neighbors {
		switch n.ID {
		case "01PROC_NGINX":
			if n.ViaRelation != "runs_on" || n.Direction != "incoming" || n.Depth != 1 {
				t.Errorf("proc neighbor = %s/%s/d%d, want runs_on/incoming/1", n.ViaRelation, n.Direction, n.Depth)
			}
		case "01HOST_DB":
			if n.ViaRelation != "connected_to" || n.Direction != "outgoing" || n.Depth != 1 {
				t.Errorf("db neighbor = %s/%s/d%d, want connected_to/outgoing/1", n.ViaRelation, n.Direction, n.Depth)
			}
		default:
			t.Errorf("unexpected neighbor %s", n.ID)
		}
	}
}

// TestTelemetryKeys pins the graph-to-telemetry pivot (#115): join keys from
// the entity's own attributes plus those inherited from direct neighbors, with
// the metric-label spelling and the usage caveats — the host.id lesson from
// the recette, encoded as a tool.
func TestTelemetryKeys(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// The process inherits its host's join key through runs_on.
	_, out, err := s.telemetryKeys(ctx, nil, TelemetryKeysInput{EntityID: "01PROC_NGINX"})
	if err != nil {
		t.Fatalf("telemetryKeys: %v", err)
	}
	byKey := map[string]TelemetryKey{}
	for _, k := range out.Keys {
		byKey[k.Key] = k
	}
	pid, ok := byKey["process.pid"]
	if !ok {
		t.Fatalf("process.pid missing from keys: %+v", out.Keys)
	}
	if pid.Source != "identity" || pid.Value != "4242" || pid.MetricLabel != "process_pid" {
		t.Errorf("process.pid = %+v, want identity/4242/process_pid", pid)
	}
	if pid.Note == "" {
		t.Error("process.pid must carry the ephemeral caveat")
	}
	hostName, ok := byKey["host.name"]
	if !ok {
		t.Fatalf("inherited host.name missing: %+v", out.Keys)
	}
	if !strings.Contains(hostName.Source, "via runs_on") {
		t.Errorf("host.name source = %q, want inherited via runs_on", hostName.Source)
	}
	if hostName.Note == "" {
		t.Error("host.name must carry the name-vs-identity caveat")
	}
	if out.Guidance == "" || !strings.Contains(out.Guidance, "underscores") {
		t.Errorf("guidance must explain the metric-label flattening, got %q", out.Guidance)
	}

	// The host's own key is reported once, from its identity, even though a
	// neighbor carries the same attribute key.
	_, out, err = s.telemetryKeys(ctx, nil, TelemetryKeysInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatal(err)
	}
	var hostKeys []TelemetryKey
	for _, k := range out.Keys {
		if k.Key == "host.name" {
			hostKeys = append(hostKeys, k)
		}
	}
	if len(hostKeys) != 1 || hostKeys[0].Source != "identity" || hostKeys[0].Value != "web-server-1" {
		t.Errorf("host.name keys = %+v, want exactly one from identity", hostKeys)
	}

	if _, _, kerr := s.telemetryKeys(ctx, nil, TelemetryKeysInput{}); kerr == nil {
		t.Error("missing entity_id must error")
	}
	if _, _, kerr := s.telemetryKeys(ctx, nil, TelemetryKeysInput{EntityID: "ghost"}); kerr == nil {
		t.Error("unknown entity must error")
	}
}

// blockingStore blocks reads until the caller's context dies, simulating a
// scan over a huge log.
type blockingStore struct{}

func (blockingStore) ReadByEntity(ctx context.Context, _ model.EntityID) ([]model.Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingStore) ReadByTimeRange(ctx context.Context, _, _ time.Time) ([]model.Event, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingStore) ScanByTimeRange(ctx context.Context, _, _ time.Time, _ func(model.Event) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingStore) PruneHorizon() time.Time { return time.Time{} }

type obsCall struct{ tool, outcome string }
type fakeObserver struct{ calls []obsCall }

func (o *fakeObserver) ObserveTool(tool, outcome string, _ time.Duration) {
	o.calls = append(o.calls, obsCall{tool, outcome})
}

// TestObserveRecordsToolCall pins the query-observability seam (#166): the
// per-tool wrapper records the tool name and an ok/error outcome.
func TestObserveRecordsToolCall(t *testing.T) {
	s := newTestServer()
	rec := &fakeObserver{}
	s.SetObserver(rec)

	okHandler := observe(s, "find_entities", s.findEntities)
	if _, _, err := okHandler(context.Background(), nil, FindEntitiesInput{Type: "host"}); err != nil {
		t.Fatalf("ok call: %v", err)
	}
	errHandler := observe(s, "get_entity", s.getEntity)
	if _, _, err := errHandler(context.Background(), nil, GetEntityInput{}); err == nil {
		t.Fatal("expected an error for empty entity id")
	}

	want := []obsCall{{"find_entities", "ok"}, {"get_entity", "error"}}
	if len(rec.calls) != len(want) {
		t.Fatalf("recorded %d calls, want %d: %+v", len(rec.calls), len(want), rec.calls)
	}
	for i, w := range want {
		if rec.calls[i] != w {
			t.Errorf("call %d = %+v, want %+v", i, rec.calls[i], w)
		}
	}
}

// TestToolCallTimeout pins the per-call budget: a tool whose read outlives the
// deadline returns a deadline error instead of hanging the transport (#115).
func TestToolCallTimeout(t *testing.T) {
	g, _ := newFixture()
	s := New(g, blockingStore{})
	s.timeout = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, _, err := withTimeout(s.budget, s.recentChanges)(context.Background(), nil, RecentChangesInput{Window: "1h"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Errorf("timed-out tool returned %v, want a deadline error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool call did not respect its timeout")
	}
}

// TestAsOfReads pins #135: the read tools answer "as it was at T" from the
// event log — entities appear, change, and disappear depending on the instant;
// an as_of older than the retention horizon is refused.
func TestAsOfReads(t *testing.T) {
	t0 := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	web, db := host("01W", "web-1"), host("01D", "db-1")
	webV2 := web
	webV2.Attributes = []model.KeyValue{{Key: "os.type", Value: model.StringValue("linux")}, {Key: "status", Value: model.StringValue("degraded")}}
	mkE := func(ct model.ChangeType, e model.Entity, at time.Time) model.Event {
		return model.Event{Entity: &model.EntityEvent{
			EventID: fmt.Sprintf("e-%s-%d", e.ID, at.Unix()), ChangeType: ct, Entity: e,
			EventTime: at, RecordedAt: at, SchemaVersion: "1.0",
		}}
	}
	rel := model.Relation{ID: "R1", Type: "connected_to", From: web.ID, To: db.ID}
	mkR := func(ct model.ChangeType, at time.Time) model.Event {
		return model.Event{Relation: &model.RelationEvent{
			EventID: fmt.Sprintf("r-%d", at.Unix()), ChangeType: ct, Relation: rel,
			EventTime: at, RecordedAt: at, SchemaVersion: "1.0",
		}}
	}
	st := &fakeStore{byTime: []model.Event{
		mkE(model.EntityCreated, web, t0),                             // 12:00 web appears
		mkE(model.EntityCreated, db, t0.Add(time.Hour)),               // 13:00 db appears
		mkR(model.RelationAdded, t0.Add(2*time.Hour)),                 // 14:00 web->db
		mkE(model.EntityAttributeUpdated, webV2, t0.Add(3*time.Hour)), // 15:00 web degraded
		mkR(model.RelationRemoved, t0.Add(4*time.Hour)),               // 16:00 edge gone
		mkE(model.EntityDeleted, db, t0.Add(5*time.Hour)),             // 17:00 db deleted
	}}
	g := &fakeGraph{entities: map[model.EntityID]model.Entity{}, deleted: map[model.EntityID]bool{}}
	s := New(g, st) // the LIVE graph is empty: every hit below proves the as-of fold answered
	ctx := context.Background()
	at := func(h int) string { return t0.Add(time.Duration(h) * time.Hour).Format(time.RFC3339) }

	// 12:30 — only web exists, no relations.
	_, out, err := s.findEntities(ctx, nil, FindEntitiesInput{AsOf: t0.Add(30 * time.Minute).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || out.Entities[0].ID != "01W" {
		t.Fatalf("12:30 entities = %+v, want only web", out.Entities)
	}

	// 14:30 — both entities, the relation is live, web still healthy.
	_, ds, err := s.describeSchema(ctx, nil, DescribeSchemaInput{AsOf: at(2) + ""})
	if err != nil {
		t.Fatal(err)
	}
	if ds.TotalEntities != 2 || ds.TotalRelations != 1 {
		t.Fatalf("14:00 schema = %d/%d, want 2/1", ds.TotalEntities, ds.TotalRelations)
	}
	_, ge, err := s.getEntity(ctx, nil, GetEntityInput{EntityID: "01W", AsOf: at(2)})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ge.Entity.Attributes {
		if a.Key == "status" {
			t.Fatalf("14:00 web must not yet be degraded: %+v", ge.Entity.Attributes)
		}
	}
	_, fp, err := s.findPath(ctx, nil, FindPathInput{FromID: "01W", ToID: "01D", AsOf: at(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !fp.Reachable || fp.Hops != 1 {
		t.Fatalf("14:00 path = %+v, want reachable in 1 hop", fp)
	}

	// 15:30 — web degraded; 16:30 — edge gone (unreachable, entities live).
	_, ge, err = s.getEntity(ctx, nil, GetEntityInput{EntityID: "01W", AsOf: at(3)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range ge.Entity.Attributes {
		if a.Key == "status" && a.Value == "degraded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("15:00 web must be degraded: %+v", ge.Entity.Attributes)
	}
	_, fp, err = s.findPath(ctx, nil, FindPathInput{FromID: "01W", ToID: "01D", AsOf: at(4)})
	if err != nil {
		t.Fatal(err)
	}
	if fp.Reachable {
		t.Fatal("16:00 path must be unreachable (edge removed)")
	}

	// 17:30 — db deleted: neighbors of web see nothing, find_entities sees one.
	_, ns, err := s.getNeighbors(ctx, nil, GetNeighborsInput{EntityID: "01W", AsOf: at(5)})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Count != 0 {
		t.Fatalf("17:00 neighbors = %d, want 0", ns.Count)
	}

	// Horizon refusal and format validation.
	st.horizon = t0.Add(time.Hour)
	if _, _, herr := s.findEntities(ctx, nil, FindEntitiesInput{AsOf: t0.Add(30 * time.Minute).Format(time.RFC3339)}); herr == nil || !strings.Contains(herr.Error(), "retention horizon") {
		t.Fatalf("pre-horizon as_of = %v, want a retention-horizon error", herr)
	}
	st.horizon = time.Time{}
	if _, _, ferr := s.findEntities(ctx, nil, FindEntitiesInput{AsOf: "yesterday"}); ferr == nil {
		t.Fatal("invalid as_of format must error")
	}

	// Empty as_of still reads the live graph (which is empty here).
	_, out, err = s.findEntities(ctx, nil, FindEntitiesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 0 {
		t.Fatalf("live graph read = %d, want 0", out.Total)
	}
}

// TestPreEpochTimestampsRejected pins #165: the time index encodes event_time
// as unsigned nanoseconds, so a pre-1970 instant wraps above every real key —
// as_of would fold the full current graph and graph_diff would silently come
// back empty. Every event-time input must be refused at the parse boundary.
func TestPreEpochTimestampsRejected(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	const preEpoch = "1950-01-01T00:00:00Z"
	wantRejected := func(tool string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "1970") {
			t.Fatalf("%s with a 1950 instant = %v, want a pre-1970 rejection", tool, err)
		}
	}

	_, _, err := s.findEntities(ctx, nil, FindEntitiesInput{AsOf: preEpoch})
	wantRejected("find_entities as_of", err)
	_, _, err = s.getEntity(ctx, nil, GetEntityInput{EntityID: "01HOST_WEB", AsOf: preEpoch})
	wantRejected("get_entity as_of", err)
	_, _, err = s.graphDiff(ctx, nil, GraphDiffInput{From: preEpoch})
	wantRejected("graph_diff from", err)
	_, _, err = s.graphDiff(ctx, nil, GraphDiffInput{From: preEpoch, To: "2026-05-29T12:00:00Z"})
	wantRejected("graph_diff from/to", err)
	_, _, err = s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB", Since: preEpoch})
	wantRejected("entity_history since", err)
	_, _, err = s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB", Until: preEpoch})
	wantRejected("entity_history until", err)
	_, _, err = s.entityHistory(ctx, nil, EntityHistoryInput{EntityID: "01HOST_WEB", AsKnownAt: preEpoch})
	wantRejected("entity_history as_known_at", err)
}

// TestImpactOf pins the #136 blast-radius semantics on the fixture topology
// (proc -runs_on-> web -connected_to-> db): impact follows each relation
// type's dependency direction and propagates transitively.
func TestImpactOf(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// web fails: proc depends on it (runs_on, To->From) and db shares a
	// connectivity edge (both ways) — two impacted at depth 1.
	_, out, err := s.impactOf(ctx, nil, ImpactOfInput{EntityID: "01HOST_WEB"})
	if err != nil {
		t.Fatalf("impactOf: %v", err)
	}
	if out.Total != 2 || out.Truncated {
		t.Fatalf("web blast radius = %d (truncated=%v), want 2", out.Total, out.Truncated)
	}
	via := map[string]string{}
	for _, e := range out.Impacted {
		via[e.ID] = e.Via
		if e.Depth != 1 {
			t.Errorf("impacted %s at depth %d, want 1", e.ID, e.Depth)
		}
	}
	if via["01PROC_NGINX"] != "runs_on" || via["01HOST_DB"] != "connected_to" {
		t.Errorf("via map = %v", via)
	}
	if !strings.Contains(out.Summary, "impacts 2 entities") {
		t.Errorf("summary = %q", out.Summary)
	}

	// proc fails: nothing depends on it — zero impact is a first-class answer.
	_, out, err = s.impactOf(ctx, nil, ImpactOfInput{EntityID: "01PROC_NGINX"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 0 {
		t.Fatalf("proc blast radius = %d, want 0 (impact must not flow From->To across runs_on)", out.Total)
	}
	if !strings.Contains(out.Summary, "Nothing depends on") {
		t.Errorf("empty summary = %q", out.Summary)
	}

	// db fails: web via connectivity (depth 1), then proc transitively
	// (depth 2, runs_on against web).
	_, out, err = s.impactOf(ctx, nil, ImpactOfInput{EntityID: "01HOST_DB"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 2 {
		t.Fatalf("db blast radius = %d, want 2 (transitive)", out.Total)
	}
	depths := map[string]int{}
	for _, e := range out.Impacted {
		depths[e.ID] = e.Depth
	}
	if depths["01HOST_WEB"] != 1 || depths["01PROC_NGINX"] != 2 {
		t.Errorf("depths = %v, want web:1 proc:2", depths)
	}

	// Limit caps the list, never the totals; bad ids error.
	_, out, err = s.impactOf(ctx, nil, ImpactOfInput{EntityID: "01HOST_DB", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 2 || len(out.Impacted) != 1 || !out.Truncated {
		t.Errorf("limited = total %d, returned %d, truncated %v", out.Total, len(out.Impacted), out.Truncated)
	}
	if _, _, gerr := s.impactOf(ctx, nil, ImpactOfInput{EntityID: "ghost"}); gerr == nil {
		t.Error("unknown entity must error")
	}
}

// TestDescribeType pins #137: the per-type zoom answers from the live graph —
// observed keys with usage, empirical relation shapes, and the relation-kind
// view with endpoint shapes and impact flow.
func TestDescribeType(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	_, out, err := s.describeType(ctx, nil, DescribeTypeInput{Type: "host"})
	if err != nil {
		t.Fatalf("describeType(host): %v", err)
	}
	if out.Kind != "entity" || out.Count != 2 {
		t.Fatalf("host = kind %s count %d, want entity/2", out.Kind, out.Count)
	}
	if len(out.IdentityKeys) == 0 || out.IdentityKeys[0].Key != "host.name" || out.IdentityKeys[0].Seen != 2 {
		t.Errorf("identity keys = %+v, want host.name seen 2x", out.IdentityKeys)
	}
	if len(out.AttributeKeys) == 0 || out.AttributeKeys[0].Key != "os.type" || out.AttributeKeys[0].Example == "" {
		t.Errorf("attribute keys = %+v, want os.type with example", out.AttributeKeys)
	}
	// hosts participate in runs_on (incoming from process) and connected_to.
	var runsOn *RelationParticipation
	for i := range out.Relations {
		if out.Relations[i].RelationType == "runs_on" && out.Relations[i].Direction == "incoming" {
			runsOn = &out.Relations[i]
		}
	}
	if runsOn == nil || runsOn.Count != 1 || len(runsOn.PeerTypes) != 1 || runsOn.PeerTypes[0].Type != "process" {
		t.Fatalf("runs_on participation = %+v", out.Relations)
	}
	if len(out.Samples) == 0 || out.Description == "" {
		t.Error("samples and description must be present")
	}

	// Relation kind: endpoint shapes observed empirically + impact direction.
	_, rel, err := s.describeType(ctx, nil, DescribeTypeInput{Type: "runs_on"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Kind != "relation" || !rel.Registered || rel.Count != 1 || !rel.Structural {
		t.Fatalf("runs_on = %+v", rel)
	}
	if rel.ImpactFlow != "to_from" {
		t.Errorf("runs_on impact = %s, want to_from", rel.ImpactFlow)
	}
	if len(rel.EndpointShapes) != 1 || rel.EndpointShapes[0].FromType != "process" || rel.EndpointShapes[0].ToType != "host" {
		t.Errorf("shapes = %+v, want process->host", rel.EndpointShapes)
	}

	// A registered-but-empty type still answers; garbage errors with the hint.
	_, empty, err := s.describeType(ctx, nil, DescribeTypeInput{Type: "network.route"})
	if err != nil {
		t.Fatalf("registered empty type: %v", err)
	}
	if empty.Count != 0 || empty.Kind != "entity" {
		t.Errorf("network.route = %+v", empty)
	}
	if _, _, gerr := s.describeType(ctx, nil, DescribeTypeInput{Type: "no.such.type"}); gerr == nil || !strings.Contains(gerr.Error(), "describe_schema") {
		t.Errorf("unknown type error = %v", gerr)
	}
}
