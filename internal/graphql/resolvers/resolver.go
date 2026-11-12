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
	// NewestEventTime is when a producer last spoke — freshness measured from the
	// journal, not from when a projection was rebuilt. The distinction matters:
	// a restarted replica has a brand-new projection over an old graph, and
	// reporting the rebuild would claim freshness it does not have.
	NewestEventTime() (time.Time, bool, error)
}

// Graph is the subset of the projection the resolvers read current state from.
type Graph interface {
	GetEntity(id model.EntityID) (model.Entity, bool, bool)
	// ResolveHandle turns a client-supplied handle — an identity fingerprint, an
	// identity written inline as type:key=value, or a logical id — into a
	// logical id (ADR 0035).
	ResolveHandle(handle string) (model.EntityID, bool)
	ListEntities(typ string) []model.Entity
	ListRelations(typ string, from, to model.EntityID) []model.Relation
	RelationsTouching(id model.EntityID, relType string) []model.Relation
	// The paging methods materialize only the requested page, whereas the List*
	// methods copy and sort the entire type on every call.
	PageEntitiesByFingerprint(typ, after string, limit int) (page []model.Entity, next string, total int)
	PageRelations(typ string, from, to model.EntityID, after string, limit int) (page []model.Relation, next string, total int)
	EntityCount() int
	RelationCount() int
}

// Resolver wires the GraphQL API to the projection, the log, and the change
// engine (for subscriptions).
type Resolver struct {
	// Tenant names the tenant this resolver answers for, surfaced by the
	// graphScope query (#399). Empty on a single-tenant instance.
	Tenant string
	// TenantName is the operator's label for that tenant (#361). Decoration:
	// compare Tenant, render TenantName. Nothing resolves a name to an id.
	TenantName  string
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
	eid, ok := g.ResolveHandle(id)
	if !ok {
		return nil, nil
	}
	e, ok, deleted := g.GetEntity(eid)
	if !ok {
		return nil, nil
	}
	return entityToGQL(e, deleted), nil
}

// A cursor from an older version still base64-decodes cleanly and, starting
// with a digit, sorts before every identity fingerprint, so accepting it
// would silently restart the enumeration from the first page.
func decodeEntityCursor(after *string) (string, error) {
	if after == nil || *after == "" {
		return "", nil
	}
	decoded, err := decodeCursor(*after)
	if err != nil {
		return "", fmt.Errorf("invalid after cursor: %w", err)
	}
	sep := -1
	for i := 0; i < len(decoded); i++ {
		if decoded[i] == ':' {
			sep = i
			break
		}
	}
	if sep <= 0 || sep == len(decoded)-1 {
		return "", fmt.Errorf("this cursor is not from this version: entity pagination now uses identity fingerprints, please restart the enumeration without the after parameter")
	}
	for i := sep + 1; i < len(decoded); i++ {
		c := decoded[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("this cursor is not from this version: entity pagination now uses identity fingerprints, please restart the enumeration without the after parameter")
		}
	}
	return decoded, nil
}

func pageSize(first *int) (int, error) {
	n := 50
	if first != nil {
		n = *first
	}
	if n < 0 {
		n = 0
	}
	if n > MaxPageSize {
		return 0, fmt.Errorf("first: %d exceeds the maximum page size of %d; ask for at most %d and page with `after`, or narrow the query — serving fewer than requested without saying so is how a partial answer gets read as a complete one", n, MaxPageSize, MaxPageSize)
	}
	return n, nil
}

