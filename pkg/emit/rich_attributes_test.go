package emit

import (
	"bytes"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	plogotlp "go.opentelemetry.io/collector/pdata/plog/plogotlp"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// TestRichAttributesEmitted proves the SDK emits the full AnyValue (arrays and
// nested maps) on entity.description, symmetric with what Toise now ingests
// (#259). The scalar Attributes and the rich ones share one description map.
func TestRichAttributesEmitted(t *testing.T) {
	c, _, _ := fixtureClient(t)
	ent := Entity{
		Type:       "host",
		ID:         map[string]string{"host.id": "h-1"},
		Attributes: map[string]string{"os.type": "linux"},
		RichAttributes: map[string]any{
			"cpu.count": 8,
			"tags":      []any{"edge", "prod"},
			"net":       map[string]any{"mtu": int64(1500), "addrs": []any{"10.0.0.1", "10.0.0.2"}},
		},
	}
	ld, err := c.Build(wire.EventEntityState, []Entity{ent})
	if err != nil {
		t.Fatal(err)
	}
	desc, ok := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).
		Attributes().Get(wire.AttrEntityDescription)
	if !ok || desc.Type() != pcommon.ValueTypeMap {
		t.Fatalf("no entity.description map emitted")
	}
	m := desc.Map()

	if v, _ := m.Get("os.type"); v.Str() != "linux" {
		t.Errorf("scalar os.type = %q, want linux", v.Str())
	}
	if v, _ := m.Get("cpu.count"); v.Type() != pcommon.ValueTypeInt || v.Int() != 8 {
		t.Errorf("cpu.count = %v, want int 8", v.AsRaw())
	}
	tags, _ := m.Get("tags")
	if tags.Type() != pcommon.ValueTypeSlice || tags.Slice().Len() != 2 ||
		tags.Slice().At(0).Str() != "edge" || tags.Slice().At(1).Str() != "prod" {
		t.Errorf("tags = %v, want [edge prod]", tags.AsRaw())
	}
	net, _ := m.Get("net")
	if net.Type() != pcommon.ValueTypeMap {
		t.Fatalf("net = %v, want a nested map", net.AsRaw())
	}
	if mtu, _ := net.Map().Get("mtu"); mtu.Int() != 1500 {
		t.Errorf("net.mtu = %v, want 1500", mtu.AsRaw())
	}
	if addrs, _ := net.Map().Get("addrs"); addrs.Slice().Len() != 2 {
		t.Errorf("net.addrs len = %d, want 2", addrs.Slice().Len())
	}
}

// TestRichAttributesDeterministic pins that two builds of the same rich input
// produce byte-identical wire output (sorted map keys at every level).
func TestRichAttributesDeterministic(t *testing.T) {
	c, _, _ := fixtureClient(t)
	ent := Entity{
		Type: "host", ID: map[string]string{"host.id": "h-1"},
		RichAttributes: map[string]any{
			"z": map[string]any{"b": 2, "a": 1},
			"a": []any{"x", "y"},
		},
	}
	a, err := c.Build(wire.EventEntityState, []Entity{ent})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Build(wire.EventEntityState, []Entity{ent})
	if err != nil {
		t.Fatal(err)
	}
	ab, _ := plogotlp.NewExportRequestFromLogs(a).MarshalProto()
	bb, _ := plogotlp.NewExportRequestFromLogs(b).MarshalProto()
	if !bytes.Equal(ab, bb) {
		t.Fatal("rich-attribute builds differ — map ordering leaked into the wire form")
	}
}

// TestRichAttributesGuardrails pins the no-silent-loss guardrails: a key shared
// with the scalar attributes and an unsupported value type both fail Build.
func TestRichAttributesGuardrails(t *testing.T) {
	c, _, _ := fixtureClient(t)

	dup := Entity{
		Type: "host", ID: map[string]string{"host.id": "h-1"},
		Attributes:     map[string]string{"k": "v"},
		RichAttributes: map[string]any{"k": 1},
	}
	if _, err := c.Build(wire.EventEntityState, []Entity{dup}); err == nil {
		t.Error("a key in both Attributes and RichAttributes must be rejected")
	}

	bad := Entity{
		Type: "host", ID: map[string]string{"host.id": "h-1"},
		RichAttributes: map[string]any{"weird": struct{}{}},
	}
	if _, err := c.Build(wire.EventEntityState, []Entity{bad}); err == nil {
		t.Error("an unsupported value type must be rejected, not silently dropped")
	}

	// uint64 is deliberately unsupported (it can overflow int64) — must error too.
	over := Entity{
		Type: "host", ID: map[string]string{"host.id": "h-1"},
		RichAttributes: map[string]any{"big": uint64(1)},
	}
	if _, err := c.Build(wire.EventEntityState, []Entity{over}); err == nil {
		t.Error("uint64 must be rejected rather than risk an overflowing conversion")
	}
}
