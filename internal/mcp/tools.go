package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
)

// --- find_entities ---

// FindEntitiesInput filters the entity set.
type FindEntitiesInput struct {
	Type  string            `json:"type,omitempty" jsonschema:"restrict to this entity type (omit for all types)"`
	Match map[string]string `json:"match,omitempty" jsonschema:"attribute key/value pairs every result must have (string comparison, against identity and attributes)"`
	Limit int               `json:"limit,omitempty" jsonschema:"maximum entities to return (default 50, max 200)"`
	AsOf  string            `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: read the graph as it was then (event-time), instead of now"`
}

// FindEntitiesOutput carries the matching entities.
type FindEntitiesOutput struct {
	Entities  []Entity `json:"entities"`
	Total     int      `json:"total" jsonschema:"number of entities matching the filter before the limit was applied"`
	Truncated bool     `json:"truncated" jsonschema:"true if more entities matched than were returned; narrow the filter or raise the limit"`
}

func (s *Server) findEntities(ctx context.Context, _ *mcpsdk.CallToolRequest, in FindEntitiesInput) (*mcpsdk.CallToolResult, FindEntitiesOutput, error) {
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, FindEntitiesOutput{}, err
	}
	limit := clampLimit(in.Limit)
	all := g.ListEntities(in.Type)
	matched := make([]model.Entity, 0, len(all))
	for _, e := range all {
		if matches(e, in.Match) {
			matched = append(matched, e)
		}
	}
	out := FindEntitiesOutput{Total: len(matched), Truncated: len(matched) > limit}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out.Entities = make([]Entity, len(matched))
	for i, e := range matched {
		out.Entities[i] = entityOut(e, false)
	}
	return nil, out, nil
}

// --- get_entity ---

// GetEntityInput names the entity to fetch.
type GetEntityInput struct {
	EntityID string `json:"entity_id" jsonschema:"the logical entity id to fetch"`
	AsOf     string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: read the entity as it was then (event-time), instead of now"`
}

// GetEntityOutput carries the entity.
type GetEntityOutput struct {
	Entity Entity `json:"entity"`
}

func (s *Server) getEntity(ctx context.Context, _ *mcpsdk.CallToolRequest, in GetEntityInput) (*mcpsdk.CallToolResult, GetEntityOutput, error) {
	if in.EntityID == "" {
		return nil, GetEntityOutput{}, fmt.Errorf("an entity id is required")
	}
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, GetEntityOutput{}, err
	}
	e, ok, deleted := g.GetEntity(model.EntityID(in.EntityID))
	if !ok {
		return nil, GetEntityOutput{}, fmt.Errorf("no entity found with id %q; use find_entities to discover ids — if it was deleted a while ago its tombstone may have been evicted, but entity_history still has its past", in.EntityID)
	}
	return nil, GetEntityOutput{Entity: entityOut(e, deleted)}, nil
}

// --- get_neighbors ---

// GetNeighborsInput parameterises the traversal.
type GetNeighborsInput struct {
	EntityID     string `json:"entity_id" jsonschema:"the entity to traverse outward from"`
	RelationType string `json:"relation_type,omitempty" jsonschema:"only follow relations of this type (omit to follow any)"`
	Depth        int    `json:"depth,omitempty" jsonschema:"how many relation hops to traverse, 1 to 5 (default 1)"`
	AsOf         string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: traverse the graph as it was then (event-time), instead of now"`
}

// Neighbor is a reachable entity plus the edge facts that reached it: the
// relation type, its direction, and the hop distance from the start (#115).
type Neighbor struct {
	Entity
	ViaRelation string `json:"via_relation" jsonschema:"relation type of the edge that first reached this entity"`
	Direction   string `json:"direction" jsonschema:"outgoing if that edge points from the previous hop to this entity, incoming otherwise"`
	Depth       int    `json:"depth" jsonschema:"hop distance from the start entity"`
	// ResolvedEntity is the read-time binding of an observed network.endpoint to
	// the canonical host/service.listener it denotes (#184). The stored edge
	// still targets the endpoint; this is a derived overlay, never persisted.
	ResolvedEntity *Entity `json:"resolved_entity,omitempty" jsonschema:"for an observed network.endpoint, the canonical service.listener/host it resolves to; absent when the peer is external/off-fleet or unresolved"`
}