func (r *queryResolver) Entities(ctx context.Context, filter *generated.EntityFilter, first *int, after, asOf *string) (*generated.EntityConnection, error) {
	g, err := r.graphAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	n, err := pageSize(first)
	if err != nil {
		return nil, err
	}
	typ := ""
	if filter != nil && filter.Type != nil {
		typ = *filter.Type
	}
	if filter == nil || len(filter.Match) == 0 {
		var afterFP string
		afterFP, err = decodeEntityCursor(after)
		if err != nil {
			return nil, err
		}
		page, next, total := g.PageEntitiesByFingerprint(typ, afterFP, n)
		edges := make([]generated.EntityEdge, len(page))
		for i, e := range page {
			edges[i] = generated.EntityEdge{Cursor: encodeCursor(e.IdentityHash()), Node: entityToGQL(e, false)}
		}
		// endCursor names the last rendered edge, not the existence of a next
		// page; clearing it on the last page would make a consumer that
		// persists it lose its position.
		// A zero-item page has no cursor, so claiming a next page there would
		// loop forever; the real set size is carried by totalCount in this
		// response.
		pageInfo := &generated.PageInfo{HasNextPage: next != ""}
		if len(edges) > 0 {
			end := edges[len(edges)-1].Cursor
			pageInfo.EndCursor = &end
		}
		return &generated.EntityConnection{
			Edges:      edges,
			PageInfo:   pageInfo,
			TotalCount: total,
		}, nil
	}
	all := g.ListEntities(typ)
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
	sort.Slice(kept, func(i, j int) bool { return kept[i].IdentityHash() < kept[j].IdentityHash() })
	page, end, hasNext, err := paginate(kept, func(e model.Entity) string { return e.IdentityHash() }, first, after)
	if err != nil {
		return nil, err
	}
	edges := make([]generated.EntityEdge, len(page))
	for i, e := range page {
		edges[i] = generated.EntityEdge{Cursor: encodeCursor(e.IdentityHash()), Node: entityToGQL(e, false)}
	}
	return &generated.EntityConnection{
		Edges:      edges,
		PageInfo:   &generated.PageInfo{HasNextPage: hasNext, EndCursor: end},
		TotalCount: len(kept),
	}, nil
}

func (r *queryResolver) Relations(ctx context.Context, filter *generated.RelationFilter, first *int, after, asOf *string) (*generated.RelationConnection, error) {
	g, err := r.graphAt(ctx, asOf)
	if err != nil {
		return nil, err
	}
	n, err := pageSize(first)
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
			// Accept the same three handle forms the rest of the schema does
			// (ADR 0035). Before this, a fingerprint here matched nothing and
			// answered totalCount: 0 — a silent empty result where every other
			// surface resolves it, which is the exact failure this project
			// spent a release removing.
			id, ok := g.ResolveHandle(*filter.FromID)
			if !ok {
				return &generated.RelationConnection{Edges: []generated.RelationEdge{}, PageInfo: &generated.PageInfo{}}, nil
			}
			from = id
		}
		if filter.ToID != nil {
			id, ok := g.ResolveHandle(*filter.ToID)
			if !ok {
				return &generated.RelationConnection{Edges: []generated.RelationEdge{}, PageInfo: &generated.PageInfo{}}, nil
			}
			to = id
		}
	}
	afterKey := ""
	if after != nil && *after != "" {
		afterKey, err = decodeCursor(*after)
		if err != nil {
			return nil, fmt.Errorf("invalid after cursor: %w", err)
		}
	}
	page, next, total := g.PageRelations(typ, from, to, afterKey, n)
	edges := make([]generated.RelationEdge, len(page))
	for i, rel := range page {
		edges[i] = generated.RelationEdge{Cursor: encodeCursor(string(rel.ID)), Node: relationToGQL(rel)}
	}
	pageInfo := &generated.PageInfo{HasNextPage: next != ""}
	if len(edges) > 0 {
		end := edges[len(edges)-1].Cursor
		pageInfo.EndCursor = &end
	}
	return &generated.RelationConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: total,
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
	eid, ok := r.Graph.ResolveHandle(id)
	if !ok {
		return &generated.ChangeConnection{}, nil
	}
	evs, err := r.Store.ReadByEntity(ctx, eid)
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
	return r.changeConnection(filtered, first, after, generated.ChangeOrderOldestFirst)
}

