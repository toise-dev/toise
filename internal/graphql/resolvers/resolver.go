// Package resolvers implements the gqlgen ResolverRoot, backing the GraphQL API
// with the in-memory projection (current state) and the event log (history).
// See ADR 0010.
package resolvers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/toise-dev/toise/internal/annotations"
	"github.com/toise-dev/toise/internal/audit"
	"github.com/toise-dev/toise/internal/canonical"
	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

// EventReader is the subset of the store the resolvers read history from.
type EventReader interface {
	ReadByEntity(ctx context.Context, id model.EntityID) ([]model.Event, error)
	ReadByTimeRange(ctx context.Context, start, end time.Time) ([]model.Event, error)
	// ScanByTimeRange streams the range to fn (no intermediate slice), backing
	// the as-of fold (projection.At).
	ScanByTimeRange(ctx context.Context, start, end time.Time, fn func(model.Event) error) error
	// ScanTimeIndex walks the window's index entries without resolving primary
	// records, so a filter can skip an event without paying its read and decode;
	// Resolve fetches the ones a caller keeps (#351).
	ScanTimeIndex(ctx context.Context, start, end time.Time, newestFirst bool, fn func(store.TimeIndexEntry) error) error
	Resolve(seq uint64) (model.Event, bool, error)
	// PruneHorizon is the latest retention cutoff ever applied (zero = never
	// pruned): the oldest instant an as-of read can answer completely.
	PruneHorizon() time.Time
}

// Graph is the subset of the projection the resolvers read current state from.
type Graph interface {
	GetEntity(id model.EntityID) (model.Entity, bool, bool)
	ListEntities(typ string) []model.Entity
	ListRelations(typ string, from, to model.EntityID) []model.Relation
	RelationsTouching(id model.EntityID, relType string) []model.Relation
}

// Resolver wires the GraphQL API to the projection, the log, and the change
// engine (for subscriptions).
type Resolver struct {
	Graph       Graph
	Store       EventReader
	Engine      *change.Engine
	Annotations *annotations.Store
	Audit       *audit.Auditor // nil/disabled = no audit records (ADR 0028)
	Now         func() time.Time
	// IdentityThreshold is the same_as confidence at or above which an alias
	// joins the canonical view (ADR 0020). Zero means canonical.DefaultThreshold,
	// so a Resolver built without it still answers like the MCP surface rather
	// than collapsing on every belief.
	IdentityThreshold float64
}

