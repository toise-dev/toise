package metrics

import (
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

func TestVendorNonNormative(t *testing.T) {
	tests := []struct {
		name           string
		entities       []model.Entity
		wantNonNorm    int
		wantPopulation int
	}{
		{
			name:           "no entities",
			entities:       []model.Entity{},
			wantNonNorm:    0,
			wantPopulation: 0,
		},
		{
			name: "entity without hw.vendor",
			entities: []model.Entity{
				entity("a", nil, nil),
			},
			wantNonNorm:    0,
			wantPopulation: 0,
		},
		{
			name: "normative value",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrHWVendor, Value: stringValue("h3c")}}, nil),
			},
			wantNonNorm:    0,
			wantPopulation: 1,
		},
		{
			name: "capitalized value",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrHWVendor, Value: stringValue("Fortinet")}}, nil),
			},
			wantNonNorm:    1,
			wantPopulation: 1,
		},
		{
			name: "value with surrounding spaces",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrHWVendor, Value: stringValue(" h3c ")}}, nil),
			},
			wantNonNorm:    1,
			wantPopulation: 1,
		},
		{
			name: "free-form value with commas",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrHWVendor, Value: stringValue("Cisco Systems, Inc.")}}, nil),
			},
			wantNonNorm:    1,
			wantPopulation: 1,
		},
		{
			name: "mixed fleet with hw.vendor in identity",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrHWVendor, Value: stringValue("cisco")}}, nil),
				entity("b", nil, []model.KeyValue{{Key: attrHWVendor, Value: stringValue("Arista")}}),
				entity("c", nil, nil),
			},
			wantNonNorm:    1,
			wantPopulation: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNonNorm, gotPopulation := VendorNonNormative(tt.entities)
			if gotNonNorm != tt.wantNonNorm || gotPopulation != tt.wantPopulation {
				t.Fatalf("VendorNonNormative() = (%d, %d), want (%d, %d)", gotNonNorm, gotPopulation, tt.wantNonNorm, tt.wantPopulation)
			}
		})
	}
}

func TestRetiredVendorKey(t *testing.T) {
	tests := []struct {
		name     string
		entities []model.Entity
		want     int
	}{
		{
			name:     "no entities",
			entities: []model.Entity{},
			want:     0,
		},
		{
			name: "no retired key",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrHWVendor, Value: stringValue("h3c")}}, nil),
			},
			want: 0,
		},
		{
			name: "retired key in attributes",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrVendor, Value: stringValue("h3c")}}, nil),
			},
			want: 1,
		},
		{
			name: "retired key in identity",
			entities: []model.Entity{
				entity("a", nil, []model.KeyValue{{Key: attrVendor, Value: stringValue("hpe")}}),
			},
			want: 1,
		},
		{
			name: "retired key counted once per entity",
			entities: []model.Entity{
				entity("a", []model.KeyValue{{Key: attrVendor, Value: stringValue("h3c")}}, nil),
				entity("b", nil, []model.KeyValue{{Key: attrVendor, Value: stringValue("hpe")}}),
				entity("c", nil, nil),
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RetiredVendorKey(tt.entities); got != tt.want {
				t.Fatalf("RetiredVendorKey() = %d, want %d", got, tt.want)
			}
		})
	}
}

// All three series must be published even when nothing carries a vendor. A
// zero that is never emitted cannot be told apart from a check that is not
// running, which is the reading this set of metrics exists to prevent.
func TestVendorSeriesAreExposedEvenAtZero(t *testing.T) {
	body := scrape(t, Handler(NewCollector(fakeGraph{}, fakeStore{}, "1.2.3", "abc"), perTenantFixture()))
	for _, want := range []string{
		"toise_identity_vendor_non_normative 0",
		"toise_identity_vendor_comparable_entities 0",
		"toise_identity_vendor_retired_key 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not carry %q", want)
		}
	}
}

// The reader's actual question: "show me my Cisco estate" must not silently
// undercount. A capitalized value is the ENTITY-MIB fallback and cannot be
// grouped, so it has to be counted as non-normative while still belonging to
// the population the check looked at.
func TestVendorUndercountIsVisibleAsAWhole(t *testing.T) {
	dev := func(id, key, val string) model.Entity {
		return model.Entity{ID: model.EntityID(id), Type: typeNetworkDevice,
			Attributes: []model.KeyValue{{Key: key, Value: model.StringValue(val)}}}
	}
	g := tenantGraph{perTenant: [][]model.Entity{{
		dev("a", attrHWVendor, "cisco"),
		dev("b", attrHWVendor, "Fortinet"),
		dev("c", attrHWVendor, "Cisco Systems, Inc."),
		dev("d", attrHWVendor, " h3c "),
		dev("e", "vendor", "h3c"),
	}}}
	body := scrape(t, Handler(NewCollector(g, fakeStore{}, "1.2.3", "abc"), perTenantFixture()))
	for _, want := range []string{
		"toise_identity_vendor_non_normative 3",
		"toise_identity_vendor_comparable_entities 4",
		"toise_identity_vendor_retired_key 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not carry %q", want)
		}
	}
}
