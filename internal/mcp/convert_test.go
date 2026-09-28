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

	// An entity identified by its name has nothing extra to show.
	byName := model.Entity{
		Type:     model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.name", Value: model.StringValue("web-server-1")}},
	}
	if got := entityOut(byName, false).DisplayName; got != "" {
		t.Fatalf("display_name = %q, want empty when the name is the identity", got)
	}
}
