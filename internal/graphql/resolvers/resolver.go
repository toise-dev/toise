// Package resolvers implements the gqlgen ResolverRoot, backing the GraphQL API
// with the in-memory projection (current state) and the event log (history).
// See ADR 0010.
package resolvers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
)

// EventReader is the subset of the store the resolvers read history from.
type EventReader interface {
	ReadByEntity(ctx context.Context, id model.EntityID) ([]model.Event, error)
	ReadByTimeRange(ctx context.Context, start, end time.Time) ([]model.Event, error)
	// PruneHorizon is the latest retention cutoff ever applied (zero = never
	// pruned): the oldest instant an as-of read can answer completely.
	PruneHorizon() time.Time
}

// Graph is the subset of the projection the resolvers read current state from.
type Graph interface {
	GetEntity(id model.EntityID) (model.Entity, bool, bool)
	ListEntities(typ string) []model.Entity
	ListRelations(typ string, from, to model.EntityID) []model.Relation
}

// Resolver wires the GraphQL API to the projection, the log, and the change
// engine (for subscriptions).
type Resolver struct {
	Graph  Graph
	Store  EventReader
	Engine *change.Engine
	Now    func() time.Time
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// graphAt resolves which graph a query reads: the live projection when asOf is
// nil/empty, otherwise a projection folded from the event log up to asOf —
// the event-time reading ("the world as it was"), mirroring the MCP tools'
// as_of (#135). An asOf older than the retention horizon is rejected: those
// events are pruned and a silent partial graph would be worse than an error.
func (r *Resolver) graphAt(ctx context.Context, asOf *string) (Graph, error) {
	if asOf == nil || *asOf == "" {
		return r.Graph, nil
	}
	t, err := parseOptTime(asOf, "asOf")
	if err != nil {
		return nil, err
	}
	if h := r.Store.PruneHorizon(); !h.IsZero() && t.Before(h) {
		return nil, fmt.Errorf("asOf %s is before the retention horizon %s: events that old have been pruned",
			t.UTC().Format(time.RFC3339), h.Format(time.RFC3339))
	}
	evs, err := r.Store.ReadByTimeRange(ctx, time.Unix(0, 0), t.Add(time.Nanosecond))
	if err != nil {
		return nil, fmt.Errorf("reading events up to %s: %w", t.UTC().Format(time.RFC3339), err)
	}
	g := projection.New()
	for i := range evs {
		g.Apply(evs[i])
	}
	return g, nil
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

func (r *queryResolver) EntityHistory(ctx context.Context, id string, since, until, asKnownAt *string, first *int, after *string) (*generated.ChangeConnection, error) {
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
	filtered := evs[:0:0]
	for _, ev := range evs {
		et, rt := eventTimes(ev)
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
		ei, _ := eventTimes(filtered[i])
		ej, _ := eventTimes(filtered[j])
		return ei.Before(ej)
	})
	return r.changeConnection(filtered, first, after)
}

func (r *queryResolver) RecentChanges(ctx context.Context, window string, first *int, after *string) (*generated.ChangeConnection, error) {
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("invalid window %q: use a positive Go duration like 15m, 2h, or 24h", window)
	}
	now := r.now()
	evs, err := r.Store.ReadByTimeRange(ctx, now.Add(-d), now.Add(time.Nanosecond)) // inclusive of now
	if err != nil {
		return nil, err
	}
	// newest-first
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
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

func (r *subscriptionResolver) EntityChanged(ctx context.Context) (<-chan *generated.ChangeEvent, error) {
	return r.stream(ctx, func(ev model.Event) bool { return ev.Entity != nil }), nil
}

func (r *subscriptionResolver) RelationChanged(ctx context.Context) (<-chan *generated.ChangeEvent, error) {
	return r.stream(ctx, func(ev model.Event) bool { return ev.Relation != nil }), nil
}

func (r *subscriptionResolver) stream(ctx context.Context, want func(model.Event) bool) <-chan *generated.ChangeEvent {
	ch := make(chan *generated.ChangeEvent, 16)
	unsub := r.Engine.Subscribe(func(ev model.Event, _ bool) {
		if !want(ev) {
			return
		}
		select {
		case ch <- eventToChangeGQL(ev):
		default: // drop if the client cannot keep up
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
	return t, nil
}

func eventTimes(ev model.Event) (eventTime, recordedAt time.Time) {
	switch {
	case ev.Entity != nil:
		return ev.Entity.EventTime, ev.Entity.RecordedAt
	case ev.Relation != nil:
		return ev.Relation.EventTime, ev.Relation.RecordedAt
	default:
		return time.Time{}, time.Time{}
	}
}
