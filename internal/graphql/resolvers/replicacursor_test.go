package resolvers

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/graphql/generated"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

// newReplicaResolver builds a queryResolver wired to the given projection graph,
// fake store and fixed clock.
func newReplicaResolver(g *projection.Graph, f *fakeEventReader, now time.Time) *queryResolver {
	return &queryResolver{&Resolver{Graph: g, Store: f, Now: func() time.Time { return now }}}
}

// hexJournal returns a 32-char hex journal id built from a repeated byte.
func hexJournal(b byte) string {
	raw := make([]byte, 16)
	for i := range raw {
		raw[i] = b
	}
	return hex.EncodeToString(raw)
}

func TestJournalIDStableAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := store.Open(dir, store.DefaultConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id1 := s1.JournalID()
	if len(id1) != 32 {
		t.Fatalf("journal id length = %d, want 32", len(id1))
	}
	if _, err = hex.DecodeString(id1); err != nil {
		t.Fatalf("journal id not hex: %v", err)
	}
	if err = s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := store.Open(dir, store.DefaultConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if s2.JournalID() != id1 {
		t.Fatalf("journal id changed after reopen: %q vs %q", s2.JournalID(), id1)
	}

	other, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open other: %v", err)
	}
	defer other.Close()
	if other.JournalID() == id1 {
		t.Fatalf("two distinct stores share journal id %q", id1)
	}
}

func TestDecodeLocalCursor(t *testing.T) {
	journal := hexJournal(0xaa)
	other := hexJournal(0xbb)

	id, err := decodeLocalCursor(encodeLocalCursor(journal, "evt-1"), journal)
	if err != nil || id != "evt-1" {
		t.Fatalf("same journal: id=%q err=%v", id, err)
	}
	if _, err = decodeLocalCursor(encodeLocalCursor(other, "evt-1"), journal); err == nil || !strings.Contains(err.Error(), "another replica") {
		t.Fatalf("foreign journal: err=%v, want 'another replica'", err)
	}
	// Legacy cursor without the journal mark is returned as-is.
	id, err = decodeLocalCursor(encodeCursor("evt-9"), journal)
	if err != nil || id != "evt-9" {
		t.Fatalf("legacy cursor: id=%q err=%v", id, err)
	}
	if _, err := decodeLocalCursor(encodeCursor("j1:tooshort:x"), journal); err == nil || !strings.Contains(err.Error(), "malformed cursor") {
		t.Fatalf("malformed mark: err=%v, want 'malformed cursor'", err)
	}
}

func TestRecentChangesCursorIsJournalScoped(t *testing.T) {
	now := time.Now()
	entries, events := buildMixedEvents()
	journalA := hexJournal(0x01)
	journalB := hexJournal(0x02)
	fakeA := &fakeEventReader{journal: journalA, entries: entries, events: events}
	fakeB := &fakeEventReader{journal: journalB, entries: entries, events: events}

	window := "1h"
	first := 5
	page1, err := newReplicaResolver(projection.New(), fakeA, now).RecentChanges(context.Background(), &window, nil, nil, nil, nil, false, &first, nil)
	if err != nil {
		t.Fatalf("page1 on A: %v", err)
	}
	if page1 == nil || page1.PageInfo == nil || page1.PageInfo.EndCursor == nil {
		t.Fatalf("page1 missing end cursor")
	}
	cursor := *page1.PageInfo.EndCursor

	// Replaying A's cursor on B must fail with "another replica".
	if _, err = newReplicaResolver(projection.New(), fakeB, now).RecentChanges(context.Background(), &window, nil, nil, nil, nil, false, &first, &cursor); err == nil || !strings.Contains(err.Error(), "another replica") {
		t.Fatalf("cursor from A on B: err=%v, want 'another replica'", err)
	}

	// Replaying on A must succeed and must not repeat page 1 event ids.
	page2, err := newReplicaResolver(projection.New(), fakeA, now).RecentChanges(context.Background(), &window, nil, nil, nil, nil, false, &first, &cursor)
	if err != nil {
		t.Fatalf("page2 on A: %v", err)
	}
	if len(page2.Edges) == 0 {
		t.Fatalf("page2 on A is empty: the no-repeat check below would prove nothing")
	}
	seen := map[string]bool{}
	for _, e := range page1.Edges {
		seen[changeEventID(e)] = true
	}
	for _, e := range page2.Edges {
		if seen[changeEventID(e)] {
			t.Fatalf("page2 repeats event %q from page1", changeEventID(e))
		}
	}
}

