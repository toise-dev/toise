package debugui

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/version"
)

//go:embed templates/*.html
var templatesFS embed.FS

// defaultWindow is the look-back window for the dashboard and the changes view;
// defaultWindowLabel is its human form (time.Duration.String would render
// "24h0m0s").
const (
	defaultWindow      = 24 * time.Hour
	defaultWindowLabel = "24h"
)

// entityListCap bounds the entity list page so a huge graph cannot render an
// unbounded table.
const entityListCap = 500

// Graph is the subset of the in-memory projection the debug UI reads current
// state from (ADR 0008).
type Graph interface {
	GetEntity(id model.EntityID) (model.Entity, bool, bool)
	ListEntities(typ string) []model.Entity
	ListRelations(typ string, from, to model.EntityID) []model.Relation
	CountByType() map[string]int
	EntityCount() int
	RelationCount() int
}

// EventReader is the subset of the event log the debug UI reads history from
// (ADR 0007).
type EventReader interface {
	ReadByEntity(ctx context.Context, id model.EntityID) ([]model.Event, error)
	ReadByTimeRange(ctx context.Context, start, end time.Time) ([]model.Event, error)
}

// Handler serves the debug UI over HTTP.
type Handler struct {
	graph       Graph
	store       EventReader
	tenant      string
	listTenants func() []string // nil = no switcher (claim-derived tenancy)
	tmpl        *template.Template
	now         func() time.Time
	mux         *http.ServeMux
}

// New builds a debug UI handler reading from the given projection and event log.
// It parses the embedded templates; an error means a template is malformed,
// which is a programming error the caller should surface at startup.
// New builds the handler for one tenant's graph. listTenants, when non-nil,
// supplies the ids offered by the tenant switcher; pass nil when the tenant is
// derived from verified claims — there the reader must not learn which other
// tenants exist, and could not switch anyway (ADR 0028).
func New(graph Graph, store EventReader, tenantID string, listTenants func() []string) (*Handler, error) {
	tmpl, err := template.New("debugui").ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing debug UI templates: %w", err)
	}
	h := &Handler{graph: graph, store: store, tenant: tenantID, listTenants: listTenants, tmpl: tmpl, now: time.Now}
	h.mux = http.NewServeMux()
	h.mux.HandleFunc("/", h.dashboard)
	h.mux.HandleFunc("/entities", h.entities)
	h.mux.HandleFunc("/entity", h.entity)
	h.mux.HandleFunc("/changes", h.changes)
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// --- view models -------------------------------------------------------------

type pageMeta struct {
	Title   string
	Version string
	// Tenant is the id of the graph this page answers for. Every page says it,
	// because an empty view that does not say WHAT it is empty for is
	// indistinguishable from "nothing was emitted" — the misreading that costs a
	// diagnosis in any multi-tenant deployment (#335).
	Tenant string
	// Tenants, when non-empty, feeds the switcher. Empty under claim-derived
	// tenancy: the reader must not learn which other tenants exist.
	Tenants []string
}

func (h *Handler) meta(title string) pageMeta {
	m := pageMeta{Title: title, Version: version.String(), Tenant: h.tenant}
	if h.listTenants != nil {
		m.Tenants = h.listTenants()
	}
	return m
}

type attrView struct {
	Key   string
	Value string
	Type  string
}

type typeCount struct {
	Type  string
	Count int
}

type changeRow struct {
	EventTime   string
	RecordedAt  string
	Recorded    bool
	ChangeType  string
	Subject     string
	EntityID    string
	ChangedKeys string
	Structural  bool
}

type neighborView struct {
	ID         string
	Label      string
	Type       string
	Via        string
	Structural bool
}

type entityRow struct {
	ID        string
	Type      string
	Label     string
	Deleted   bool
	AttrCount int
}

