package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/toise-dev/toise/internal/model"
)

func digestEntEv(id string, ct model.ChangeType, typ string, src model.DeleteSource, keys []string, at time.Time) model.Event {
	return model.Event{Entity: &model.EntityEvent{
		EventID:      id,
		ChangeType:   ct,
		Entity:       model.Entity{ID: model.EntityID(id), Type: typ},
		ChangedKeys:  keys,
		DeleteSource: src,
		Scope:        "test-scope",
		EventTime:    at,
		RecordedAt:   at,
	}}
}

func noName(model.EntityID) string { return "" }

func TestGroupEventsLivenessDeletions(t *testing.T) {
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		digestEntEv("h1", model.EntityDeleted, "host", model.DeleteSourceLivenessExpiry, nil, base),
		digestEntEv("h2", model.EntityDeleted, "host", model.DeleteSourceLivenessExpiry, nil, base.Add(time.Minute)),
		digestEntEv("h3", model.EntityDeleted, "host", model.DeleteSourceLivenessExpiry, nil, base.Add(2*time.Minute)),
	}
	groups, total, groupCount, truncated := groupEvents(events, base.Add(-time.Hour), base, 50, noName)
	if total != 3 || groupCount != 1 || truncated || len(groups) != 1 {
		t.Fatalf("got total=%d groupCount=%d truncated=%v groups=%d", total, groupCount, truncated, len(groups))
	}
	g := groups[0]
	if g.Count != 3 {
		t.Fatalf("count = %d, want 3", g.Count)
	}
	key, params, text := labelFor(g)
	if key != "entity.deleted.liveness_expiry" {
		t.Fatalf("label_key = %q", key)
	}
	c, ok := params["count"].(int)
	if !ok || c != 3 {
		t.Fatalf("label_params[count] = %v (%T), want int 3", params["count"], params["count"])
	}
	if !strings.Contains(text, "stopped reporting (liveness expired)") {
		t.Fatalf("label = %q", text)
	}
}

func TestGroupEventsOrderNewestFirst(t *testing.T) {
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		digestEntEv("a1", model.EntityCreated, "host", "", nil, base.Add(10*time.Minute)),
		digestEntEv("b1", model.EntityCreated, "process", "", nil, base.Add(30*time.Minute)),
	}
	groups, _, _, _ := groupEvents(events, base.Add(-time.Hour), base, 50, noName)
	if len(groups) != 2 || groups[0].Type != "process" || groups[1].Type != "host" {
		t.Fatalf("order wrong: %+v", groups)
	}
}

func TestGroupEventsExamplesCappedNewestFirst(t *testing.T) {
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	var events []model.Event
	for i := 0; i < 7; i++ {
		events = append(events, digestEntEv("h"+strings.Repeat("x", i), model.EntityStateChanged, "host", "", []string{"state"}, base.Add(time.Duration(i)*time.Minute)))
	}
	groups, total, _, _ := groupEvents(events, base.Add(-time.Hour), base, 50, noName)
	if total != 7 || len(groups) != 1 || groups[0].Count != 7 {
		t.Fatalf("total=%d groups=%d count=%d", total, len(groups), groups[0].Count)
	}
	ex := groups[0].Examples
	if len(ex) != 5 {
		t.Fatalf("examples = %d, want 5", len(ex))
	}
	want := formatTime(base.Add(6 * time.Minute))
	if ex[0].At != want {
		t.Fatalf("newest example at = %q, want %q", ex[0].At, want)
	}
	for i := 1; i < len(ex); i++ {
		if ex[i].At > ex[i-1].At {
			t.Fatalf("examples not newest first at %d", i)
		}
	}
}

func TestGroupIDStableAcrossCallsAndWindowSensitive(t *testing.T) {
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	events := []model.Event{digestEntEv("a1", model.EntityCreated, "host", "", nil, base)}
	g1, _, _, _ := groupEvents(events, base.Add(-time.Hour), base, 50, noName)
	g2, _, _, _ := groupEvents(events, base.Add(-time.Hour), base, 50, noName)
	if g1[0].ID != g2[0].ID {
		t.Fatalf("id not stable: %q vs %q", g1[0].ID, g2[0].ID)
	}
	g3, _, _, _ := groupEvents(events, base.Add(-30*time.Minute), base, 50, noName)
	if g1[0].ID == g3[0].ID {
		t.Fatalf("id did not change with the window")
	}
}

