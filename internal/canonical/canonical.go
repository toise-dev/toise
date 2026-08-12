// Package canonical holds the read-time identity overlay of ADR 0020: the walk
// over producer-asserted same_as belief edges that says which entities are
// believed to be the same real thing.
//
// It lives in its own package because two read surfaces need it — MCP and
// GraphQL — and a second spelling of a traversal is how two surfaces come to
// disagree about what the graph says. The collapse is NEVER written back: the
// exact entities and any low-confidence or conflicting evidence stay intact
// (ADR 0018, ADR 0022). Toise records the belief; only the grouping is derived.
package canonical

import (
	"sort"

	"github.com/toise-dev/toise/internal/model"
)

// ConfidenceKey is the same_as edge attribute carrying the producer's stated
// probability that the two endpoints are the same real thing.
const ConfidenceKey = "confidence"

// BasisKey names the evidence that justifies the belief, e.g. "hyperv-kvp" or
// "serial_match".
const BasisKey = "basis"

// DefaultThreshold is the confidence at or above which an alias is believed
// strongly enough to collapse. Deliberately high: a wrong merge is far more
// expensive to a consumer than a missing one, because it produces a confident
// answer about the wrong machine instead of an obvious gap.
const DefaultThreshold = 0.9

// Graph is the projection subset the walk reads.
type Graph interface {
	GetEntity(id model.EntityID) (model.Entity, bool, bool)
	RelationsTouching(id model.EntityID, relType string) []model.Relation
}

// Link is one supporting same_as edge with its provenance.
type Link struct {
	From       string
	To         string
	Confidence float64
	Basis      string
}

// Walk returns the canonical group of root — itself first, then every entity
// reachable over same_as edges at or above threshold, transitively — and the
// edges that justify it, sorted by endpoint. A single-element result means no
// qualifying alias.
//
// An edge whose confidence is absent, non-numeric or outside [0,1] does not
// collapse anything: an unreadable belief is treated as no belief rather than as
// a weak one, so a malformed value can never merge two machines.
func Walk(g Graph, root model.EntityID, threshold float64) ([]model.EntityID, []Link) {
	seen := map[model.EntityID]bool{root: true}
	seenLink := map[model.RelationID]bool{}
	members := []model.EntityID{root}
	queue := []model.EntityID{root}
	var links []Link

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, r := range g.RelationsTouching(cur, model.RelSameAs) {
			conf, ok := Confidence(r)
			if !ok || conf < threshold {
				continue
			}
			if !seenLink[r.ID] {
				seenLink[r.ID] = true
				links = append(links, Link{
					From: string(r.From), To: string(r.To),
					Confidence: conf, Basis: Basis(r),
				})
			}
			other := r.To
			if other == cur {
				other = r.From
			}
			if seen[other] {
				continue
			}
			seen[other] = true
			members = append(members, other)
			queue = append(queue, other)
		}
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].From != links[j].From {
			return links[i].From < links[j].From
		}
		return links[i].To < links[j].To
	})
	return members, links
}

// Confidence reads the edge's confidence as a number in [0,1]. ok is false when
// it is absent, non-numeric or out of range: per ADR 0022 the value is stored
// exactly as the producer sent it, and validated here, at the moment it is used.
func Confidence(r model.Relation) (float64, bool) {
	for _, kv := range r.Attributes {
		if kv.Key != ConfidenceKey {
			continue
		}
		switch kv.Value.Kind() {
		case model.KindDouble:
			c := kv.Value.Double()
			return c, c >= 0 && c <= 1
		case model.KindInt:
			c := float64(kv.Value.Int())
			return c, c >= 0 && c <= 1
		default:
			return 0, false
		}
	}
	return 0, false
}

// Basis returns the edge's stated evidence, or "" when it carries none. It
// renders with Display, not String: String is the type-tagged encoding used for
// identity hashing, which would surface a basis of "hyperv-kvp" as "s:hyperv-kvp".
func Basis(r model.Relation) string {
	for _, kv := range r.Attributes {
		if kv.Key == BasisKey {
			return kv.Value.Display()
		}
	}
	return ""
}