// --- handlers ----------------------------------------------------------------

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	now := h.now()
	evs, err := h.store.ReadByTimeRange(r.Context(), now.Add(-defaultWindow), now.Add(time.Nanosecond))
	if err != nil {
		h.fail(w, "reading recent changes", err)
		return
	}
	data := struct {
		pageMeta
		TotalEntities  int
		TotalRelations int
		EntityTypes    []typeCount
		RelationTypes  []typeCount
		Window         string
		Recent         []changeRow
	}{
		pageMeta:       h.meta("Dashboard"),
		TotalEntities:  h.graph.EntityCount(),
		TotalRelations: h.graph.RelationCount(),
		EntityTypes:    sortedCounts(h.graph.CountByType()),
		RelationTypes:  sortedCounts(relationCounts(h.graph.ListRelations("", "", ""))),
		Window:         defaultWindowLabel,
		Recent:         h.changeRows(evs, "", 10),
	}
	h.render(w, "dashboard", data)
}

func (h *Handler) entities(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	all := h.graph.ListEntities(typ)
	truncated := len(all) > entityListCap
	shown := all
	if truncated {
		shown = all[:entityListCap]
	}
	rows := make([]entityRow, len(shown)) // ListEntities excludes deleted entities
	for i, e := range shown {
		rows[i] = entityRow{
			ID:        string(e.ID),
			Type:      e.Type,
			Label:     label(e),
			AttrCount: len(e.Attributes),
		}
	}
	data := struct {
		pageMeta
		Type      string
		Types     []string
		Entities  []entityRow
		Total     int
		Truncated bool
	}{
		pageMeta:  h.meta("Entities"),
		Type:      typ,
		Types:     typeNames(h.graph.CountByType()),
		Entities:  rows,
		Total:     len(all),
		Truncated: truncated,
	}
	h.render(w, "entities", data)
}

func (h *Handler) entity(w http.ResponseWriter, r *http.Request) {
	id := model.EntityID(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing id query parameter", http.StatusBadRequest)
		return
	}
	e, ok, deleted := h.graph.GetEntity(id)
	if !ok {
		http.Error(w, "no entity with id "+string(id), http.StatusNotFound)
		return
	}
	evs, err := h.store.ReadByEntity(r.Context(), id)
	if err != nil {
		h.fail(w, "reading entity history", err)
		return
	}
	data := struct {
		pageMeta
		ID         string
		Type       string
		Label      string
		Deleted    bool
		Identity   []attrView
		Attributes []attrView
		Neighbors  []neighborView
		History    []changeRow
	}{
		pageMeta:   h.meta(label(e)),
		ID:         string(e.ID),
		Type:       e.Type,
		Label:      label(e),
		Deleted:    deleted,
		Identity:   attrsView(e.Identity),
		Attributes: attrsView(e.Attributes),
		Neighbors:  h.neighbors(id),
		History:    h.changeRows(evs, "", 0),
	}
	h.render(w, "entity", data)
}

func (h *Handler) changes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window := q.Get("window")
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 {
		d = defaultWindow
		window = defaultWindowLabel
	}
	kind := q.Get("kind")
	switch kind {
	case "", "entity", "relation", "structural":
	default:
		kind = ""
	}
	now := h.now()
	evs, rerr := h.store.ReadByTimeRange(r.Context(), now.Add(-d), now.Add(time.Nanosecond))
	if rerr != nil {
		h.fail(w, "reading changes", rerr)
		return
	}
	rows := h.changeRows(evs, kind, 0)
	data := struct {
		pageMeta
		Window string
		Kind   string
		Kinds  []string
		Count  int
		Recent []changeRow
	}{
		pageMeta: h.meta("Changes"),
		Window:   window,
		Kind:     kind,
		Kinds:    []string{"", "entity", "relation", "structural"},
		Count:    len(rows),
		Recent:   rows,
	}
	h.render(w, "changes", data)
}

// --- helpers -----------------------------------------------------------------