// GetNeighborsOutput carries the reachable entities with their edges.
type GetNeighborsOutput struct {
	Neighbors []Neighbor `json:"neighbors"`
	Count     int        `json:"count"`
}

func (s *Server) getNeighbors(ctx context.Context, _ *mcpsdk.CallToolRequest, in GetNeighborsInput) (*mcpsdk.CallToolResult, GetNeighborsOutput, error) {
	if in.EntityID == "" {
		return nil, GetNeighborsOutput{}, fmt.Errorf("an entity_id is required")
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > maxDepth {
		return nil, GetNeighborsOutput{}, fmt.Errorf("depth %d exceeds the maximum of %d; try a smaller depth", in.Depth, maxDepth)
	}
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, GetNeighborsOutput{}, err
	}
	start := model.EntityID(in.EntityID)
	if _, ok, _ := g.GetEntity(start); !ok {
		return nil, GetNeighborsOutput{}, fmt.Errorf("no entity found with id %q; use find_entities to discover ids", in.EntityID)
	}
	// BFS through the edge view so each neighbor carries how it was reached;
	// the first (shallowest) edge to reach an entity wins, like a shortest path.
	visited := map[model.EntityID]struct{}{start: {}}
	frontier := []model.EntityID{start}
	out := GetNeighborsOutput{Neighbors: []Neighbor{}}
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		var next []model.EntityID
		for _, cur := range frontier {
			edges := edgesOf(g, cur, in.RelationType)
			for i := range edges {
				e := &edges[i]
				if _, seen := visited[e.other]; seen {
					continue
				}
				visited[e.other] = struct{}{}
				next = append(next, e.other)
				ent, _, _ := g.GetEntity(e.other)
				nb := Neighbor{
					Entity:      entityOut(ent, false),
					ViaRelation: e.rel.Type,
					Direction:   e.direction,
					Depth:       d,
				}
				if ent.Type == model.TypeNetworkEndpoint {
					if resolved, ok := resolveEndpoint(g, ent); ok {
						ro := entityOut(resolved, false)
						nb.ResolvedEntity = &ro
					}
				}
				out.Neighbors = append(out.Neighbors, nb)
			}
		}
		frontier = next
	}
	out.Count = len(out.Neighbors)
	return nil, out, nil
}

// --- entity_history ---

// EntityHistoryInput bounds the timeline.
type EntityHistoryInput struct {
	EntityID  string `json:"entity_id" jsonschema:"the entity whose timeline to return"`
	Since     string `json:"since,omitempty" jsonschema:"RFC 3339 lower bound on event-time (inclusive)"`
	Until     string `json:"until,omitempty" jsonschema:"RFC 3339 upper bound on event-time (inclusive)"`
	AsKnownAt string `json:"as_known_at,omitempty" jsonschema:"RFC 3339 audit cut-off: include only changes Toise had recorded by this instant"`
	// Budget controls (#115): the timeline is heartbeat-dominated on a live
	// instance, so heartbeats are excluded and the result is bounded by default.
	ChangeType        string `json:"change_type,omitempty" jsonschema:"only changes of this type, e.g. entity.state_changed or relation.removed (omit for all)"`
	IncludeHeartbeats bool   `json:"include_heartbeats,omitempty" jsonschema:"include entity.unchanged heartbeats (excluded by default; they dominate raw timelines)"`
	Limit             int    `json:"limit,omitempty" jsonschema:"maximum changes to return (default 50, max 200); when truncated the newest are kept"`
}

// EntityHistoryOutput carries the timeline, oldest first.
type EntityHistoryOutput struct {
	Changes []Change `json:"changes"`
	Count   int      `json:"count" jsonschema:"number of changes returned"`
	ChangeDigest
}

