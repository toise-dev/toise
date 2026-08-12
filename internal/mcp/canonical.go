package mcp

import (
	"sort"

	"github.com/toise-dev/toise/internal/canonical"
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
	members, links := canonical.Walk(g, root, s.idThr)
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
	out := make([]SameAsLink, len(links))
	for i, l := range links {
		out[i] = SameAsLink{From: l.From, To: l.To, Confidence: l.Confidence, Basis: l.Basis}
	}
	return &CanonicalGroup{Aliases: aliases, Links: out}
}

// canonicalMemberIDs returns the entity's canonical group as ids — itself plus
// every entity reachable over same_as edges at/above the threshold, transitively.
// A single-element slice (just root) means no qualifying alias.
func (s *Server) canonicalMemberIDs(g Graph, root model.EntityID) []model.EntityID {
	members, _ := canonical.Walk(g, root, s.idThr)
	return members
}
