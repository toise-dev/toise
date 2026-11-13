package mcp

import (
	"github.com/toise-dev/toise/internal/model"
)

// hostAttachmentRelations is the whitelist of relations that stay on a
// single machine. Anything else (connected_to, same_as, neighbor_of, ...)
// can leave the host, so it is never followed when looking for an entity's
// host.
var hostAttachmentRelations = map[string]bool{
	"runs_on":       true,
	"has_interface": true,
	"bound_to":      true,
	"listens_on":    true,
}

// hostResolver resolves the single host an entity sits on, for digest
// examples. Ambiguity (several hosts at the same distance) resolves to an
// ambiguous ref: we never guess.
type hostResolver struct {
	g     Graph
	extra map[model.EntityID][]model.EntityID
	cache map[model.EntityID]hostRef
}

type hostRef struct {
	name        string
	fingerprint string
	ambiguous   bool
}

func newHostResolver(g Graph, events []model.Event) *hostResolver {
	h := &hostResolver{
		g:     g,
		extra: map[model.EntityID][]model.EntityID{},
		cache: map[model.EntityID]hostRef{},
	}
	// Relation events keep the link even after the live graph dropped the
	// edge: an entity deleted in the window lost its edges, but the removal
	// event still records the link (e.g. deleted listener -> listens_on ->
	// interface -> host). Only attachment relations are kept.
	for _, ev := range events {
		if ev.Relation == nil {
			continue
		}
		rel := ev.Relation.Relation
		if !hostAttachmentRelations[rel.Type] {
			continue
		}
		h.extra[rel.From] = append(h.extra[rel.From], rel.To)
		h.extra[rel.To] = append(h.extra[rel.To], rel.From)
	}
	return h
}

func (h *hostResolver) hostOf(id model.EntityID, ent model.Entity) hostRef {
	if ref, ok := h.cache[id]; ok {
		return ref
	}
	ref := h.resolve(id, ent)
	h.cache[id] = ref
	return ref
}

func (h *hostResolver) resolve(id model.EntityID, ent model.Entity) hostRef {
	if ent.Type == "" {
		if e, ok, _ := h.g.GetEntity(id); ok {
			ent = e
		}
	}
	if ent.Type == "host" {
		return hostRef{}
	}
	// An identity key host.id points straight at the host. Build the host
	// fingerprint instead of a "host:host.id=..." string: a value holding
	// ',' or '=' would break that format.
	for _, kv := range ent.Identity {
		if kv.Key == "host.id" {
			fp := model.Entity{Type: "host", Identity: []model.KeyValue{{Key: "host.id", Value: kv.Value}}}.IdentityHash()
			if hid, ok := h.g.ResolveHandle(fp); ok {
				if he, ok, _ := h.g.GetEntity(hid); ok {
					return h.refOf(he)
				}
			}
			break
		}
	}
	// Breadth-first over live edges plus event edges, max depth 2. Stop at
	// the first depth holding hosts; never expand from a host.
	visited := map[model.EntityID]bool{id: true}
	frontier := []model.EntityID{id}
	for depth := 0; depth < 2 && len(frontier) > 0; depth++ {
		seen := map[model.EntityID]bool{}
		var hosts []model.Entity
		var next []model.EntityID
		for _, cur := range frontier {
			for _, n := range h.neighbors(cur) {
				if visited[n] {
					continue
				}
				visited[n] = true
				e, ok, _ := h.g.GetEntity(n)
				if !ok {
					continue
				}
				if e.Type == "host" {
					if !seen[n] {
						seen[n] = true
						hosts = append(hosts, e)
					}
					continue
				}
				next = append(next, n)
			}
		}
		if len(hosts) == 1 {
			return h.refOf(hosts[0])
		}
		if len(hosts) > 1 {
			return hostRef{ambiguous: true}
		}
		frontier = next
	}
	return hostRef{}
}

func (h *hostResolver) neighbors(id model.EntityID) []model.EntityID {
	out := make([]model.EntityID, 0, len(h.extra[id]))
	edges := edgesOf(h.g, id, "")
	for i := range edges {
		if hostAttachmentRelations[edges[i].rel.Type] {
			out = append(out, edges[i].other)
		}
	}
	out = append(out, h.extra[id]...)
	return out
}

func (h *hostResolver) refOf(e model.Entity) hostRef {
	name := ""
	for _, kv := range e.Attributes {
		if kv.Key == "host.name" && kv.Value.Display() != "" {
			name = kv.Value.Display()
			break
		}
	}
	if name == "" {
		for _, kv := range e.Identity {
			if kv.Key == "host.name" && kv.Value.Display() != "" {
				name = kv.Value.Display()
				break
			}
		}
	}
	if name == "" {
		for _, kv := range e.Identity {
			if kv.Key == "host.id" {
				name = kv.Value.Display()
				break
			}
		}
	}
	return hostRef{name: name, fingerprint: e.IdentityHash()}
}

func (h *hostResolver) forEvent(ev model.Event) (name, fingerprint string) {
	if ev.Entity != nil {
		ref := h.hostOf(ev.Entity.Entity.ID, ev.Entity.Entity)
		return ref.name, ref.fingerprint
	}
	if ev.Relation != nil {
		rel := ev.Relation.Relation
		// From wins; only fall back to To when From has no host at all.
		// An ambiguous From stays empty: never guess, never use To.
		if e, ok, _ := h.g.GetEntity(rel.From); ok && e.Type == "host" {
			ref := h.refOf(e)
			return ref.name, ref.fingerprint
		}
		if ref := h.hostOf(rel.From, model.Entity{}); ref.name != "" || ref.ambiguous {
			return ref.name, ref.fingerprint
		}
		ref := h.hostOf(rel.To, model.Entity{})
		return ref.name, ref.fingerprint
	}
	return "", ""
}
