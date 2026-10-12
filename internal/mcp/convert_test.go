package mcp

import (
	"fmt"
	"sort"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// An entity whose identity is a uuid renders its readable name in its own
// field, so a caller has something to show without splitting the label and
// without digging through attributes for a value that drifts (#384).
func TestEntityOutCarriesDisplayName(t *testing.T) {
	e := model.Entity{
		ID:         "01ABC",
		Type:       model.TypeHost,
		Identity:   []model.KeyValue{{Key: "host.id", Value: model.StringValue("6ccc0dcc")}},
		Attributes: []model.KeyValue{{Key: "host.name", Value: model.StringValue("dash172")}},
	}

	full := entityOut(e, false)
	if full.DisplayName != "dash172" {
		t.Fatalf("display_name = %q, want dash172", full.DisplayName)
	}
	if full.IdentityFingerprint == "" || full.IdentityFingerprint == full.DisplayName {
		t.Fatalf("the key and the display value must be separate fields, got %q / %q",
			full.IdentityFingerprint, full.DisplayName)
	}

	// Compact is the mode that exists to scan many entities cheaply, and it is
	// the one that used to hide every readable name.
	compact := entityOutV(e, false, true)
	if compact.DisplayName != "dash172" {
		t.Fatalf("compact display_name = %q, want dash172", compact.DisplayName)
	}

	// A rename moves the display value and leaves the handle alone. Nothing may
	// key on the first; that is what the second is for.
	renamed := e
	renamed.Attributes = []model.KeyValue{{Key: "host.name", Value: model.StringValue("dash172-bis")}}
	after := entityOut(renamed, false)
	if after.DisplayName == full.DisplayName {
		t.Fatal("the fixture must actually change the display name")
	}
	if after.IdentityFingerprint != full.IdentityFingerprint {
		t.Fatalf("renaming changed the fingerprint: %s vs %s",
			full.IdentityFingerprint, after.IdentityFingerprint)
	}

	// An entity identified by its own name still has a name to show: the field
	// always answers when one exists, so a caller never needs a fallback branch.
	// The LABEL is what skips it, to avoid "host web-server-1 host.name=web-server-1".
	byName := model.Entity{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.name", Value: model.StringValue("web-server-1")}},
	}
	out := entityOut(byName, false)
	if out.DisplayName != "web-server-1" {
		t.Fatalf("display_name = %q, want web-server-1", out.DisplayName)
	}
	if out.Label != "host host.name=web-server-1" {
		t.Fatalf("label = %q, want no duplicated name", out.Label)
	}

	// A type whose readable name has to be composed gets it composed here, once,
	// rather than in every consumer.
	ep := model.Entity{
		Type: model.TypeNetworkEndpoint,
		Identity: []model.KeyValue{
			{Key: "server.address", Value: model.StringValue("10.0.0.5")},
			{Key: "server.port", Value: model.StringValue("5432")},
		},
	}
	if got := entityOut(ep, false).DisplayName; got != "10.0.0.5:5432" {
		t.Fatalf("endpoint display_name = %q, want 10.0.0.5:5432", got)
	}
}

// TestAttributeFilterKeepsAnEntityThatDied is the trap #380 warned about, and the
// reason this filter tests the event's own snapshot rather than the live graph.
//
// Resolving the matching id set from the projection first — the shape the issue
// proposed — answers "changes to entities carrying the attribute NOW". An entity
// deleted during the window is gone from the projection, so its changes would
// vanish from the filtered view. For an incident that is exactly backwards: the
// entities worth looking at are the ones that died. A filter that silently
// dropped every deletion would reproduce, inside the feature meant to fix a
// partial answer, the defect of a partial answer.
func TestAttributeFilterKeepsAnEntityThatDied(t *testing.T) {
	f, err := newChangeFilterFull("", "", map[string]string{"entity.label.trial": "X"}, false)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if !f.needsRecord() {
		t.Fatal("an attribute filter must resolve records; it cannot be answered from the index tag")
	}

	trial := model.Entity{
		ID:         "e1",
		Type:       model.TypeHost,
		Identity:   []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}},
		Attributes: []model.KeyValue{{Key: "entity.label.trial", Value: model.StringValue("X")}},
	}
	other := trial
	other.Attributes = []model.KeyValue{{Key: "entity.label.trial", Value: model.StringValue("Y")}}

	// The case that matters: a DELETION carrying the last-known state. It must be
	// kept, because it is the most interesting event in the window.
	death := model.Event{Entity: &model.EntityEvent{
		ChangeType:   model.EntityDeleted,
		Entity:       trial,
		DeleteSource: model.DeleteSourceLivenessExpiry,
	}}
	if !f.keepScope(death) {
		t.Error("an entity that DIED during the window was dropped by the attribute filter; that is the trap this design avoids")
	}

	if !f.keepScope(model.Event{Entity: &model.EntityEvent{ChangeType: model.EntityCreated, Entity: trial}}) {
		t.Error("a matching creation was dropped")
	}
	if f.keepScope(model.Event{Entity: &model.EntityEvent{ChangeType: model.EntityCreated, Entity: other}}) {
		t.Error("a non-matching entity was kept; the filter does nothing")
	}

	// A relation carries no entity, so it cannot answer a question about entity
	// attributes. Excluded rather than guessed at — and the tool description says
	// so, because an exclusion the caller does not know about is the defect.
	rel := model.Event{Relation: &model.RelationEvent{ChangeType: model.RelationAdded}}
	if f.keepScope(rel) {
		t.Error("a relation change matched an entity-attribute filter; it carries no entity to match")
	}

	// Without the filter, nothing is excluded and no decoding is forced.
	plain, err := newChangeFilterFull("", "", nil, false)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if plain.needsRecord() {
		t.Error("an unfiltered read must not pay for record resolution")
	}
	if !plain.keepScope(rel) {
		t.Error("an unfiltered read dropped a relation change")
	}
}

