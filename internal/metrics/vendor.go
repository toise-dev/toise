package metrics

import (
	"strings"

	"github.com/toise-dev/toise/internal/model"
)

const attrVendor = "vendor"

// VendorNonNormative counts network.device entities whose hw.vendor is present
// but outside the normative form, and the population for which the question is
// meaningful: the entities that carry hw.vendor at all.
//
// The normative form is the short lowercase registry name derived from the
// IANA enterprise number. A value outside it is usually the ENTITY-MIB
// fallback, a free-form manufacturer string, which cannot be grouped: the same
// vendor reads "Cisco Systems, Inc." on one device and "Cisco" on another, and
// a fleet query by vendor then silently undercounts. It is not asserted to be
// an error: a registry name could one day contain a character this rule
// rejects. The count is what alerts; the offending value is read from the API.
//
// Vendor values are intentionally not exposed as metric labels: a vendor is
// inventory data and label cardinality would follow the fleet.
func VendorNonNormative(entities []model.Entity) (nonNormative, population int) {
	for _, e := range entities {
		value, ok := findAttr(e, attrHWVendor)
		if !ok {
			continue
		}
		population++
		// The normative form is a NAME. A hw.vendor that is not a string cannot
		// be one, whatever it renders as: the raw enterprise number 729 renders
		// as "729", which is non-empty, lowercase and unpadded, and would pass
		// a check written on the rendering alone.
		if value.Kind() != model.KindString || !isNormativeVendor(value.Str()) {
			nonNormative++
		}
	}
	return nonNormative, population
}

// RetiredVendorKey counts network.device entities still carrying the retired
// `vendor` key, in identity or descriptive attributes. The key was replaced by
// hw.vendor; a value left on the old key is invisible to every filter written
// against the new one, so "show me my HPE fleet" returns an empty list that
// reads as "no HPE in this fleet".
func RetiredVendorKey(entities []model.Entity) (retired int) {
	for _, e := range entities {
		if _, ok := findAttr(e, attrVendor); ok {
			retired++
		}
	}
	return retired
}

func isNormativeVendor(s string) bool {
	return s != "" && s == strings.ToLower(s) && s == strings.TrimSpace(s)
}