func (s *Server) entityHistory(ctx context.Context, _ *mcpsdk.CallToolRequest, in EntityHistoryInput) (*mcpsdk.CallToolResult, EntityHistoryOutput, error) {
	if in.EntityID == "" {
		return nil, EntityHistoryOutput{}, fmt.Errorf("an entity_id is required")
	}
	since, err := parseOptTime(in.Since, "since")
	if err != nil {
		return nil, EntityHistoryOutput{}, err
	}
	until, err := parseOptTime(in.Until, "until")
	if err != nil {
		return nil, EntityHistoryOutput{}, err
	}
	known, err := parseOptTime(in.AsKnownAt, "as_known_at")
	if err != nil {
		return nil, EntityHistoryOutput{}, err
	}
	filter, err := newChangeFilter(in.ChangeType, in.IncludeHeartbeats)
	if err != nil {
		return nil, EntityHistoryOutput{}, err
	}
	limit := clampLimit(in.Limit)
	evs, err := s.store.ReadByEntity(ctx, model.EntityID(in.EntityID))
	if err != nil {
		return nil, EntityHistoryOutput{}, fmt.Errorf("reading history: %w", err)
	}
	out := EntityHistoryOutput{}
	filtered := evs[:0:0]
	for _, ev := range evs {
		et, rt := ev.Times()
		if !since.IsZero() && et.Before(since) {
			continue
		}
		if !until.IsZero() && et.After(until) {
			continue
		}
		if !known.IsZero() && rt.After(known) {
			continue
		}
		kept, heartbeat := filter.keep(ev)
		if heartbeat {
			out.HeartbeatsExcluded++
		}
		if !kept {
			continue
		}
		out.tally(ev)
		filtered = append(filtered, ev)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		ei, _ := filtered[i].Times()
		ej, _ := filtered[j].Times()
		return ei.Before(ej)
	})
	out.Total = len(filtered)
	if len(filtered) > limit {
		// keep the newest changes; the slice stays sorted oldest first.
		filtered = filtered[len(filtered)-limit:]
		out.Truncated = true
	}
	out.Count = len(filtered)
	out.Changes = make([]Change, len(filtered))
	for i, ev := range filtered {
		out.Changes[i] = changeOut(ev)
	}
	out.finishDigest()
	return nil, out, nil
}

// --- recent_changes ---

// RecentChangesInput selects a window and kind of change.
type RecentChangesInput struct {
	Window string `json:"window,omitempty" jsonschema:"a positive Go duration looking back from now, e.g. 15m, 2h, 24h (default 1h)"`
	Kind   string `json:"kind,omitempty" jsonschema:"filter: entity, relation, structural, or all (default all)"`
	// Budget controls (#115): a live window is heartbeat-dominated, so
	// heartbeats are excluded and the result is bounded by default.
	ChangeType        string `json:"change_type,omitempty" jsonschema:"only changes of this type, e.g. entity.created or relation.removed (omit for all)"`
	IncludeHeartbeats bool   `json:"include_heartbeats,omitempty" jsonschema:"include entity.unchanged heartbeats (excluded by default; they dominate raw windows)"`
	Limit             int    `json:"limit,omitempty" jsonschema:"maximum changes to return (default 50, max 200); the newest are kept"`
}

// RecentChangesOutput carries the changes, newest first.
type RecentChangesOutput struct {
	Changes []Change `json:"changes"`
	Count   int      `json:"count" jsonschema:"number of changes returned"`
	ChangeDigest
}

