package emit

import (
	"testing"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// TestDeleteReasonEmitted asserts the SDK rides Entity.DeleteReason on
// entity.delete events as the entity.delete.reason attribute, and never on
// entity.state. See #260.
func TestDeleteReasonEmitted(t *testing.T) {
	c, _, _ := fixtureClient(t)
	ent := Entity{
		Type:         "host",
		ID:           map[string]string{"host.id": "h-1"},
		DeleteReason: "scaled_down",
	}

	ld, err := c.Build(wire.EventEntityDelete, []Entity{ent})
	if err != nil {
		t.Fatal(err)
	}
	lr := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	v, ok := lr.Attributes().Get(wire.AttrEntityDeleteReason)
	if !ok {
		t.Fatalf("delete event missing %s", wire.AttrEntityDeleteReason)
	}
	if v.Str() != "scaled_down" {
		t.Errorf("%s = %q, want %q", wire.AttrEntityDeleteReason, v.Str(), "scaled_down")
	}

	// A state event must NOT carry the reason even if the field is set.
	ls, err := c.Build(wire.EventEntityState, []Entity{ent})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := ls.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).
		Attributes().Get(wire.AttrEntityDeleteReason); present {
		t.Error("entity.state must not carry entity.delete.reason")
	}

	// An empty reason adds no attribute (purely additive, no empty noise).
	ld2, err := c.Build(wire.EventEntityDelete, []Entity{{Type: "host", ID: map[string]string{"host.id": "h-2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := ld2.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).
		Attributes().Get(wire.AttrEntityDeleteReason); present {
		t.Error("a delete with no reason must not emit an empty entity.delete.reason")
	}
}