func TestGroupEventsTruncated(t *testing.T) {
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	events := []model.Event{
		digestEntEv("a1", model.EntityCreated, "host", "", nil, base.Add(10*time.Minute)),
		digestEntEv("b1", model.EntityCreated, "process", "", nil, base.Add(30*time.Minute)),
	}
	groups, _, groupCount, truncated := groupEvents(events, base.Add(-time.Hour), base, 1, noName)
	if !truncated || groupCount != 2 || len(groups) != 1 {
		t.Fatalf("truncated=%v groupCount=%d len=%d", truncated, groupCount, len(groups))
	}
	if groups[0].Type != "process" {
		t.Fatalf("kept group = %q, want the newest", groups[0].Type)
	}
}

func TestChangeDigestWindowBounds(t *testing.T) {
	s := newTestServer()
	ctx := context.Background()
	if _, _, err := s.changeDigest(ctx, nil, ChangeDigestInput{To: "2026-05-29T12:45:00Z", Window: "16h"}); err == nil ||
		!strings.Contains(err.Error(), "window must be between 15m and 1h") {
		t.Fatalf("16h err = %v", err)
	}
	if _, _, err := s.changeDigest(ctx, nil, ChangeDigestInput{To: "2026-05-29T12:45:00Z", Window: "10m"}); err == nil ||
		!strings.Contains(err.Error(), "window must be between 15m and 1h") {
		t.Fatalf("10m err = %v", err)
	}
}

func TestChangeDigestWindow(t *testing.T) {
	s := newTestServer()
	_, out, err := s.changeDigest(context.Background(), nil, ChangeDigestInput{To: "2026-05-29T12:45:00Z", Window: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalChanges != 2 || out.GroupCount != 2 || len(out.Groups) != 2 {
		t.Fatalf("total=%d groupCount=%d groups=%d", out.TotalChanges, out.GroupCount, len(out.Groups))
	}
	if out.Groups[0].ChangeType != "relation.added" || out.Groups[1].ChangeType != "entity.created" {
		t.Fatalf("order: %s then %s", out.Groups[0].ChangeType, out.Groups[1].ChangeType)
	}
	if out.Groups == nil {
		t.Fatal("groups must never be nil")
	}
	// The tool output itself must carry the label, not only labelFor in isolation.
	rel := out.Groups[0]
	if rel.LabelKey != "relation.added" || rel.LabelParams["count"] != 1 || !strings.HasPrefix(rel.Label, "1 runs_on relations added at 12:30:00 UTC") {
		t.Errorf("relation group label: key=%q params=%v label=%q", rel.LabelKey, rel.LabelParams, rel.Label)
	}
	if len(rel.Examples) != 1 || rel.Examples[0].DisplayName == " -> " {
		t.Errorf("a relation example must name its endpoints: %+v", rel.Examples)
	}
}

func TestChangeDigestScope(t *testing.T) {
	ctx := context.Background()
	s := newTestServer()
	_, out, err := s.changeDigest(ctx, nil, ChangeDigestInput{
		To: "2026-05-29T12:45:00Z", Window: "1h",
		Scope: &DigestScope{Entity: "01HOST_DB", Depth: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.GroupCount != 0 || len(out.Groups) != 0 || out.Groups == nil {
		t.Fatalf("db scope: groupCount=%d len=%d", out.GroupCount, len(out.Groups))
	}

	s = newTestServer()
	_, out, err = s.changeDigest(ctx, nil, ChangeDigestInput{
		To: "2026-05-29T12:45:00Z", Window: "1h",
		Scope: &DigestScope{Entity: "01PROC_NGINX", Depth: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.GroupCount != 2 || len(out.Groups) != 2 {
		t.Fatalf("proc scope: groupCount=%d len=%d", out.GroupCount, len(out.Groups))
	}
	types := map[string]bool{}
	for _, g := range out.Groups {
		types[g.ChangeType] = true
	}
	if !types["relation.added"] || !types["entity.created"] {
		t.Fatalf("proc scope groups = %v", types)
	}

	s = newTestServer()
	_, _, err = s.changeDigest(ctx, nil, ChangeDigestInput{
		To: "2026-05-29T12:45:00Z", Window: "1h",
		Scope: &DigestScope{Entity: "nope", Depth: 0},
	})
	if err == nil || !strings.Contains(err.Error(), "scope entity not found: nope") {
		t.Fatalf("nope err = %v", err)
	}
}