// defaultRecentChangesWindow matches the MCP recent_changes default, so the same
// question asked over either surface takes the same shape and the same default.
const defaultRecentChangesWindow = "1h"

// changeWindow resolves the two ways of naming a window into instants: a
// duration looking back from now, or an explicit from/to pair. The pair is what
// lets a past window be investigated at all — a wide duration plus a page size
// keeps only the NEWEST changes, so an event hours back is absent from an answer
// that claims to cover it, and its absence reads as "nothing happened".
func (r *queryResolver) changeWindow(window, from, to *string) (start, end time.Time, err error) {
	has := func(p *string) bool { return p != nil && *p != "" }
	if has(from) {
		if has(window) {
			return time.Time{}, time.Time{}, fmt.Errorf("give either window or from/to, not both")
		}
		start, perr := time.Parse(time.RFC3339, *from)
		if perr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid from %q: use an RFC 3339 instant like 2026-09-20T08:00:00Z", *from)
		}
		endAt := r.now()
		if has(to) {
			endAt, perr = time.Parse(time.RFC3339, *to)
			if perr != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("invalid to %q: use an RFC 3339 instant like 2026-09-20T09:30:00Z", *to)
			}
		}
		if !endAt.After(start) {
			return time.Time{}, time.Time{}, fmt.Errorf("to (%s) must be after from (%s)", endAt.Format(time.RFC3339), start.Format(time.RFC3339))
		}
		return start, endAt.Add(time.Nanosecond), nil
	}
	if has(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("to requires from: give both instants, or a window duration instead")
	}
	w := defaultRecentChangesWindow
	if has(window) {
		w = *window
	}
	d, derr := time.ParseDuration(w)
	if derr != nil || d <= 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid window %q: use a positive Go duration like 15m, 2h, or 24h", w)
	}
	now := r.now()
	return now.Add(-d), now.Add(time.Nanosecond), nil
}