func (s *Server) recentChanges(ctx context.Context, _ *mcpsdk.CallToolRequest, in RecentChangesInput) (*mcpsdk.CallToolResult, RecentChangesOutput, error) {
	if in.Window == "" {
		in.Window = "1h"
	}
	d, err := time.ParseDuration(in.Window)
	if err != nil || d <= 0 {
		return nil, RecentChangesOutput{}, fmt.Errorf("invalid window %q: use a positive Go duration like 15m, 2h, or 24h", in.Window)
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	switch kind {
	case "", "all", "entity", "relation", "structural":
	default:
		return nil, RecentChangesOutput{}, fmt.Errorf("invalid kind %q: use entity, relation, structural, or all", in.Kind)
	}
	filter, err := newChangeFilter(in.ChangeType, in.IncludeHeartbeats)
	if err != nil {
		return nil, RecentChangesOutput{}, err
	}
	limit := clampLimit(in.Limit)
	now := s.now()
	evs, err := s.store.ReadByTimeRange(ctx, now.Add(-d), now.Add(time.Nanosecond))
	if err != nil {
		return nil, RecentChangesOutput{}, fmt.Errorf("reading changes: %w", err)
	}
	out := RecentChangesOutput{Changes: []Change{}}
	for i := len(evs) - 1; i >= 0; i-- { // newest first
		ev := evs[i]
		if !keepKind(ev, kind) {
			continue
		}
		kept, heartbeat := filter.keep(ev)
		if heartbeat {
			out.HeartbeatsExcluded++
		}
		if !kept {
			continue
		}
		out.Total++
		out.tally(ev)
		if len(out.Changes) < limit {
			out.Changes = append(out.Changes, changeOut(ev))
		}
	}
	out.Truncated = out.Total > limit
	out.Count = len(out.Changes)
	out.finishDigest()
	return nil, out, nil
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

// ChangeDigest summarizes the full match set of a timeline tool so the model
// can budget follow-ups without paging through everything: how many changes
// matched in total, whether the returned slice was truncated, how many
// heartbeats were excluded, and the matching changes by type (#115).
type ChangeDigest struct {
	Total              int         `json:"total" jsonschema:"changes matching the filters before the limit was applied"`
	Truncated          bool        `json:"truncated" jsonschema:"true if more changes matched than were returned; narrow the window or filters, or raise the limit"`
	HeartbeatsExcluded int         `json:"heartbeats_excluded,omitempty" jsonschema:"entity.unchanged heartbeats excluded (set include_heartbeats to see them)"`
	ByChangeType       []TypeCount `json:"by_change_type,omitempty" jsonschema:"matching changes per change type, before the limit"`
}

func (d *ChangeDigest) tally(ev model.Event) {
	ct := eventChangeType(ev).String()
	for i := range d.ByChangeType {
		if d.ByChangeType[i].Type == ct {
			d.ByChangeType[i].Count++
			return
		}
	}
	d.ByChangeType = append(d.ByChangeType, TypeCount{Type: ct, Count: 1})
}

func (d *ChangeDigest) finishDigest() {
	sort.Slice(d.ByChangeType, func(i, j int) bool {
		if d.ByChangeType[i].Count != d.ByChangeType[j].Count {
			return d.ByChangeType[i].Count > d.ByChangeType[j].Count
		}
		return d.ByChangeType[i].Type < d.ByChangeType[j].Type
	})
}

// changeFilter is the shared per-change budget filter: an explicit change_type
// wins (asking for entity.unchanged explicitly includes heartbeats); otherwise
// heartbeats are excluded unless opted in.
type changeFilter struct {
	changeType        model.ChangeType
	includeHeartbeats bool
}

func newChangeFilter(changeType string, includeHeartbeats bool) (changeFilter, error) {
	ct, err := parseChangeType(changeType)
	if err != nil {
		return changeFilter{}, err
	}
	return changeFilter{changeType: ct, includeHeartbeats: includeHeartbeats}, nil
}

func (f changeFilter) keep(ev model.Event) (kept, heartbeat bool) {
	ct := eventChangeType(ev)
	if f.changeType != model.ChangeUnspecified {
		return ct == f.changeType, false
	}
	if ct == model.EntityUnchanged && !f.includeHeartbeats {
		return false, true
	}
	return true, false
}

func parseChangeType(s string) (model.ChangeType, error) {
	if s == "" {
		return model.ChangeUnspecified, nil
	}
	for c := model.EntityCreated; c <= model.RelationAttributeChanged; c++ {
		if c.String() == s {
			return c, nil
		}
	}
	return 0, fmt.Errorf("invalid change_type %q: use one of entity.created, entity.deleted, "+
		"entity.attribute_updated, entity.state_changed, entity.unchanged, relation.added, "+
		"relation.removed, or relation.attribute_changed", s)
}

func eventChangeType(ev model.Event) model.ChangeType {
	switch {
	case ev.Entity != nil:
		return ev.Entity.ChangeType
	case ev.Relation != nil:
		return ev.Relation.ChangeType
	default:
		return model.ChangeUnspecified
	}
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	}
	return limit
}

// --- describe_schema ---

// DescribeSchemaInput optionally pins the instant to describe.
type DescribeSchemaInput struct {
	AsOf string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: describe the graph as it was then (event-time), instead of now"`
}

// TypeCount pairs a type name with the number of instances in the graph.
type TypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// DescribeSchemaOutput summarizes the graph's contents.
type DescribeSchemaOutput struct {
	Description    string      `json:"description" jsonschema:"a natural-language summary of what this Toise instance currently knows"`
	EntityTypes    []TypeCount `json:"entity_types"`
	RelationTypes  []TypeCount `json:"relation_types"`
	TotalEntities  int         `json:"total_entities"`
	TotalRelations int         `json:"total_relations"`
}

func (s *Server) describeSchema(ctx context.Context, _ *mcpsdk.CallToolRequest, in DescribeSchemaInput) (*mcpsdk.CallToolResult, DescribeSchemaOutput, error) {
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, DescribeSchemaOutput{}, err
	}
	entTypes := sortedCounts(g.CountByType())
	relTypes := sortedCounts(relationCounts(g.ListRelations("", "", "")))
	out := DescribeSchemaOutput{
		EntityTypes:    entTypes,
		RelationTypes:  relTypes,
		TotalEntities:  g.EntityCount(),
		TotalRelations: g.RelationCount(),
	}
	out.Description = describe(out)
	return nil, out, nil
}

