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
	// State says whether this member is still there, and if not, in which way:
	// live, deleted, or unresolvable. Until 0.23 all three answered with an empty
	// type and label, so a consumer could not tell a retracted alias from one this
	// graph never held — and the walk follows same_as edges without checking their
	// far end, so both really happen. Same vocabulary as GraphQL's
	// CanonicalMemberState, so the two surfaces answer with the same words.
	State string `json:"state" jsonschema:"live (held and renderable), deleted (held as a tombstone: it existed, was removed, and the belief still names it), or unresolvable (the belief names an id this graph holds no entity for). Read it before treating an alias as a usable entity: type and label are empty unless state is live"`
}

// SameAsLink is one supporting same_as belief edge, with its provenance.
type SameAsLink struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Confidence float64 `json:"confidence"`
	Basis      string  `json:"basis,omitempty"`
}

// InertBelief is a same_as edge a producer asserted on this entity that the
// overlay could not use, with the reason. It exists because a group of one and
// a group whose only evidence was unreadable are otherwise the SAME answer: the
// producer believes it published an identity join, the consumer reads two
// separate entities, and nothing says so (#544).
//
// The edge is stored exactly as sent (ADR 0022) and is reported here, not
// rejected: a malformed value must never merge two machines, and discarding it
// would lose evidence a later threshold change could use.
type InertBelief struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Basis  string `json:"basis,omitempty" jsonschema:"the evidence the producer stated, when it stated one — the half that stays useful even when the confidence does not"`
	Reason string `json:"reason" jsonschema:"why the overlay could not use this belief: no_confidence, confidence_not_numeric, or confidence_out_of_range"`
}

// CanonicalGroup is the read-time identity overlay (ADR 0020): the entities a set
// of high-confidence same_as edges asserts are the same real thing as the one
// fetched, plus the edges that justify it. It is NEVER stored — recomputed per
// query from the producer-asserted belief edges, so the exact entities and any
// low-confidence/conflicting evidence stay intact (ADR 0018/0022).
type CanonicalGroup struct {
	Aliases []CanonicalMember `json:"aliases" jsonschema:"other entities asserted to be the same real thing as this one (transitive, at/above the confidence threshold)"`
	Links   []SameAsLink      `json:"links" jsonschema:"the same_as belief edges supporting the group, with confidence and basis"`
	Inert   []InertBelief     `json:"inert,omitempty" jsonschema:"same_as edges asserted on this entity that the overlay could NOT use, with the reason. A producer stated an identity join here and it groups nothing: read this before concluding the entity has no alias, because an empty aliases list and an unreadable belief are otherwise the same answer"`
}

// canonicalGroup walks same_as edges with confidence >= threshold out from root,
// transitively, and returns the other group members plus the supporting links.
//
// Returns nil only when NOTHING is to be said: no qualifying alias AND no belief
// the overlay had to ignore. A block carrying only `inert` is the case this
// function used to answer with nil, which made "no alias" and "an alias I could
// not read" the same answer (#544).
func (s *Server) canonicalGroup(g Graph, root model.EntityID) *CanonicalGroup {
	members, links := canonical.Walk(g, root, s.idThr)
	inert := inertBeliefs(g, root)
	if len(members) <= 1 {
		// No qualifying alias. That is a real answer — unless a producer asserted
		// one the overlay could not read, in which case saying "no alias" is the
		// wrong answer and this is the only place it can be said.
		if len(inert) == 0 {
			return nil
		}
		return &CanonicalGroup{Inert: inert}
	}
	var aliases []CanonicalMember
	for _, id := range members {
		if id == root {
			continue
		}
		switch ent, ok, deleted := g.GetEntity(id); {
		case ok && !deleted:
			aliases = append(aliases, CanonicalMember{ID: string(id), Type: ent.Type, Label: label(ent), State: "live"})
		case ok:
			aliases = append(aliases, CanonicalMember{ID: string(id), State: "deleted"})
		default:
			aliases = append(aliases, CanonicalMember{ID: string(id), State: "unresolvable"})
		}
	}
	if len(aliases) == 0 {
		if len(inert) == 0 {
			return nil
		}
		return &CanonicalGroup{Inert: inert}
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].ID < aliases[j].ID })
	out := make([]SameAsLink, len(links))
	for i, l := range links {
		out[i] = SameAsLink{From: l.From, To: l.To, Confidence: l.Confidence, Basis: l.Basis}
	}
	return &CanonicalGroup{Aliases: aliases, Links: out, Inert: inert}
}

// inertBeliefs lists the same_as edges touching root whose confidence the
// overlay cannot use, with the reason, sorted for a stable answer.
//
// The reasons are the same three the ingest counter and the conformance kit
// report, in the same words, so one defect is never described three ways. The
// classification is derived from canonical.Confidence rather than re-implemented:
// a second copy of the rule is how a counter and a reader come to disagree about
// the same edge.
func inertBeliefs(g Graph, root model.EntityID) []InertBelief {
	var out []InertBelief
	for _, r := range g.RelationsTouching(root, model.RelSameAs) {
		if _, ok := canonical.Confidence(r); ok {
			continue // readable; weak or strong is the threshold's business
		}
		out = append(out, InertBelief{
			From:   string(r.From),
			To:     string(r.To),
			Basis:  canonical.Basis(r),
			Reason: inertReason(r),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// inertReason names which of the three unusable shapes an edge has. It reads the
// attribute directly because canonical.Confidence deliberately collapses all
// three into one "not usable" answer — the distinction matters to the producer
// that has to fix it, not to the overlay that has to ignore it.
func inertReason(r model.Relation) string {
	for _, kv := range r.Attributes {
		if kv.Key != canonical.ConfidenceKey {
			continue
		}
		switch kv.Value.Kind() {
		case model.KindDouble, model.KindInt:
			return "confidence_out_of_range"
		default:
			return "confidence_not_numeric"
		}
	}
	return "no_confidence"
}

// canonicalMemberIDs returns the entity's canonical group as ids — itself plus
// every entity reachable over same_as edges at/above the threshold, transitively.
// A single-element slice (just root) means no qualifying alias.
func (s *Server) canonicalMemberIDs(g Graph, root model.EntityID) []model.EntityID {
	members, _ := canonical.Walk(g, root, s.idThr)
	return members
}
