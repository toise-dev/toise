package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestHTTPStatelessIgnoresStaleSession guards the Streamable HTTP transport
// against the "session expired" regression: a client that replays a session id
// the server has long forgotten (idle eviction, restart) must still be served.
//
// In stateful mode the SDK answers an unknown Mcp-Session-Id with 404 "session
// not found"; statelessness (server.go) makes the same request succeed, because
// the tools are pure reads with nothing to retain between calls.
func TestHTTPStatelessIgnoresStaleSession(t *testing.T) {
	srv := httptest.NewServer(newTestServer().HTTPHandler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"find_entities","arguments":{"type":"host"}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	// A session id the server never issued — as if it had expired server-side.
	req.Header.Set("Mcp-Session-Id", "stale-session-evicted-long-ago")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for a call carrying a stale session id, got %d", resp.StatusCode)
	}

	rpc := readSSEResult(t, resp)
	if _, isErr := rpc["error"]; isErr {
		t.Fatalf("JSON-RPC error on a stale-session call: %v", rpc["error"])
	}
	result, ok := rpc["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result object in response: %v", rpc)
	}
	sc, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent in tool result: %v", result)
	}
	if total, _ := sc["total"].(float64); total != 2 {
		t.Fatalf("want 2 hosts over the wire, got %v", sc["total"])
	}
}

// readSSEResult decodes the single JSON-RPC message the Streamable HTTP
// transport returns as a text/event-stream "data:" line.
func readSSEResult(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			t.Fatalf("decode SSE data line: %v", err)
		}
		return msg
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	t.Fatal("no data line in SSE response")
	return nil
}