func relationCounts(rels []model.Relation) map[string]int {
	m := make(map[string]int)
	for _, r := range rels {
		m[r.Type]++
	}
	return m
}

func sortedCounts(m map[string]int) []TypeCount {
	out := make([]TypeCount, 0, len(m))
	for t, n := range m {
		out = append(out, TypeCount{Type: t, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func describe(o DescribeSchemaOutput) string {
	if o.TotalEntities == 0 {
		return "This Toise instance is empty: no entities have been observed yet."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This Toise instance tracks %d entities across %d types",
		o.TotalEntities, len(o.EntityTypes))
	if len(o.EntityTypes) > 0 {
		b.WriteString(" (")
		b.WriteString(joinCounts(o.EntityTypes))
		b.WriteString(")")
	}
	if o.TotalRelations > 0 {
		fmt.Fprintf(&b, ", connected by %d relations across %d types (%s)",
			o.TotalRelations, len(o.RelationTypes), joinCounts(o.RelationTypes))
	} else {
		b.WriteString(", with no relations recorded yet")
	}
	b.WriteString(".")
	return b.String()
}

func joinCounts(tcs []TypeCount) string {
	parts := make([]string, len(tcs))
	for i, tc := range tcs {
		parts[i] = fmt.Sprintf("%d %s", tc.Count, tc.Type)
	}
	return strings.Join(parts, ", ")
}

// --- shared helpers ---

func parseOptTime(s, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: use an RFC 3339 timestamp like 2026-05-29T14:00:00Z", field, s)
	}
	// The persisted time index encodes event_time as unsigned nanoseconds, so
	// a pre-epoch instant would wrap above every real key and read the whole
	// log; reject it here instead of migrating the on-disk encoding.
	if t.Before(time.Unix(0, 0)) {
		return time.Time{}, fmt.Errorf("invalid %s %q: RFC 3339 timestamps before 1970-01-01T00:00:00Z are not supported", field, s)
	}
	return t, nil
}