func (r *queryResolver) RecentChanges(ctx context.Context, window, from, to, scope *string, match []generated.AttributeMatch, includeHeartbeats bool, first *int, after *string) (*generated.ChangeConnection, error) {
	start, end, err := r.changeWindow(window, from, to)
	if err != nil {
		return nil, err
	}
	// The total is counted from the time index and only the page is resolved:
	// the old shape paid one Resolve per kept event across the whole window —
	// measured 392,871 resolutions for first:1 over 24 h on one tenant, which
	// is what pushed the query past the HTTP deadline.
	n, err := pageSize(first)
	if err != nil {
		return nil, err
	}
	var afterID string
	cursorSeen := after == nil || *after == ""
	if !cursorSeen {
		afterID, err = decodeCursor(*after)
		if err != nil {
			return nil, err
		}
	}
	want := make(map[string]string, len(match))
	for _, m := range match {
		want[m.Key] = m.Value
	}
	var page []model.Event
	total := 0
	hasNext := false
	// Walk the time index newest-first and drop heartbeats from the tag alone:
	// a window is heartbeat-dominated, and excluding an event must not cost a
	// point lookup and a decode of it (#351). Only kept events are resolved —
	// pre-tagging entries fall back to resolving, and age out with retention.
	err = r.Store.ScanTimeIndex(ctx, start, end, true, func(e store.TimeIndexEntry) error {
		var ev model.Event
		resolved := false
		if !e.Tagged {
			rev, ok, rerr := r.Store.Resolve(e.Seq)
			if rerr != nil || !ok {
				return rerr
			}
			ev, resolved = rev, true
			if !includeHeartbeats && ev.Entity != nil && ev.Entity.ChangeType == model.EntityUnchanged {
				return nil
			}
		} else if !includeHeartbeats && e.ChangeType == model.EntityUnchanged {
			return nil
		}
		// The scope is not in the time index, so it is tested on the resolved
		// record — before the page bound, never on the bounded page: filtering
		// afterwards would keep the newest changes of any scope and discard most
		// of them, hiding older changes from the requested scope behind newer ones
		// from another (#394).
		if scope != nil && *scope != "" {
			if !resolved {
				rev, ok, rerr := r.Store.Resolve(e.Seq)
				if rerr != nil || !ok {
					return rerr
				}
				ev, resolved = rev, true
			}
			if eventScopeOf(ev) != *scope {
				return nil
			}
		}
		// The attribute is tested against the entity AS THE EVENT SAW IT, never
		// against the live projection: an entity deleted during the window is
		// gone from the projection, and its changes are the ones worth looking
		// at. A relation carries no entity and so cannot answer an entity
		// question — excluded rather than guessed at (#380).
		if len(want) > 0 {
			if !resolved {
				rev, ok, rerr := r.Store.Resolve(e.Seq)
				if rerr != nil || !ok {
					return rerr
				}
				ev, resolved = rev, true
			}
			if ev.Entity == nil || !ev.Entity.Entity.MatchAll(want) {
				return nil
			}
		}
		// The entry is kept: it counts toward the total whether or not it lands
		// in the page.
		total++
		if !cursorSeen {
			if !resolved {
				rev, ok, rerr := r.Store.Resolve(e.Seq)
				if rerr != nil || !ok {
					return rerr
				}
				ev = rev
			}
			if eventID(ev) == afterID {
				cursorSeen = true
			}
			return nil
		}
		if len(page) < n {
			if !resolved {
				rev, ok, rerr := r.Store.Resolve(e.Seq)
				if rerr != nil || !ok {
					return rerr
				}
				ev = rev
			}
			page = append(page, ev)
			return nil
		}
		// Past the page bound nothing is resolved anymore: the entry only
		// proves that more kept events exist.
		hasNext = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return buildChangeConnection(page, total, hasNext, generated.ChangeOrderNewestFirst), nil
}

// buildChangeConnection assembles the connection from an already-bounded page:
// edges carry the cursor of their node, the end cursor is the last edge's
// cursor (nil on an empty page), and the far end of the matching set is
// reachable only when the page runs to the end of it. An empty match holds
// both ends trivially: there is nothing absent to warn about, and claiming
// otherwise would read as a truncation.
func buildChangeConnection(page []model.Event, total int, hasNext bool, order generated.ChangeOrder) *generated.ChangeConnection {
	edges := make([]generated.ChangeEdge, len(page))
	for i, ev := range page {
		edges[i] = generated.ChangeEdge{Cursor: encodeCursor(eventID(ev)), Node: eventToChangeGQL(ev)}
	}
	var end *string
	if len(page) > 0 {
		c := encodeCursor(eventID(page[len(page)-1]))
		end = &c
	}
	reachesFarEnd := !hasNext
	holdsNewest, holdsOldest := true, true
	if total > 0 {
		switch order {
		case generated.ChangeOrderOldestFirst:
			holdsNewest = reachesFarEnd
		case generated.ChangeOrderNewestFirst:
			holdsOldest = reachesFarEnd
		}
	}
	return &generated.ChangeConnection{
		Edges:       edges,
		PageInfo:    &generated.PageInfo{HasNextPage: hasNext, EndCursor: end},
		TotalCount:  total,
		Order:       order,
		HoldsNewest: holdsNewest,
		HoldsOldest: holdsOldest,
	}
}

func (r *queryResolver) changeConnection(evs []model.Event, first *int, after *string, order generated.ChangeOrder) (*generated.ChangeConnection, error) {
	page, _, hasNext, err := paginate(evs, eventID, first, after)
	if err != nil {
		return nil, err
	}
	return buildChangeConnection(page, len(evs), hasNext, order), nil
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

// eventScopeOf is the instrumentation scope that observed an event, whichever
// kind it is.
func eventScopeOf(ev model.Event) string {
	switch {
	case ev.Entity != nil:
		return ev.Entity.Scope
	case ev.Relation != nil:
		return ev.Relation.Scope
	}
	return ""
}
