package resolvers

import (
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

// GraphQL carries the readable name in its own field, like MCP: the value you
// show and the value you key on are separate, so neither invites the other's
// use (#384, ADR 0035).
func TestEntityToGQLCarriesDisplayName(t *testing.T) {
	e := model.Entity{
		ID:         "01ABC",
		Type:       model.TypeContainer,
		Identity:   []model.KeyValue{{Key: "container.id", Value: model.StringValue("b5183e6e")}},
		Attributes: []model.KeyValue{{Key: "container.name", Value: model.StringValue("senhub-ping")}},
	}

	got := entityToGQL(e, false)
	if got.DisplayName != "senhub-ping" {
		t.Fatalf("displayName = %q, want senhub-ping", got.DisplayName)
	}
	if got.IdentityFingerprint == "" {
		t.Fatal("identityFingerprint must still be populated")
	}

	renamed := e
	renamed.Attributes = []model.KeyValue{{Key: "container.name", Value: model.StringValue("senhub-ping-2")}}
	after := entityToGQL(renamed, false)
	if after.DisplayName == got.DisplayName {
		t.Fatal("the fixture must actually change the display name")
	}
	if after.IdentityFingerprint != got.IdentityFingerprint {
		t.Fatalf("renaming changed the fingerprint: %s vs %s",
			got.IdentityFingerprint, after.IdentityFingerprint)
	}

	// No readable name observed: the field is empty rather than echoing the id.
	bare := model.Entity{
		Type:     model.TypeContainer,
		Identity: []model.KeyValue{{Key: "container.id", Value: model.StringValue("b5183e6e")}},
	}
	if got := entityToGQL(bare, false).DisplayName; got != "" {
		t.Fatalf("displayName = %q, want empty", got)
	}
}
