package metrics

import (
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

func stringValue(s string) model.Value { return model.StringValue(s) }

func arrayValue(parts ...string) model.Value {
	vals := make([]model.Value, len(parts))
	for i, p := range parts {
		vals[i] = model.StringValue(p)
	}
	return model.ArrayValue(vals)
}

func entity(id string, attrs, identity []model.KeyValue) model.Entity {
	return model.Entity{
		ID:         model.EntityID(id),
		Type:       typeNetworkDevice,
		Attributes: attrs,
		Identity:   identity,
	}
}

func TestSerialOverlap(t *testing.T) {
	tests := []struct {
		name           string
		entities       []model.Entity
		wantOverlaps   int
		wantComparable int
	}{
		{
			name:           "no entities",
			entities:       []model.Entity{},
			wantOverlaps:   0,
			wantComparable: 0,
		},
		{
			name: "entities without chassis serials",
			entities: []model.Entity{
				entity("a", nil, nil),
				entity("b", nil, nil),
			},
			wantOverlaps:   0,
			wantComparable: 0,
		},
		{
			name: "two entities sharing one serial without vendor",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
				entity("b", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
			},
			wantOverlaps:   1,
			wantComparable: 2,
		},
		{
			name: "two entities sharing one serial with one vendor known",
			entities: []model.Entity{
				entity("a", []model.KeyValue{
					{Key: attrChassisSerials, Value: stringValue("SN1234")},
					{Key: attrHWVendor, Value: stringValue("Cisco")},
				}, nil),
				entity("b", []model.KeyValue{
					{Key: attrChassisSerials, Value: stringValue("SN1234")},
				}, nil),
			},
			wantOverlaps:   1,
			wantComparable: 2,
		},
		{
			name: "two entities sharing one serial with different known vendors",
			entities: []model.Entity{
				entity("a", []model.KeyValue{
					{Key: attrChassisSerials, Value: stringValue("SN1234")},
					{Key: attrHWVendor, Value: stringValue("Cisco")},
				}, nil),
				entity("b", []model.KeyValue{
					{Key: attrChassisSerials, Value: stringValue("SN1234")},
					{Key: attrHWVendor, Value: stringValue("Juniper")},
				}, nil),
			},
			wantOverlaps:   0,
			wantComparable: 2,
		},
		{
			name: "two entities sharing one serial with same vendor ignoring case and spaces",
			entities: []model.Entity{
				entity("a", []model.KeyValue{
					{Key: attrChassisSerials, Value: stringValue("SN1234")},
					{Key: attrHWVendor, Value: stringValue(" Cisco ")},
				}, nil),
				entity("b", []model.KeyValue{
					{Key: attrChassisSerials, Value: stringValue("SN1234")},
					{Key: attrHWVendor, Value: stringValue("cisco")},
				}, nil),
			},
			wantOverlaps:   1,
			wantComparable: 2,
		},
		{
			name: "entity with a list of serials sharing one",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrChassisSerials, Value: arrayValue("SN1234", "SN9999")}}, nil),
				entity("b", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
			},
			wantOverlaps:   1,
			wantComparable: 2,
		},
		{
			name: "short serial shared by two entities",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("abc")}}, nil),
				entity("b", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("abc")}}, nil),
			},
			wantOverlaps:   0,
			wantComparable: 0,
		},
		{
			name: "same entity seen twice",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
				entity("a", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
			},
			wantOverlaps:   0,
			wantComparable: 1,
		},
		{
			name: "serial in identity instead of attributes",
			entities: []model.Entity{
				entity("a", nil, []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}),
				entity("b", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
			},
			wantOverlaps:   1,
			wantComparable: 2,
		},
		{
			name: "three entities sharing one serial",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
				entity("b", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
				entity("c", []model.KeyValue{{Key: attrChassisSerials, Value: stringValue("SN1234")}}, nil),
			},
			wantOverlaps:   1,
			wantComparable: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOverlaps, gotComparable := SerialOverlap(tt.entities)
			if gotOverlaps != tt.wantOverlaps || gotComparable != tt.wantComparable {
				t.Fatalf("SerialOverlap() = (%d, %d), want (%d, %d)", gotOverlaps, gotComparable, tt.wantOverlaps, tt.wantComparable)
			}
		})
	}
}

// Both series must be published even when nothing carries a serial. A zero that
// is never emitted cannot be told apart from a check that is not running, which
// is the reading this pair of metrics exists to prevent.
func TestSerialSeriesAreExposedEvenAtZero(t *testing.T) {
	body := scrape(t, Handler(NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc"), perTenantFixture()))
	for _, want := range []string{
		"toise_identity_serial_overlaps 0",
		"toise_identity_serial_comparable_entities 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not carry %q", want)
		}
	}
}

// tenantGraph serves one slice per tenant, as the aggregate does in production.
type tenantGraph struct{ perTenant [][]model.Entity }

func (tenantGraph) EntityCount() int            { return 0 }
func (tenantGraph) RelationCount() int          { return 0 }
func (tenantGraph) CountByType() map[string]int { return nil }
func (g tenantGraph) EntitiesOfTypePerTenant(string) [][]model.Entity {
	return g.perTenant
}

// Two estates that never met may legitimately carry the same chassis serial.
// Pooling them would publish a cross-tenant comparison as a single number, so
// the same serial in two tenants must count as no overlap at all.
func TestSerialOverlapDoesNotCompareAcrossTenants(t *testing.T) {
	serial := []model.KeyValue{{Key: attrChassisSerials, Value: model.StringValue("CN14HD90F4")}}
	g := tenantGraph{perTenant: [][]model.Entity{
		{{ID: "a", Type: typeNetworkDevice, Attributes: serial}},
		{{ID: "b", Type: typeNetworkDevice, Attributes: serial}},
	}}
	body := scrape(t, Handler(NewCollector(g, fakeStore{}, "1.2.3", "abc"), perTenantFixture()))
	if !strings.Contains(body, "toise_identity_serial_overlaps 0") {
		t.Error("one serial held by two TENANTS was reported as an overlap; tenants are being pooled")
	}
	if !strings.Contains(body, "toise_identity_serial_comparable_entities 2") {
		t.Error("the comparable population must still count both entities")
	}
}
