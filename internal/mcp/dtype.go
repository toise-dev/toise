package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
)

// --- describe_type ---

// keyUsageSample caps how many entities of the type are scanned for observed
// attribute keys: enough to be representative, bounded so a huge type cannot
// blow the call budget.
const keyUsageSample = 500

// maxTypeSamples is how many example labels the output carries.
const maxTypeSamples = 5

// DescribeTypeInput names the type to zoom on.
type DescribeTypeInput struct {
	Type string `json:"type" jsonschema:"the entity or relation type to describe, e.g. host or runs_on"`
	AsOf string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: describe the type as the graph was then (event-time), instead of now"`
}

// AttributeUsage is one observed attribute key with how often it appears and
// one example value.
type AttributeUsage struct {
	Key     string `json:"key"`
	Seen    int    `json:"seen" jsonschema:"entities (within the sample) carrying this key"`
	Example string `json:"example" jsonschema:"one observed value, to show the shape"`
}

// RelationParticipation is one relation type this entity type participates in,
// with the empirically observed peer types.
type RelationParticipation struct {
	RelationType string      `json:"relation_type"`
	Direction    string      `json:"direction" jsonschema:"outgoing if entities of this type are the source, incoming if the target"`
	Count        int         `json:"count"`
	PeerTypes    []TypeCount `json:"peer_types" jsonschema:"the types observed at the other end, with counts"`
}

// EndpointShape is one observed (from, to) type pair for a relation type.
type EndpointShape struct {
	FromType string `json:"from_type"`
	ToType   string `json:"to_type"`
	Count    int    `json:"count"`
}

// DescribeTypeOutput zooms on one type: its registration, its observed shape,
// and how it connects.
type DescribeTypeOutput struct {
	Kind        string `json:"kind" jsonschema:"entity, relation, or unknown"`
	Type        string `json:"type"`
	Registered  bool   `json:"registered" jsonschema:"true if the type is in the built-in registry"`
	Count       int    `json:"count" jsonschema:"live instances in the graph"`
	Description string `json:"description" jsonschema:"a natural-language summary"`

	// Entity kinds.
	IdentityKeys  []AttributeUsage        `json:"identity_keys,omitempty" jsonschema:"observed identifying attribute keys (sampled)"`
	AttributeKeys []AttributeUsage        `json:"attribute_keys,omitempty" jsonschema:"observed descriptive attribute keys (sampled)"`
	Relations     []RelationParticipation `json:"relations,omitempty" jsonschema:"how this type connects, empirically"`
	Samples       []string                `json:"samples,omitempty" jsonschema:"a few example entity labels"`

	// Relation kinds.
	Structural     bool            `json:"structural,omitempty" jsonschema:"whether appearance/disappearance of this relation is alert-worthy"`
	ImpactFlow     string          `json:"impact_flow,omitempty" jsonschema:"how failure propagates across it: to_from, from_to, or both"`
	EndpointShapes []EndpointShape `json:"endpoint_shapes,omitempty" jsonschema:"the (from, to) entity-type pairs observed, with counts"`
}

func (s *Server) describeType(ctx context.Context, _ *mcpsdk.CallToolRequest, in DescribeTypeInput) (*mcpsdk.CallToolResult, DescribeTypeOutput, error) {
	if in.Type == "" {
		return nil, DescribeTypeOutput{}, fmt.Errorf("a type is required; describe_schema lists the types present")
	}
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, DescribeTypeOutput{}, err
	}

	if rels := g.ListRelations(in.Type, "", ""); len(rels) > 0 || isRegisteredRelation(in.Type) {
		return nil, describeRelationType(g, in.Type, rels), nil
	}
	ents := g.ListEntities(in.Type)
	if len(ents) > 0 || model.IsKnownEntityType(in.Type) {
		return nil, describeEntityType(g, in.Type, ents), nil
	}
	return nil, DescribeTypeOutput{}, fmt.Errorf("type %q is neither a known type nor present in the graph; describe_schema lists what exists", in.Type)
}

func isRegisteredRelation(t string) bool {
	_, ok := model.RelationDef(t)
	return ok
}

