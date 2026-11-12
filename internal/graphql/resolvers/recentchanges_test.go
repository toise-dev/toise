package resolvers

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/store"
)

// fakeEventReader is an in-memory EventReader that counts Resolve calls so a
// test can prove the resolver only pays for the page it returns.
type fakeEventReader struct {
	entries  []store.TimeIndexEntry // stored newest-first
	events   map[uint64]model.Event
	resolves int
}

func (f *fakeEventReader) ReadByEntity(ctx context.Context, id model.EntityID) ([]model.Event, error) {
	return nil, nil
}

func (f *fakeEventReader) ReadByTimeRange(ctx context.Context, start, end time.Time) ([]model.Event, error) {
	return nil, nil
}

func (f *fakeEventReader) ScanByTimeRange(ctx context.Context, start, end time.Time, fn func(model.Event) error) error {
	return nil
}

func (f *fakeEventReader) ScanTimeIndex(ctx context.Context, start, end time.Time, newestFirst bool, fn func(store.TimeIndexEntry) error) error {
	if newestFirst {
		for _, e := range f.entries {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	}
	for i := len(f.entries) - 1; i >= 0; i-- {
		if err := fn(f.entries[i]); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeEventReader) Resolve(seq uint64) (model.Event, bool, error) {
	f.resolves++
	ev, ok := f.events[seq]
	return ev, ok, nil
}

func (f *fakeEventReader) PruneHorizon() time.Time { return time.Time{} }

func (f *fakeEventReader) NewestEventTime() (time.Time, bool, error) {
	return time.Time{}, false, nil
}

var (
	testT0    = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	testFrom  = "2026-01-14T12:00:00Z"
	testTo    = "2026-01-15T12:00:00Z"
	testCtx   = context.Background()
	testStart = time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	testEnd   = testT0
)

func newTestResolver(f *fakeEventReader) *queryResolver {
	return &queryResolver{&Resolver{Store: f, Now: func() time.Time { return testT0 }}}
}

// buildMixedEvents returns 300 index entries newest-first mixing tagged
// heartbeats, untagged heartbeats, tagged entities, untagged entities and
// relations, with alternating scopes.
func buildMixedEvents() ([]store.TimeIndexEntry, map[uint64]model.Event) {
	var entries []store.TimeIndexEntry
	events := make(map[uint64]model.Event)
	seq := uint64(1)
	add := func(ct model.ChangeType, tagged bool, relation bool, scope string) {
		id := fmt.Sprintf("evt-%04d", seq)
		var ev model.Event
		if relation {
			ev = model.Event{Relation: &model.RelationEvent{EventID: id, ChangeType: ct, Scope: scope}}
		} else {
			ev = model.Event{Entity: &model.EntityEvent{EventID: id, ChangeType: ct, Scope: scope, Entity: model.Entity{}}}
		}
		entries = append(entries, store.TimeIndexEntry{Seq: seq, ChangeType: ct, Tagged: tagged})
		events[seq] = ev
		seq++
	}
	for i := 0; i < 300; i++ {
		scope := "a"
		if i%2 == 1 {
			scope = "b"
		}
		switch i % 5 {
		case 0:
			add(model.EntityUnchanged, true, false, scope)
		case 1:
			add(model.EntityUnchanged, false, false, scope)
		case 2:
			add(model.EntityAttributeUpdated, true, false, scope)
		case 3:
			add(model.EntityCreated, false, false, scope)
		case 4:
			add(model.RelationAttributeChanged, true, true, scope)
		}
	}
	return entries, events
}

// referenceRecentChanges is the pre-change behavior written out in the test:
// resolve everything, filter, then paginate the full slice.
func referenceRecentChanges(r *queryResolver, scope *string, match []generated.AttributeMatch, includeHeartbeats bool, first *int, after *string) (*generated.ChangeConnection, error) {
	want := make(map[string]string, len(match))
	for _, m := range match {
		want[m.Key] = m.Value
	}
	var evs []model.Event
	err := r.Store.ScanTimeIndex(testCtx, testStart, testEnd, true, func(e store.TimeIndexEntry) error {
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
		if scope != nil && *scope != "" && eventScopeOf(ev) != *scope {
			return nil
		}
		if len(want) > 0 {
			if ev.Entity == nil || !ev.Entity.Entity.MatchAll(want) {
				return nil
			}
		}
		evs = append(evs, ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.changeConnection(evs, first, after, generated.ChangeOrderNewestFirst)
}

type connSummary struct {
	cursors     []string
	total       int
	hasNext     bool
	endCursor   string
	holdsNewest bool
	holdsOldest bool
}

func summarize(c *generated.ChangeConnection) connSummary {
	s := connSummary{total: c.TotalCount, hasNext: c.PageInfo.HasNextPage, holdsNewest: c.HoldsNewest, holdsOldest: c.HoldsOldest}
	for _, e := range c.Edges {
		s.cursors = append(s.cursors, e.Cursor)
	}
	if c.PageInfo.EndCursor != nil {
		s.endCursor = *c.PageInfo.EndCursor
	}
	return s
}

func TestRecentChangesEquivalence(t *testing.T) {
	entries, events := buildMixedEvents()
	for _, first := range []int{0, 1, 3, 50, 200} {
		f1 := &fakeEventReader{entries: entries, events: events}
		got, err := newTestResolver(f1).RecentChanges(testCtx, nil, &testFrom, &testTo, nil, nil, false, &first, nil)
		if err != nil {
			t.Fatalf("first=%d: unexpected error: %v", first, err)
		}
		f2 := &fakeEventReader{entries: entries, events: events}
		want, err := referenceRecentChanges(newTestResolver(f2), nil, nil, false, &first, nil)
		if err != nil {
			t.Fatalf("first=%d: reference error: %v", first, err)
		}
		gs, ws := summarize(got), summarize(want)
		if !reflect.DeepEqual(gs, ws) {
			t.Errorf("first=%d: got %+v, want %+v", first, gs, ws)
		}
	}
	// Full pagination with `after` must walk the same edges in the same order.
	page := 7
	var gotCursors, wantCursors []string
	var after *string
	for {
		f := &fakeEventReader{entries: entries, events: events}
		conn, err := newTestResolver(f).RecentChanges(testCtx, nil, &testFrom, &testTo, nil, nil, false, &page, after)
		if err != nil {
			t.Fatalf("pagination: unexpected error: %v", err)
		}
		for _, e := range conn.Edges {
			gotCursors = append(gotCursors, e.Cursor)
		}
		if !conn.PageInfo.HasNextPage {
			break
		}
		after = conn.PageInfo.EndCursor
	}
	after = nil
	for {
		f := &fakeEventReader{entries: entries, events: events}
		conn, err := referenceRecentChanges(newTestResolver(f), nil, nil, false, &page, after)
		if err != nil {
			t.Fatalf("reference pagination: unexpected error: %v", err)
		}
		for _, e := range conn.Edges {
			wantCursors = append(wantCursors, e.Cursor)
		}
		if !conn.PageInfo.HasNextPage {
			break
		}
		after = conn.PageInfo.EndCursor
	}
	if !reflect.DeepEqual(gotCursors, wantCursors) {
		t.Errorf("paginated edges differ: got %d cursors, want %d", len(gotCursors), len(wantCursors))
	}
}

func TestRecentChangesResolvesOnlyThePage(t *testing.T) {
	var entries []store.TimeIndexEntry
	events := make(map[uint64]model.Event)
	for seq := uint64(1); seq <= 10000; seq++ {
		id := fmt.Sprintf("evt-%05d", seq)
		entries = append(entries, store.TimeIndexEntry{Seq: seq, ChangeType: model.EntityAttributeUpdated, Tagged: true})
		events[seq] = model.Event{Entity: &model.EntityEvent{EventID: id, ChangeType: model.EntityAttributeUpdated, Entity: model.Entity{}}}
	}
	f := &fakeEventReader{entries: entries, events: events}
	first := 5
	conn, err := newTestResolver(f).RecentChanges(testCtx, nil, &testFrom, &testTo, nil, nil, false, &first, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.resolves > 5 {
		t.Errorf("Resolve called %d times, want at most 5", f.resolves)
	}
	if conn.TotalCount != 10000 {
		t.Errorf("TotalCount = %d, want 10000", conn.TotalCount)
	}
	if !conn.PageInfo.HasNextPage {
		t.Errorf("HasNextPage = false, want true")
	}
}

func TestRecentChangesUnknownCursor(t *testing.T) {
	entries, events := buildMixedEvents()
	f := &fakeEventReader{entries: entries, events: events}
	after := encodeCursor("evt-9999")
	first := 10
	conn, err := newTestResolver(f).RecentChanges(testCtx, nil, &testFrom, &testTo, nil, nil, false, &first, &after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conn.Edges) != 0 {
		t.Errorf("edges = %d, want 0 for an unknown cursor", len(conn.Edges))
	}
	if conn.PageInfo.HasNextPage {
		t.Errorf("HasNextPage = true, want false for an unknown cursor")
	}
	f2 := &fakeEventReader{entries: entries, events: events}
	want, err := referenceRecentChanges(newTestResolver(f2), nil, nil, false, &first, &after)
	if err != nil {
		t.Fatalf("reference error: %v", err)
	}
	if conn.TotalCount != want.TotalCount {
		t.Errorf("TotalCount = %d, want %d (the full kept total)", conn.TotalCount, want.TotalCount)
	}
}

func TestRecentChangesOverCapFirst(t *testing.T) {
	entries, events := buildMixedEvents()
	f := &fakeEventReader{entries: entries, events: events}
	first := MaxPageSize + 1
	_, err := newTestResolver(f).RecentChanges(testCtx, nil, &testFrom, &testTo, nil, nil, false, &first, nil)
	if err == nil {
		t.Fatalf("want an error for first > MaxPageSize, got nil")
	}
	if f.resolves != 0 {
		t.Errorf("Resolve called %d times, want 0 when the page size is refused", f.resolves)
	}
}

func TestRecentChangesScopeFilter(t *testing.T) {
	var entries []store.TimeIndexEntry
	events := make(map[uint64]model.Event)
	add := func(seq uint64, scope string) {
		id := fmt.Sprintf("evt-%04d", seq)
		entries = append(entries, store.TimeIndexEntry{Seq: seq, ChangeType: model.EntityAttributeUpdated, Tagged: true})
		events[seq] = model.Event{Entity: &model.EntityEvent{EventID: id, ChangeType: model.EntityAttributeUpdated, Scope: scope, Entity: model.Entity{}}}
	}
	// Newest-first: a "b" event sits ahead of an older "a" event and must not
	// hide it.
	add(1, "b")
	add(2, "a")
	add(3, "b")
	add(4, "a")
	f := &fakeEventReader{entries: entries, events: events}
	scope := "a"
	first := 10
	conn, err := newTestResolver(f).RecentChanges(testCtx, nil, &testFrom, &testTo, &scope, nil, false, &first, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantCursors := []string{encodeCursor("evt-0002"), encodeCursor("evt-0004")}
	gotCursors := make([]string, 0, len(conn.Edges))
	for _, e := range conn.Edges {
		gotCursors = append(gotCursors, e.Cursor)
	}
	if !reflect.DeepEqual(gotCursors, wantCursors) {
		t.Errorf("cursors = %v, want %v", gotCursors, wantCursors)
	}
	if conn.TotalCount != 2 {
		t.Errorf("TotalCount = %d, want 2", conn.TotalCount)
	}
}
