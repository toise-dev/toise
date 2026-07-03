package emit

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

func descriptor(t *testing.T, c *Client, rel Relationship) pcommon.Map {
	t.Helper()
	ent := Entity{
		Type:          "network.device",
		ID:            map[string]string{"name": "sw1"},
		Relationships: []Relationship{rel},
	}
	ld, err := c.Build(wire.EventEntityState, []Entity{ent})
	if err != nil {
		t.Fatal(err)
	}
	rels, ok := ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).
		Attributes().Get(wire.AttrEntityRelationships)
	if !ok || rels.Slice().Len() != 1 {
		t.Fatalf("no relationship descriptor emitted")
	}
	return rels.Slice().At(0).Map()
}

// TestSameAsBeliefEmitted proves a producer can now express same_as confidence
// and basis on the wire — the input the canonical overlay (ADR 0020) collapses
// on. Without this the overlay was inert: no producer could feed it a belief.
func TestSameAsBeliefEmitted(t *testing.T) {
	c, _, _ := fixtureClient(t)
	m := descriptor(t, c, Relationship{
		Type:       wire.RelTypeSameAs,
		TargetType: "network.device",
		TargetID:   map[string]string{"mac": "00:11:22:33:44:55"},
		Confidence: 0.95,
		Basis:      "ifPhysAddress",
	})
	conf, ok := m.Get(wire.RelConfidence)
	if !ok || conf.Type() != pcommon.ValueTypeDouble || conf.Double() != 0.95 {
		t.Errorf("confidence = %v, want double 0.95", conf.AsRaw())
	}
	if basis, _ := m.Get(wire.RelBasis); basis.Str() != "ifPhysAddress" {
		t.Errorf("basis = %q, want ifPhysAddress", basis.Str())
	}
}

// TestBeliefAttributesOnlyOnSameAs pins that confidence/basis ride only on
// same_as edges: a runs_on descriptor never carries them even if the producer
// set them, so ordinary embedded edges stay attribute-free.
func TestBeliefAttributesOnlyOnSameAs(t *testing.T) {
	c, _, _ := fixtureClient(t)
	m := descriptor(t, c, Relationship{
		Type:       "runs_on",
		TargetType: "host",
		TargetID:   map[string]string{"host.id": "h-1"},
		Confidence: 0.9,
		Basis:      "irrelevant",
	})
	if _, ok := m.Get(wire.RelConfidence); ok {
		t.Error("confidence emitted on a non-same_as edge")
	}
	if _, ok := m.Get(wire.RelBasis); ok {
		t.Error("basis emitted on a non-same_as edge")
	}
}

// TestSameAsWithoutConfidenceOmitsIt proves an unset belief is simply not
// carried (rather than a zero confidence, which the overlay would treat as
// "no belief" anyway), and that Build rejects an out-of-range confidence.
func TestSameAsBeliefValidation(t *testing.T) {
	c, _, _ := fixtureClient(t)

	m := descriptor(t, c, Relationship{
		Type:       wire.RelTypeSameAs,
		TargetType: "network.device",
		TargetID:   map[string]string{"mac": "aa:bb"},
	})
	if _, ok := m.Get(wire.RelConfidence); ok {
		t.Error("confidence emitted when unset (should be omitted)")
	}

	ent := Entity{
		Type: "network.device",
		ID:   map[string]string{"name": "sw1"},
		Relationships: []Relationship{{
			Type: wire.RelTypeSameAs, TargetType: "network.device",
			TargetID: map[string]string{"mac": "aa:bb"}, Confidence: 1.5,
		}},
	}
	if _, err := c.Build(wire.EventEntityState, []Entity{ent}); err == nil {
		t.Error("Build accepted a confidence outside [0,1]")
	}
}
