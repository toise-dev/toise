package mcp

import (
	"sort"

	"github.com/toise-dev/toise/internal/model"
)

// CanonicalMember is one entity in a canonical (same-real-thing) group.
type CanonicalMember struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// SameAsLink is one supporting same_as belief edge, with its provenance.
type SameAsLink struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Confidence float64 `json:"confidence"`
	Basis      string  `json:"basis,omitempty"`
}

// CanonicalGroup is the read-time identity overlay (ADR 0020): the entities a set
// of high-confidence same_as edges asserts are the same real thing as the one
// fetched, plus the edges that justify it. It is NEVER stored — recomputed per
// query from the producer-asserted belief edges, so the exact entities and any
// low-confidence/conflicting evidence stay intact (ADR 0018/0022).
type CanonicalGroup struct {
	Aliases []CanonicalMember `json:"aliases" jsonschema:"other entities asserted to be the same real thing as this one (transitive, at/above the confidence threshold)"`
	Links   []SameAsLink      `json:"links" jsonschema:"the same_as belief edges supporting the group, with confidence and basis"`
}

// canonicalGroup walks same_as edges with confidence >= threshold out from root,
// transitively, and returns the other group members plus the supporting links.
// Returns nil when no qualifying same_as edge touches the entity.
func (s *Server) canonicalGroup(g Graph, root model.EntityID) *CanonicalGroup {
	members, links := s.walkSameAs(g, root)
	if len(members) <= 1 {
		return nil
	}
	var aliases []CanonicalMember
	for _, id := range members {
		if id == root {
			continue
		}
		if ent, ok, deleted := g.GetEntity(id); ok && !deleted {
			aliases = append(aliases, CanonicalMember{ID: string(id), Type: ent.Type, Label: label(ent)})
		} else {
			aliases = append(aliases, CanonicalMember{ID: string(id)})
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].ID < aliases[j].ID })
	sort.Slice(links, func(i, j int) bool {
		if links[i].From != links[j].From {
			return links[i].From < links[j].From
		}
		return links[i].To < links[j].To
	})
	return &CanonicalGroup{Aliases: aliases, Links: links}
}

// canonicalMemberIDs returns the entity's canonical group as ids — itself plus
// every entity reachable over same_as edges at/above the threshold, transitively.
// A single-element slice (just root) means no qualifying alias.
func (s *Server) canonicalMemberIDs(g Graph, root model.EntityID) []model.EntityID {
	members, _ := s.walkSameAs(g, root)
	return members
}

// walkSameAs is the shared BFS over same_as edges with confidence >= threshold.
// It returns the connected group (root first) and the supporting links.
func (s *Server) walkSameAs(g Graph, root model.EntityID) ([]model.EntityID, []SameAsLink) {
	seen := map[model.EntityID]bool{root: true}
	seenLink := map[model.RelationID]bool{}
	members := []model.EntityID{root}
	queue := []model.EntityID{root}
	var links []SameAsLink

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, r := range g.RelationsTouching(cur, model.RelSameAs) {
			conf, ok := relConfidence(r)
			if !ok || conf < s.idThr {
				continue // missing/low-confidence belief does not collapse (conservative)
			}
			if !seenLink[r.ID] {
				seenLink[r.ID] = true
				links = append(links, SameAsLink{From: string(r.From), To: string(r.To), Confidence: conf, Basis: relBasis(r)})
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
	return members, links
}

// relConfidence reads the same_as edge's confidence attribute as a number in
// [0,1]; ok is false when it is absent or not numeric (per ADR 0022 the value is
// stored as-is and only validated here, at read time, when it is consumed).
func relConfidence(r model.Relation) (float64, bool) {
	for _, kv := range r.Attributes {
		if kv.Key != "confidence" {
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

func relBasis(r model.Relation) string {
	for _, kv := range r.Attributes {
		if kv.Key == "basis" {
			return kv.Value.Display()
		}
	}
	return ""
}
