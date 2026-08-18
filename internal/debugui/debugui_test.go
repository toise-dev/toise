package debugui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// --- fakes -------------------------------------------------------------------

type fakeGraph struct {
	entities  map[model.EntityID]model.Entity
	deleted   map[model.EntityID]bool
	relations []model.Relation
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

func (s *fakeStore) ReadByEntity(_ context.Context, id model.EntityID) ([]model.Event, error) {
	return s.byEntity[id], nil
}

func (s *fakeStore) ReadByTimeRange(_ context.Context, start, end time.Time) ([]model.Event, error) {
	var out []model.Event
	for _, ev := range s.byTime {
		et := eventTime(ev)
		if !et.Before(start) && !et.After(end) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func eventTime(ev model.Event) time.Time {
	if ev.Entity != nil {
		return ev.Entity.EventTime
	}
	if ev.Relation != nil {
		return ev.Relation.EventTime
	}
	return time.Time{}
}

// --- fixture -----------------------------------------------------------------

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	web := model.Entity{
		ID:         "01HOST_WEB",
		Type:       "host",
		Identity:   []model.KeyValue{{Key: "host.name", Value: model.StringValue("web-server-1")}},
		Attributes: []model.KeyValue{{Key: "note", Value: model.StringValue("<script>alert(1)</script>")}},
	}
	proc := model.Entity{
		ID:       "01PROC_NGINX",
		Type:     "process",
		Identity: []model.KeyValue{{Key: "process.pid", Value: model.IntValue(4242)}},
	}
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{web.ID: web, proc.ID: proc},
		deleted:  map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "rel-runs", Type: "runs_on", From: proc.ID, To: web.ID, Structural: true},
		},
	}
	t0 := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	entEv := model.Event{Entity: &model.EntityEvent{
		EventID: "ev1", ChangeType: model.EntityCreated, Entity: web,
		EventTime: t0, RecordedAt: t0, SchemaVersion: "1.0",
	}}
	relEv := model.Event{Relation: &model.RelationEvent{
		EventID: "rev1", ChangeType: model.RelationAdded,
		Relation:  model.Relation{ID: "rel-runs", Type: "runs_on", From: proc.ID, To: web.ID, Structural: true},
		EventTime: t0.Add(time.Minute), RecordedAt: t0.Add(time.Minute), SchemaVersion: "1.0",
	}}
	st := &fakeStore{
		byEntity: map[model.EntityID][]model.Event{web.ID: {entEv}},
		byTime:   []model.Event{entEv, relEv},
	}
	h, err := New(g, st, "acme-corp", func() []string { return []string{"acme-corp", "default"} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.now = func() time.Time { return t0.Add(time.Hour) }
	return h
}

func get(t *testing.T, h *Handler, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String()
}

// --- tests -------------------------------------------------------------------

func TestDashboard(t *testing.T) {
	h := newTestHandler(t)
	code, body := get(t, h, "/")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"Live graph overview", "host", "process", "runs_on", "entity.created"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestEntitiesList(t *testing.T) {
	h := newTestHandler(t)
	code, body := get(t, h, "/entities?type=host")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "host host.name=web-server-1") {
		t.Error("missing host label")
	}
	if strings.Contains(body, "01PROC_NGINX") {
		t.Error("type=host filter leaked a process row")
	}
	if !strings.Contains(body, `/entity?id=01HOST_WEB`) {
		t.Error("missing link to entity detail")
	}
}

func TestEntityDetailAndEscaping(t *testing.T) {
	h := newTestHandler(t)
	code, body := get(t, h, "/entity?id=01HOST_WEB")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	// neighbor via relation, history row, identity
	for _, want := range []string{"host.name", "web-server-1", "process process.pid=4242", "runs_on", "entity.created"} {
		if !strings.Contains(body, want) {
			t.Errorf("entity detail missing %q", want)
		}
	}
	// attribute value containing HTML must be escaped, not rendered raw
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("unescaped HTML in attribute value (XSS risk)")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped attribute value")
	}
}

func TestEntityNotFound(t *testing.T) {
	h := newTestHandler(t)
	if code, _ := get(t, h, "/entity?id=ghost"); code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
	if code, _ := get(t, h, "/entity"); code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing id, got %d", code)
	}
}

func TestChangesFilter(t *testing.T) {
	h := newTestHandler(t)
	code, body := get(t, h, "/changes?window=24h&kind=structural")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(body, "relation.added") || !strings.Contains(body, "struct") {
		t.Error("structural filter should show the structural relation change")
	}
	if strings.Contains(body, "entity.created") {
		t.Error("structural filter leaked an entity change")
	}

	// invalid window falls back to default rather than erroring
	if code, _ := get(t, h, "/changes?window=bogus"); code != http.StatusOK {
		t.Fatalf("invalid window should still render, got %d", code)
	}
}

func TestUnknownPath404(t *testing.T) {
	h := newTestHandler(t)
	if code, _ := get(t, h, "/nope"); code != http.StatusNotFound {
		t.Fatalf("want 404 for unknown path, got %d", code)
	}
}

// TestEveryPageNamesItsTenant pins #335: an empty view that does not say WHAT it
// is empty for is indistinguishable from "nothing was emitted". Diagnosed as a
// live incident once — a producer's segments were in another tenant, and the
// obvious page read as "the producer stopped". Every page must name the tenant
// it answers for.
func TestEveryPageNamesItsTenant(t *testing.T) {
	h := newTestHandler(t)
	for _, target := range []string{"/", "/entities", "/changes"} {
		code, body := get(t, h, target)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", target, code)
		}
		if !strings.Contains(body, "tenant acme-corp") {
			t.Errorf("%s does not name its tenant", target)
		}
		if !strings.Contains(body, `<option selected>acme-corp</option>`) || !strings.Contains(body, "<option>default</option>") {
			t.Errorf("%s: switcher missing or current tenant not selected", target)
		}
	}
}

// TestNoSwitcherWithoutList pins the ADR 0028 side of #335: under claim-derived
// tenancy the handler gets no tenant list, and the page must not offer one — a
// scoped reader learning which other tenants exist crosses the isolation
// boundary. The tenant still has to be NAMED; only the enumeration is withheld.
func TestNoSwitcherWithoutList(t *testing.T) {
	h := newTestHandler(t)
	h.listTenants = nil
	_, body := get(t, h, "/")
	if strings.Contains(body, `<select name="tenant"`) {
		t.Error("switcher rendered with no tenant list")
	}
	if !strings.Contains(body, "tenant acme-corp") {
		t.Error("the tenant must still be named without the switcher")
	}
}