func describeEntityType(g Graph, typ string, ents []model.Entity) DescribeTypeOutput {
	out := DescribeTypeOutput{Kind: "entity", Type: typ, Registered: model.IsKnownEntityType(typ), Count: len(ents)}

	sample := ents
	if len(sample) > keyUsageSample {
		sample = sample[:keyUsageSample]
	}
	idUse, attrUse := map[string]*AttributeUsage{}, map[string]*AttributeUsage{}
	record := func(m map[string]*AttributeUsage, kvs []model.KeyValue) {
		for _, kv := range kvs {
			u := m[kv.Key]
			if u == nil {
				val, _ := valueString(kv.Value)
				u = &AttributeUsage{Key: kv.Key, Example: val}
				m[kv.Key] = u
			}
			u.Seen++
		}
	}
	for i := range sample {
		record(idUse, sample[i].Identity)
		record(attrUse, sample[i].Attributes)
	}
	out.IdentityKeys = sortedUsage(idUse)
	out.AttributeKeys = sortedUsage(attrUse)

	// Empirical relation participation: which relation types touch this type,
	// in which direction, with which peers.
	type partKey struct{ rel, dir string }
	parts := map[partKey]map[string]int{}
	isType := map[model.EntityID]bool{}
	for i := range ents {
		isType[ents[i].ID] = true
	}
	for _, r := range g.ListRelations("", "", "") {
		fromEnt, fok, _ := g.GetEntity(r.From)
		toEnt, tok, _ := g.GetEntity(r.To)
		if !fok || !tok {
			continue
		}
		if isType[r.From] {
			k := partKey{r.Type, "outgoing"}
			if parts[k] == nil {
				parts[k] = map[string]int{}
			}
			parts[k][toEnt.Type]++
		}
		if isType[r.To] {
			k := partKey{r.Type, "incoming"}
			if parts[k] == nil {
				parts[k] = map[string]int{}
			}
			parts[k][fromEnt.Type]++
		}
	}
	for k, peers := range parts {
		total := 0
		for _, n := range peers {
			total += n
		}
		out.Relations = append(out.Relations, RelationParticipation{
			RelationType: k.rel, Direction: k.dir, Count: total, PeerTypes: sortedCounts(peers),
		})
	}
	sort.Slice(out.Relations, func(i, j int) bool {
		if out.Relations[i].Count != out.Relations[j].Count {
			return out.Relations[i].Count > out.Relations[j].Count
		}
		if out.Relations[i].RelationType != out.Relations[j].RelationType {
			return out.Relations[i].RelationType < out.Relations[j].RelationType
		}
		return out.Relations[i].Direction < out.Relations[j].Direction
	})

	for i := 0; i < len(ents) && i < maxTypeSamples; i++ {
		out.Samples = append(out.Samples, label(ents[i]))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%q is an entity type", typ)
	if !out.Registered {
		b.WriteString(" (not in the built-in registry)")
	}
	fmt.Fprintf(&b, " with %d live instances", out.Count)
	if len(out.IdentityKeys) > 0 {
		fmt.Fprintf(&b, ", identified by %s", joinUsageKeys(out.IdentityKeys))
	}
	if len(out.Relations) > 0 {
		fmt.Fprintf(&b, "; it participates in %d relation shapes (largest: %s %s, %d edges)",
			len(out.Relations), out.Relations[0].RelationType, out.Relations[0].Direction, out.Relations[0].Count)
	}
	b.WriteString(".")
	out.Description = b.String()
	return out
}

func describeRelationType(g Graph, typ string, rels []model.Relation) DescribeTypeOutput {
	out := DescribeTypeOutput{Kind: "relation", Type: typ, Count: len(rels)}
	if def, ok := model.RelationDef(typ); ok {
		out.Registered = true
		out.Structural = def.Structural
		switch def.Impact {
		case model.ImpactToFrom:
			out.ImpactFlow = "to_from"
		case model.ImpactFromTo:
			out.ImpactFlow = "from_to"
		case model.ImpactNone:
			out.ImpactFlow = "none"
		default:
			out.ImpactFlow = "both"
		}
	} else {
		out.ImpactFlow = "both" // the conservative default unregistered types get
	}

	shapes := map[[2]string]int{}
	for i := range rels {
		fromEnt, fok, _ := g.GetEntity(rels[i].From)
		toEnt, tok, _ := g.GetEntity(rels[i].To)
		if !fok || !tok {
			continue
		}
		shapes[[2]string{fromEnt.Type, toEnt.Type}]++
	}
	for pair, n := range shapes {
		out.EndpointShapes = append(out.EndpointShapes, EndpointShape{FromType: pair[0], ToType: pair[1], Count: n})
	}
	sort.Slice(out.EndpointShapes, func(i, j int) bool {
		if out.EndpointShapes[i].Count != out.EndpointShapes[j].Count {
			return out.EndpointShapes[i].Count > out.EndpointShapes[j].Count
		}
		return out.EndpointShapes[i].FromType+out.EndpointShapes[i].ToType < out.EndpointShapes[j].FromType+out.EndpointShapes[j].ToType
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%q is a relation type", typ)
	if !out.Registered {
		b.WriteString(" (not in the built-in registry)")
	}
	fmt.Fprintf(&b, " with %d live edges", out.Count)
	if len(out.EndpointShapes) > 0 {
		parts := make([]string, 0, len(out.EndpointShapes))
		for _, sh := range out.EndpointShapes {
			parts = append(parts, fmt.Sprintf("%s->%s (%d)", sh.FromType, sh.ToType, sh.Count))
		}
		fmt.Fprintf(&b, ", observed as %s", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "; failure propagates %s.", strings.ReplaceAll(out.ImpactFlow, "_", "->"))
	out.Description = b.String()
	return out
}

func sortedUsage(m map[string]*AttributeUsage) []AttributeUsage {
	out := make([]AttributeUsage, 0, len(m))
	for _, u := range m {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seen != out[j].Seen {
			return out[i].Seen > out[j].Seen
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func joinUsageKeys(us []AttributeUsage) string {
	keys := make([]string, 0, len(us))
	for _, u := range us {
		keys = append(keys, u.Key)
	}
	return strings.Join(keys, " + ")
}