// neighbors builds the directly-connected entities of id, carrying the relation
// type that connects them so the operator can see how they are related.
func (h *Handler) neighbors(id model.EntityID) []neighborView {
	edges := append(h.graph.ListRelations("", id, ""), h.graph.ListRelations("", "", id)...)
	seen := make(map[model.EntityID]struct{}, len(edges))
	out := make([]neighborView, 0, len(edges))
	for _, rel := range edges {
		other := rel.To
		if other == id {
			other = rel.From
		}
		if other == id {
			continue // self-edge
		}
		if _, dup := seen[other]; dup {
			continue
		}
		seen[other] = struct{}{}
		nv := neighborView{ID: string(other), Via: rel.Type, Structural: rel.Structural}
		if e, ok, _ := h.graph.GetEntity(other); ok {
			nv.Label = label(e)
			nv.Type = e.Type
		} else {
			nv.Label = string(other)
		}
		out = append(out, nv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// changeRows converts events to view rows. When kind is non-empty it filters by
// entity/relation/structural. When limit > 0 it keeps at most that many
// (newest-first). With limit 0 it returns all in input order.
func (h *Handler) changeRows(evs []model.Event, kind string, limit int) []changeRow {
	var out []changeRow
	for i := len(evs) - 1; i >= 0; i-- { // newest-first
		ev := evs[i]
		if !keepKind(ev, kind) {
			continue
		}
		out = append(out, h.changeRow(ev))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (h *Handler) changeRow(ev model.Event) changeRow {
	switch {
	case ev.Entity != nil:
		ee := ev.Entity
		return changeRow{
			EventTime:   formatTime(ee.EventTime),
			RecordedAt:  formatTime(ee.RecordedAt),
			Recorded:    !ee.RecordedAt.IsZero(),
			ChangeType:  ee.ChangeType.String(),
			Subject:     label(ee.Entity),
			EntityID:    string(ee.Entity.ID),
			ChangedKeys: strings.Join(ee.ChangedKeys, ", "),
		}
	case ev.Relation != nil:
		re := ev.Relation
		return changeRow{
			EventTime:   formatTime(re.EventTime),
			RecordedAt:  formatTime(re.RecordedAt),
			Recorded:    !re.RecordedAt.IsZero(),
			ChangeType:  re.ChangeType.String(),
			Subject:     h.relationSubject(re.Relation),
			ChangedKeys: strings.Join(re.ChangedKeys, ", "),
			Structural:  re.Relation.Structural,
		}
	default:
		return changeRow{}
	}
}

func (h *Handler) relationSubject(r model.Relation) string {
	return fmt.Sprintf("%s → %s (%s)", h.entityLabel(r.From), h.entityLabel(r.To), r.Type)
}

func (h *Handler) entityLabel(id model.EntityID) string {
	if e, ok, _ := h.graph.GetEntity(id); ok {
		return label(e)
	}
	return string(id)
}

func keepKind(ev model.Event, kind string) bool {
	switch kind {
	case "entity":
		return ev.Entity != nil
	case "relation":
		return ev.Relation != nil
	case "structural":
		return ev.Relation != nil && ev.Relation.Relation.Structural
	default:
		return true
	}
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "rendering page", http.StatusInternalServerError)
	}
}

func (h *Handler) fail(w http.ResponseWriter, what string, err error) {
	http.Error(w, what+": "+err.Error(), http.StatusInternalServerError)
}

func valueString(v model.Value) (str, typ string) {
	switch v.Kind() {
	case model.KindInt:
		return strconv.FormatInt(v.Int(), 10), "int"
	case model.KindDouble:
		return strconv.FormatFloat(v.Double(), 'g', -1, 64), "double"
	case model.KindBool:
		return strconv.FormatBool(v.Bool()), "bool"
	case model.KindArray:
		return v.Display(), "array"
	case model.KindKvlist:
		return v.Display(), "kvlist"
	default:
		return v.Str(), "string"
	}
}

func attrsView(kvs []model.KeyValue) []attrView {
	out := make([]attrView, len(kvs))
	for i, kv := range kvs {
		val, typ := valueString(kv.Value)
		out[i] = attrView{Key: kv.Key, Value: val, Type: typ}
	}
	return out
}

func label(e model.Entity) string {
	var b strings.Builder
	b.WriteString(e.Type)
	for _, kv := range e.Identity {
		val, _ := valueString(kv.Value)
		b.WriteByte(' ')
		b.WriteString(kv.Key)
		b.WriteByte('=')
		b.WriteString(val)
	}
	return b.String()
}

func relationCounts(rels []model.Relation) map[string]int {
	m := make(map[string]int)
	for _, r := range rels {
		m[r.Type]++
	}
	return m
}

func sortedCounts(m map[string]int) []typeCount {
	out := make([]typeCount, 0, len(m))
	for t, n := range m {
		out = append(out, typeCount{Type: t, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func typeNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}