func (r *Resolver) identityThreshold() float64 {
	if r.IdentityThreshold > 0 {
		return r.IdentityThreshold
	}
	return canonical.DefaultThreshold
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// graphAt resolves which graph a query reads: the live projection when asOf is
// nil/empty, otherwise the projection folded from the event log up to asOf via
// the shared as-of service (projection.At) — the same streaming, horizon-checked,
// concurrency-bounded fold the MCP as_of path uses (#135, #166). The reading is
// event-time ("the world as it was at T").
func (r *Resolver) graphAt(ctx context.Context, asOf *string) (Graph, error) {
	if asOf == nil || *asOf == "" {
		return r.Graph, nil
	}
	t, err := parseOptTime(asOf, "asOf")
	if err != nil {
		return nil, err
	}
	return projection.At(ctx, r.Store, t)
}

// Query returns the query resolver.
func (r *Resolver) Query() generated.QueryResolver { return &queryResolver{r} }

// Subscription returns the subscription resolver.
func (r *Resolver) Subscription() generated.SubscriptionResolver { return &subscriptionResolver{r} }

type queryResolver struct{ *Resolver }

func (r *queryResolver) Entity(ctx context.Context, id string, asOf *string) (*generated.Entity, error) {
	g, err := r.graphAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	e, ok, deleted := g.GetEntity(model.EntityID(id))
	if !ok {
		return nil, nil
	}
	return entityToGQL(e, deleted), nil
}

func (r *queryResolver) Entities(ctx context.Context, filter *generated.EntityFilter, first *int, after, asOf *string) (*generated.EntityConnection, error) {
	g, err := r.graphAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	typ := ""
	if filter != nil && filter.Type != nil {
		typ = *filter.Type
	}
	all := g.ListEntities(typ)
	if filter != nil && len(filter.Match) > 0 {
		want := make(map[string]string, len(filter.Match))
		for _, m := range filter.Match {
			want[m.Key] = m.Value
		}
		kept := make([]model.Entity, 0, len(all))
		for _, e := range all {
			if e.MatchAll(want) {
				kept = append(kept, e)
			}
		}
		all = kept
	}
	page, end, hasNext, err := paginate(all, func(e model.Entity) string { return string(e.ID) }, first, after)
	if err != nil {
		return nil, err
	}
	edges := make([]generated.EntityEdge, len(page))
	for i, e := range page {
		edges[i] = generated.EntityEdge{Cursor: encodeCursor(string(e.ID)), Node: entityToGQL(e, false)}
	}
	return &generated.EntityConnection{
		Edges:      edges,
		PageInfo:   &generated.PageInfo{HasNextPage: hasNext, EndCursor: end},
		TotalCount: len(all),
	}, nil
}

func (r *queryResolver) Relations(ctx context.Context, filter *generated.RelationFilter, first *int, after, asOf *string) (*generated.RelationConnection, error) {
	g, err := r.graphAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	var typ string
	var from, to model.EntityID
	if filter != nil {
		if filter.Type != nil {
			typ = *filter.Type
		}
		if filter.FromID != nil {
			from = model.EntityID(*filter.FromID)
		}
		if filter.ToID != nil {
			to = model.EntityID(*filter.ToID)
		}
	}
	all := g.ListRelations(typ, from, to)
	page, end, hasNext, err := paginate(all, func(rel model.Relation) string { return string(rel.ID) }, first, after)
	if err != nil {
		return nil, err
	}
	edges := make([]generated.RelationEdge, len(page))
	for i, rel := range page {
		edges[i] = generated.RelationEdge{Cursor: encodeCursor(string(rel.ID)), Node: relationToGQL(rel)}
	}
	return &generated.RelationConnection{
		Edges:      edges,
		PageInfo:   &generated.PageInfo{HasNextPage: hasNext, EndCursor: end},
		TotalCount: len(all),
	}, nil
}

// dropHeartbeats filters entity.unchanged events out of a timeline, matching
// the MCP default: heartbeats dominate a live window, and both surfaces must
// give the same answer to the same question.
func dropHeartbeats(evs []model.Event) []model.Event {
	kept := evs[:0:0]
	for _, ev := range evs {
		if ev.Entity != nil && ev.Entity.ChangeType == model.EntityUnchanged {
			continue
		}
		kept = append(kept, ev)
	}
	return kept
}

func (r *queryResolver) EntityHistory(ctx context.Context, id string, since, until, asKnownAt *string, includeHeartbeats bool, first *int, after *string) (*generated.ChangeConnection, error) {
	evs, err := r.Store.ReadByEntity(ctx, model.EntityID(id))
	if err != nil {
		return nil, err
	}
	sinceT, err := parseOptTime(since, "since")
	if err != nil {
		return nil, err
	}
	untilT, err := parseOptTime(until, "until")
	if err != nil {
		return nil, err
	}
	knownT, err := parseOptTime(asKnownAt, "asKnownAt")
	if err != nil {
		return nil, err
	}
	if !includeHeartbeats {
		evs = dropHeartbeats(evs)
	}
	filtered := evs[:0:0]
	for _, ev := range evs {
		et, rt := ev.Times()
		if !sinceT.IsZero() && et.Before(sinceT) {
			continue
		}
		if !untilT.IsZero() && et.After(untilT) {
			continue
		}
		if !knownT.IsZero() && rt.After(knownT) { // audit view: only what we knew by then
			continue
		}
		filtered = append(filtered, ev)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		ei, _ := filtered[i].Times()
		ej, _ := filtered[j].Times()
		return ei.Before(ej)
	})
	return r.changeConnection(filtered, first, after)
}

// defaultRecentChangesWindow matches the MCP recent_changes default, so the same
// question asked over either surface takes the same shape and the same default.
const defaultRecentChangesWindow = "1h"

