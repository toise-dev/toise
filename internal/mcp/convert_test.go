package mcp

import (
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