// TestEveryEntityIsReachableByPaging is #432: before this, an answer declared
// what it was withholding and offered no way to obtain it.
//
// Measured on a live tenant: describe_schema reported 236 network.endpoint and
// find_entities could return at most 200. Thirty-six were unreachable — not slow
// to reach, unreachable — and for that type there was not even an attribute to
// slice on, because network.endpoint carries none by design.
func TestEveryEntityIsReachableByPaging(t *testing.T) {
	const total = 236
	all := make([]model.Entity, 0, total)
	for i := range total {
		all = append(all, model.Entity{
			ID:       model.EntityID(fmt.Sprintf("e%03d", i)),
			Type:     model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue(fmt.Sprintf("h%03d", i))}},
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].IdentityHash() < all[j].IdentityHash() })

	// Walk every page to exhaustion, exactly as a consumer must.
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		page, next, err := pageByFingerprint(all, cursor, 200)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, e := range page {
			seen[e.IdentityHash()]++
		}
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("paging does not terminate")
		}
	}

	if len(seen) != total {
		t.Errorf("reached %d of %d entities in %d pages; the rest is still unreachable", len(seen), total, pages)
	}
	for fp, n := range seen {
		if n != 1 {
			t.Errorf("entity %s returned %d times; a page sequence must not repeat", fp, n)
		}
	}
	if pages != 2 {
		t.Errorf("236 entities at 200 per page took %d pages, want 2", pages)
	}

	// A cursor a caller invented must be refused, not silently treated as the
	// beginning — that would re-serve page one and look like an empty tail.
	if _, _, err := pageByFingerprint(all, "not-base64-%%%", 200); err == nil {
		t.Error("a malformed cursor was accepted; it would silently restart the sequence")
	}

	// The last page carries no cursor: a caller must be able to stop.
	_, next, err := pageByFingerprint(all[:10], "", 200)
	if err != nil || next != "" {
		t.Errorf("a complete answer returned cursor %q; a caller cannot tell it is done", next)
	}
}

// TestNeighborPagingKeepsClosestFirst guards the property that made the answer
// useful in the first place. Paging on the fingerprint alone would have given a
// deterministic order that scattered the nearest neighbors across pages.
func TestNeighborPagingKeepsClosestFirst(t *testing.T) {
	mk := func(depth int, fp string) Neighbor {
		var n Neighbor
		n.Depth = depth
		n.IdentityFingerprint = fp
		return n
	}
	// Deliberately: the deepest neighbor sorts FIRST by fingerprint alone.
	all := []Neighbor{mk(1, "zz"), mk(1, "yy"), mk(2, "aa"), mk(2, "bb"), mk(3, "cc")}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Depth != all[j].Depth {
			return all[i].Depth < all[j].Depth
		}
		return all[i].IdentityFingerprint < all[j].IdentityFingerprint
	})

	page, next, err := pageNeighbors(all, "", 2)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	for _, n := range page {
		if n.Depth != 1 {
			t.Errorf("first page holds a depth-%d neighbor; the closest must come first", n.Depth)
		}
	}
	if next == "" {
		t.Fatal("a truncated neighborhood returned no cursor; the rest is unreachable")
	}

	// Walking on must not re-serve the shallower ones, which is what a
	// fingerprint-only cursor would have done ("aa" < "yy").
	seen := map[string]int{}
	for _, n := range page {
		seen[n.IdentityFingerprint]++
	}
	for next != "" {
		page, next, err = pageNeighbors(all, next, 2)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		for _, n := range page {
			seen[n.IdentityFingerprint]++
		}
	}
	if len(seen) != len(all) {
		t.Errorf("reached %d of %d neighbors", len(seen), len(all))
	}
	for fp, n := range seen {
		if n != 1 {
			t.Errorf("neighbor %s served %d times", fp, n)
		}
	}
}