func (r *queryResolver) RecentChanges(ctx context.Context, window *string, includeHeartbeats bool, first *int, after *string) (*generated.ChangeConnection, error) {
	w := defaultRecentChangesWindow
	if window != nil && *window != "" {
		w = *window
	}
	d, err := time.ParseDuration(w)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("invalid window %q: use a positive Go duration like 15m, 2h, or 24h", w)
	}
	now := r.now()
	// Walk the time index newest-first and drop heartbeats from the tag alone:
	// a window is heartbeat-dominated, and excluding an event must not cost a
	// point lookup and a decode of it (#351). Only kept events are resolved —
	// pre-tagging entries fall back to resolving, and age out with retention.
	var evs []model.Event
	err = r.Store.ScanTimeIndex(ctx, now.Add(-d), now.Add(time.Nanosecond), true, func(e store.TimeIndexEntry) error {
		if !includeHeartbeats && e.Tagged && e.ChangeType == model.EntityUnchanged {
			return nil
		}
		ev, ok, rerr := r.Store.Resolve(e.Seq)
		if rerr != nil || !ok {
			return rerr
		}
		if !includeHeartbeats && !e.Tagged && ev.Entity != nil && ev.Entity.ChangeType == model.EntityUnchanged {
			return nil
		}
		evs = append(evs, ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.changeConnection(evs, first, after)
}

func (r *queryResolver) changeConnection(evs []model.Event, first *int, after *string) (*generated.ChangeConnection, error) {
	page, end, hasNext, err := paginate(evs, eventID, first, after)
	if err != nil {
		return nil, err
	}
	edges := make([]generated.ChangeEdge, len(page))
	for i, ev := range page {
		edges[i] = generated.ChangeEdge{Cursor: encodeCursor(eventID(ev)), Node: eventToChangeGQL(ev)}
	}
	return &generated.ChangeConnection{
		Edges:      edges,
		PageInfo:   &generated.PageInfo{HasNextPage: hasNext, EndCursor: end},
		TotalCount: len(evs),
	}, nil
}

type subscriptionResolver struct{ *Resolver }

func (r *subscriptionResolver) EntityChanged(ctx context.Context, filter *generated.ChangeFilter) (<-chan *generated.ChangeEvent, error) {
	return r.stream(ctx, func(ev model.Event) bool {
		if ev.Entity == nil {
			return false
		}
		if filter == nil {
			return true
		}
		if filter.EntityType != nil && ev.Entity.Entity.Type != *filter.EntityType {
			return false
		}
		if filter.ChangeType != nil && changeTypeGQL[ev.Entity.ChangeType] != *filter.ChangeType {
			return false
		}
		return true
	}), nil
}

func (r *subscriptionResolver) RelationChanged(ctx context.Context, filter *generated.ChangeFilter) (<-chan *generated.ChangeEvent, error) {
	return r.stream(ctx, func(ev model.Event) bool {
		if ev.Relation == nil {
			return false
		}
		if filter == nil {
			return true
		}
		if filter.RelationType != nil && ev.Relation.Relation.Type != *filter.RelationType {
			return false
		}
		if filter.ChangeType != nil && changeTypeGQL[ev.Relation.ChangeType] != *filter.ChangeType {
			return false
		}
		if filter.StructuralOnly != nil && *filter.StructuralOnly && !ev.Relation.Relation.Structural {
			return false
		}
		return true
	}), nil
}

// stream fans engine events matching want into a bounded channel. A consumer
// that cannot keep up loses events — but never silently (#138): the count of
// drops is carried on the NEXT delivered event's dropped field, so the client
// knows it has a gap and can re-query state before resuming. The engine
// serializes subscriber callbacks (they run on the commit path), so the
// counter needs no lock.
func (r *subscriptionResolver) stream(ctx context.Context, want func(model.Event) bool) <-chan *generated.ChangeEvent {
	ch := make(chan *generated.ChangeEvent, 16)
	dropped := 0
	unsub := r.Engine.Subscribe(func(ev model.Event, _ bool) {
		if !want(ev) {
			return
		}
		out := eventToChangeGQL(ev)
		out.Dropped = dropped
		select {
		case ch <- out:
			dropped = 0
		default:
			dropped++ // this event is lost; the next delivered one says so
		}
	})
	go func() {
		<-ctx.Done()
		unsub()
		close(ch)
	}()
	return ch
}

func parseOptTime(s *string, field string) (time.Time, error) {
	if s == nil || *s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: use an RFC 3339 timestamp like 2026-05-29T14:00:00Z", field, *s)
	}
	// The persisted time index encodes event_time as unsigned nanoseconds, so
	// a pre-epoch instant would wrap above every real key and read the whole
	// log; reject it here instead of migrating the on-disk encoding.
	if t.Before(time.Unix(0, 0)) {
		return time.Time{}, fmt.Errorf("invalid %s %q: RFC 3339 timestamps before 1970-01-01T00:00:00Z are not supported", field, *s)
	}
	return t, nil
}