// changeEventID extracts the event id from a change edge regardless of kind.
func changeEventID(e generated.ChangeEdge) string { return e.Node.ID }

func TestRelationsCursorIsJournalScoped(t *testing.T) {
	now := time.Now()
	journalA := hexJournal(0x11)
	journalB := hexJournal(0x22)

	gA := projection.New()
	gB := projection.New()
	mkRel := func(g *projection.Graph, typ string, from, to model.EntityID) model.RelationID {
		id := model.ComputeRelationID(typ, from, to)
		g.Apply(model.Event{Relation: &model.RelationEvent{
			EventID:    "rel-" + string(id),
			ChangeType: model.RelationAdded,
			Relation:   model.Relation{ID: id, Type: typ, From: from, To: to},
		}})
		return id
	}
	mkRel(gA, "knows", "alice", "bob")
	mkRel(gA, "knows", "bob", "carol")
	mkRel(gA, "likes", "carol", "alice")
	mkRel(gB, "knows", "x", "y")
	mkRel(gB, "knows", "y", "z")
	mkRel(gB, "likes", "z", "x")

	first := 1
	page1, err := newReplicaResolver(gA, &fakeEventReader{journal: journalA}, now).Relations(context.Background(), nil, &first, nil, nil)
	if err != nil {
		t.Fatalf("page1 on A: %v", err)
	}
	cursor := *page1.PageInfo.EndCursor

	if _, err := newReplicaResolver(gB, &fakeEventReader{journal: journalB}, now).Relations(context.Background(), nil, &first, &cursor, nil); err == nil || !strings.Contains(err.Error(), "another replica") {
		t.Fatalf("cursor from A on B: err=%v, want 'another replica'", err)
	}

	// A legacy cursor (no journal mark) still works on any journal.
	legacy := encodeCursor("some-relation-id")
	if _, err := newReplicaResolver(gB, &fakeEventReader{journal: journalB}, now).Relations(context.Background(), nil, &first, &legacy, nil); err != nil {
		t.Fatalf("legacy cursor on B: %v", err)
	}
}

func TestRelationsCursorSurvivesRemovalBetweenPages(t *testing.T) {
	now := time.Now()
	journal := hexJournal(0x33)
	g := projection.New()
	id1 := model.ComputeRelationID("knows", "a", "b")
	id2 := model.ComputeRelationID("knows", "b", "c")
	id3 := model.ComputeRelationID("likes", "c", "a")
	for _, r := range []model.Relation{
		{ID: id1, Type: "knows", From: "a", To: "b"},
		{ID: id2, Type: "knows", From: "b", To: "c"},
		{ID: id3, Type: "likes", From: "c", To: "a"},
	} {
		g.Apply(model.Event{Relation: &model.RelationEvent{
			EventID: "add-" + string(r.ID), ChangeType: model.RelationAdded, Relation: r,
		}})
	}

	first := 1
	r := newReplicaResolver(g, &fakeEventReader{journal: journal}, now)
	page1, err := r.Relations(context.Background(), nil, &first, nil, nil)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Edges) != 1 {
		t.Fatalf("page1 edges = %d, want 1", len(page1.Edges))
	}
	firstID := page1.Edges[0].Node.ID
	cursor := *page1.PageInfo.EndCursor

	// Remove the relation returned on page 1, then fetch page 2.
	g.Apply(model.Event{Relation: &model.RelationEvent{
		EventID: "rm-" + firstID, ChangeType: model.RelationRemoved,
		Relation: model.Relation{ID: model.RelationID(firstID), Type: "knows", From: "a", To: "b"},
	}})

	page2, err := r.Relations(context.Background(), nil, &first, &cursor, nil)
	if err != nil {
		t.Fatalf("page2 after removal: %v", err)
	}
	if len(page2.Edges) != 1 {
		t.Fatalf("page2 edges = %d, want 1", len(page2.Edges))
	}
	if page2.Edges[0].Node.ID == firstID {
		t.Fatalf("page2 repeats removed relation %q", firstID)
	}
}

