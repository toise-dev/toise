package metrics

import (
	"strings"

	"github.com/toise-dev/toise/internal/model"
)

const (
	attrChassisSerials = "senhub.chassis.serial_numbers"
	attrHWVendor       = "hw.vendor"
	typeNetworkDevice  = "network.device"
	minSerialLen       = 4
)

// SerialOverlap counts chassis serial numbers that are carried by more than one
// live entity, and the population for which the question is meaningful.
//
// It does not assert that a collision is an error: a chassis moved from one
// stack to another is expected to trigger it. Vendor is therefore used as a
// disqualifier rather than a condition: two entities with different known
// vendors cannot be the same physical chassis, but missing vendor information
// does not prevent a match.
//
// Serial numbers are intentionally not exposed as metric labels. A serial
// number identifies a physical unit and is inventory data; label cardinality
// would scale with the fleet. The metric alerts by count, identity can be read
// from the API.
func SerialOverlap(entities []model.Entity) (overlaps int, comparable int) {
	type entityID = model.EntityID

	serialToEntities := make(map[string]map[entityID]model.Entity)
	comparableIDs := make(map[entityID]struct{})

	for _, e := range entities {
		serials := extractSerials(e)
		if len(serials) == 0 {
			continue
		}

		comparableIDs[e.ID] = struct{}{}
		for _, s := range serials {
			group := serialToEntities[s]
			if group == nil {
				group = make(map[entityID]model.Entity)
				serialToEntities[s] = group
			}
			group[e.ID] = e
		}
	}

	comparable = len(comparableIDs)

	for _, group := range serialToEntities {
		if len(group) < 2 {
			continue
		}
		if vendorDisqualifies(group) {
			continue
		}
		overlaps++
	}

	return overlaps, comparable
}

func extractSerials(e model.Entity) []string {
	value, ok := findAttr(e, attrChassisSerials)
	if !ok {
		return nil
	}

	switch value.Kind() {
	case model.KindString:
		s := strings.TrimSpace(value.Str())
		if len(s) >= minSerialLen {
			return []string{s}
		}
		return nil
	case model.KindArray:
		serials := make([]string, 0, len(value.Array()))
		for _, item := range value.Array() {
			var s string
			if item.Kind() == model.KindString {
				s = strings.TrimSpace(item.Str())
			} else {
				s = strings.TrimSpace(item.Display())
			}
			if len(s) >= minSerialLen {
				serials = append(serials, s)
			}
		}
		return serials
	default:
		return nil
	}
}

func findAttr(e model.Entity, key string) (model.Value, bool) {
	for _, kv := range e.Attributes {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	for _, kv := range e.Identity {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return model.Value{}, false
}

func vendorDisqualifies(group map[model.EntityID]model.Entity) bool {
	entities := make([]model.Entity, 0, len(group))
	for _, e := range group {
		entities = append(entities, e)
	}

	for i := 0; i < len(entities); i++ {
		vendorI := normalizeVendor(getVendor(entities[i]))
		if vendorI == "" {
			continue
		}
		for j := i + 1; j < len(entities); j++ {
			vendorJ := normalizeVendor(getVendor(entities[j]))
			if vendorJ == "" {
				continue
			}
			if vendorI != vendorJ {
				return true
			}
		}
	}
	return false
}

func getVendor(e model.Entity) string {
	value, ok := findAttr(e, attrHWVendor)
	if !ok {
		return ""
	}
	if value.Kind() == model.KindString {
		return value.Str()
	}
	return value.Display()
}

func normalizeVendor(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
