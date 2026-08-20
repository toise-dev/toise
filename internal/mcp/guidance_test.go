package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

// TestDisappearanceGlossStatesTheNegative pins the property that makes the gloss
// worth carrying: every value says, in words, that it is NOT an operator action.
// Consumers read the bare enum and reported human deletions, renames and manual
// removals that never happened (#346), so the denial is the load-bearing half.
func TestDisappearanceGlossStatesTheNegative(t *testing.T) {
	for _, src := range []model.DeleteSource{
		model.DeleteSourceProducer,
		model.DeleteSourceLivenessExpiry,
		model.DeleteSourceCascade,
	} {
		gloss := disappearanceGloss(src)
		if gloss == "" {
			t.Fatalf("%q has no gloss", src)
		}
		lower := strings.ToLower(gloss)
		if !strings.Contains(lower, "not") {
			t.Errorf("%q gloss never says what it is NOT: %q", src, gloss)
		}
	}
	if got := disappearanceGloss(model.DeleteSourceUnknown); got != "" {
		t.Errorf("unknown source glossed as %q; an event predating provenance must stay silent, never read as producer", got)
	}
}

// TestChangeCarriesDisappearanceGloss proves the sentence rides on the payload
// itself, on both entity and relation disappearances — a consumer must not have
// to fetch a contract document to read a deletion correctly.
func TestChangeCarriesDisappearanceGloss(t *testing.T) {
	ent := changeOut(model.Event{Entity: &model.EntityEvent{
		EventID: "e1", ChangeType: model.EntityDeleted, DeleteSource: model.DeleteSourceLivenessExpiry,
		Entity: host("01HOST_WEB", "web-server-1"),
	}})
	if !strings.Contains(ent.Disappearance, "still be running") {
		t.Errorf("entity deletion gloss does not warn the resource may survive: %q", ent.Disappearance)
	}
	rel := changeOut(model.Event{Relation: &model.RelationEvent{
		EventID: "r1", ChangeType: model.RelationRemoved, DeleteSource: model.DeleteSourceCascade,
		Relation: model.Relation{ID: "rel-runs", Type: "runs_on"},
	}})
	if !strings.Contains(rel.Disappearance, "consequence") {
		t.Errorf("cascade gloss does not mark the row as a consequence: %q", rel.Disappearance)
	}
}

// TestRecentChangesReachesAPastWindow is the regression for the failure that
// started #346: an incident window hours back was unreachable here, because the
// only way to widen the reach was a longer window whose limit then kept the
// NEWEST changes. With from/to the same tool answers about the past directly.
func TestRecentChangesReachesAPastWindow(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()

	// The fixture's oldest event is 8 hours before the server's "now"; a plain
	// 1h window cannot see it, a bounded past window can.
	_, recent, err := s.recentChanges(ctx, nil, RecentChangesInput{})
	if err != nil {
		t.Fatalf("recentChanges default: %v", err)
	}
	if recent.Total != 0 {
		t.Fatalf("default 1h window unexpectedly matched %d changes; fixture drifted", recent.Total)
	}

	_, past, err := s.recentChanges(ctx, nil, RecentChangesInput{
		From: "2026-05-29T11:00:00Z",
		To:   "2026-05-29T13:00:00Z",
	})
	if err != nil {
		t.Fatalf("recentChanges from/to: %v", err)
	}
	if past.Total == 0 {
		t.Fatal("a bounded past window found nothing; the incident-investigation path is broken")
	}
	if past.WindowFrom != "2026-05-29T11:00:00Z" || past.WindowTo != "2026-05-29T13:00:00Z" {
		t.Errorf("answer does not name the window it read: from=%q to=%q", past.WindowFrom, past.WindowTo)
	}
}

// TestRecentChangesConfessesTruncation pins the honesty rule: when the limit
// keeps only the newest slice, the answer must say which slice it covered and
// how to get the rest. A truncated window that reads as a complete one is how a
// graph holding the answer looks like a graph that has none.
func TestRecentChangesConfessesTruncation(t *testing.T) {
	s := newTestServer()

	_, out, err := s.recentChanges(context.Background(), nil, RecentChangesInput{
		From: "2026-05-29T11:00:00Z", To: "2026-05-29T15:00:00Z", Limit: 1,
	})
	if err != nil {
		t.Fatalf("recentChanges: %v", err)
	}
	if !out.Truncated || out.Count != 1 {
		t.Fatalf("want a truncated single-item answer, got truncated=%v count=%d total=%d", out.Truncated, out.Count, out.Total)
	}
	if out.Covered == "" {
		t.Fatal("truncated answer does not state what it covered")
	}
	for _, want := range []string{"PARTIAL", "NOT the whole window", "from/to"} {
		if !strings.Contains(out.Covered, want) {
			t.Errorf("covered notice missing %q: %q", want, out.Covered)
		}
	}

	// An untruncated answer stays quiet: the notice is a warning, not noise.
	_, full, err := s.recentChanges(context.Background(), nil, RecentChangesInput{
		From: "2026-05-29T11:00:00Z", To: "2026-05-29T15:00:00Z",
	})
	if err != nil {
		t.Fatalf("recentChanges full: %v", err)
	}
	if full.Covered != "" {
		t.Errorf("complete answer carries a partial-coverage notice: %q", full.Covered)
	}
}

// TestServerInstructionsCoverTheCostlyTraps pins the content of the one guidance
// channel every client gets at initialize. Each assertion here is a mistake that
// was actually made against a live instance, so a rewrite that drops one is a
// regression, not an edit.
func TestServerInstructionsCoverTheCostlyTraps(t *testing.T) {
	for _, want := range []string{
		"describe_schema", // guessing type names reads as "Toise does not know"
		"graph_diff",      // the fleet-wide incident answer nobody reached
		"from/to",         // how to target a past window
		"disappearance",   // the field that prevents inventing human deletions
		"find_entities",   // re-resolving instead of carrying an id
		"get_neighbors",   // an address is two hops away, not an attribute
	} {
		if !strings.Contains(serverInstructions, want) {
			t.Errorf("server instructions never mention %q", want)
		}
	}
	if !strings.Contains(serverInstructions, "15 minutes") {
		t.Error("server instructions omit the resurrection window that re-mints ids")
	}
	if strings.Contains(serverInstructions, "single call usually answers") {
		t.Error("the claim that one call usually suffices is back; it is what taught consumers to stop at depth 1")
	}
}

// TestGuideResourceCarriesTheTraps keeps the pinnable guide aligned with the
// instructions: a client that reads one or the other must not get a version of
// the truth that omits the traps.
func TestGuideResourceCarriesTheTraps(t *testing.T) {
	for _, want := range []string{"disappearance", "delete_source", "from", "get_neighbors", "find_entities"} {
		if !strings.Contains(guideText, want) {
			t.Errorf("toise://guide never mentions %q", want)
		}
	}
	if strings.Contains(guideText, "without a second lookup") {
		t.Error("the guide still promises one call is enough; the address of a host takes two hops")
	}
}

// TestRecentChangesRejectsContradictoryBounds keeps the borrowed graph_diff
// semantics honest here too: window and from are alternatives, not a pair.
func TestRecentChangesRejectsContradictoryBounds(t *testing.T) {
	s := newTestServer()
	_, _, err := s.recentChanges(context.Background(), nil, RecentChangesInput{
		Window: "1h", From: time.Now().Format(time.RFC3339),
	})
	if err == nil {
		t.Fatal("window and from accepted together; the answer would silently be about one of them")
	}
}