func TestEntityHistoryCursorIsJournalScoped(t *testing.T) {
	// Build the projection graph so ResolveHandle maps "ent-1" to the entity.
	g := projection.New()
	entity := model.Entity{
		ID:       model.EntityID("ent-1"),
		Type:     "host",
		Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}},
	}
	g.Apply(model.Event{Entity: &model.EntityEvent{
		EventID:    "h-00",
		ChangeType: model.EntityCreated,
		Entity:     entity,
		EventTime:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}})

	// Six attribute updates, oldest to newest; the store serves them newest-first.
	base := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
	events := make([]model.Event, 0, 6)
	for i := 1; i <= 6; i++ {
		events = append(events, model.Event{Entity: &model.EntityEvent{
			EventID:    fmt.Sprintf("h-%02d", i),
			ChangeType: model.EntityAttributeUpdated,
			Entity:     entity,
			EventTime:  base.Add(time.Duration(i) * time.Minute),
		}})
	}

	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	first := 2

	// Journal A issues the first page.
	readerA := &journalScopedReader{fakeEventReader: &fakeEventReader{journal: hexJournal(0x0A)}, events: events}
	resolverA := &queryResolver{&Resolver{Graph: g, Store: readerA, Now: func() time.Time { return now }}}

	page1, err := resolverA.EntityHistory(context.Background(), "ent-1", nil, nil, nil, false, &first, nil)
	if err != nil {
		t.Fatalf("journal A first page: unexpected error: %v", err)
	}
	if len(page1.Edges) != 2 {
		t.Fatalf("journal A first page: got %d edges, want 2", len(page1.Edges))
	}
	cursor := page1.Edges[len(page1.Edges)-1].Cursor
	if cursor == "" {
		t.Fatal("journal A first page: empty cursor on last edge")
	}

	// Replaying that cursor on journal B must be rejected as cross-replica.
	readerB := &journalScopedReader{fakeEventReader: &fakeEventReader{journal: hexJournal(0x0B)}, events: events}
	resolverB := &queryResolver{&Resolver{Graph: g, Store: readerB, Now: func() time.Time { return now }}}
	_, err = resolverB.EntityHistory(context.Background(), "ent-1", nil, nil, nil, false, &first, &cursor)
	if err == nil || !strings.Contains(err.Error(), "another replica") {
		t.Fatalf("journal B replay: got err=%v, want error containing %q", err, "another replica")
	}

	// Replaying on journal A returns the next page with no repeated EventID.
	page2, err := resolverA.EntityHistory(context.Background(), "ent-1", nil, nil, nil, false, &first, &cursor)
	if err != nil {
		t.Fatalf("journal A second page: unexpected error: %v", err)
	}
	if len(page2.Edges) != 2 {
		t.Fatalf("journal A second page: got %d edges, want 2", len(page2.Edges))
	}
	seen := map[string]bool{page1.Edges[0].Node.ID: true, page1.Edges[1].Node.ID: true}
	for _, e := range page2.Edges {
		if seen[e.Node.ID] {
			t.Fatalf("journal A second page: repeated EventID %q", e.Node.ID)
		}
		seen[e.Node.ID] = true
	}
	if page2.Edges[0].Node.ID != "h-03" || page2.Edges[1].Node.ID != "h-04" {
		t.Fatalf("journal A second page: got IDs %q, %q; want h-03, h-04",
			page2.Edges[0].Node.ID, page2.Edges[1].Node.ID)
	}
}

// journalScopedReader is the shared fake with a fixed per-entity history, so
// EntityHistory has something to page through.
type journalScopedReader struct {
	*fakeEventReader
	events []model.Event
}

func (r *journalScopedReader) ReadByEntity(context.Context, model.EntityID) ([]model.Event, error) {
	return r.events, nil
}
