package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toise-dev/toise/internal/model"
)

// --- telemetry_keys ---

// joinKeys is the curated set of OTel attributes that telemetry backends
// commonly carry, making them usable as exact join keys between a Toise entity
// and the entity's metrics and logs.
var joinKeys = map[string]struct{}{
	"host.id":                 {},
	"host.name":               {},
	"service.instance.id":     {},
	"service.name":            {},
	"service.namespace":       {},
	"network.device.id":       {},
	"db.instance.id":          {},
	"interface.name":          {},
	"process.pid":             {},
	"process.executable.name": {},
	"container.id":            {},
	"k8s.pod.uid":             {},
	"k8s.pod.name":            {},
	"k8s.namespace.name":      {},
}

// joinKeyNotes carries the caveat an LLM needs to use a key correctly.
var joinKeyNotes = map[string]string{
	"process.pid":  "ephemeral: changes on every process restart; correlate within a time window, never as a stable identity",
	"host.name":    "a name, not an identity: prefer host.id when both are present",
	"service.name": "shared by every instance of the service: combine with service.instance.id to isolate one",
	"k8s.pod.name": "recreated pods change name: prefer k8s.pod.uid when both are present",
	// A remote target has no resource of its own — the producer's resource
	// describes the machine it polled from — so its identity rides each
	// datapoint instead. It is therefore absent from any label set derived by
	// flattening resource attributes.
	"network.device.id": "identifies a polled remote device: carried per datapoint, never on the producer's resource",
	"db.instance.id":    "identifies a polled remote database: carried per datapoint, never on the producer's resource",
}

// ownerDirection names, per relation type, the direction in which the neighbor
// OWNS this entity. It is the only kind of hop a join key may be inherited
// across: a listener gains its host's host.id by following runs_on outward, an
// interface gains it by following has_interface back to the host.
//
// Observation and peer relations are absent on purpose. Following monitors
// would return the identity of the agent watching the entity, and depends_on
// the identity of a peer it talks to — in both cases telemetry that belongs to
// something else, handed back as if it described the subject (#312).
var ownerDirection = map[string]string{
	model.RelRunsOn:       "outgoing", // process or service -> the host it runs on
	model.RelBoundTo:      "outgoing", // address -> the interface it is bound to
	model.RelListensOn:    "outgoing", // listener -> the interface it listens on
	model.RelHasInterface: "incoming", // interface <- the host that has it
	model.RelHasRoute:     "incoming", // route <- the device that holds it
}

// TelemetryKeysInput names the entity.
type TelemetryKeysInput struct {
	EntityID string `json:"entity_id" jsonschema:"the entity whose telemetry join keys to derive"`
	AsOf     string `json:"as_of,omitempty" jsonschema:"RFC 3339 instant: derive the keys from the graph as it was then (event-time), instead of now"`
}

// TelemetryKey is one attribute usable to find the entity's metrics and logs.
type TelemetryKey struct {
	Key         string `json:"key" jsonschema:"the OTel resource attribute name, e.g. host.id"`
	Value       string `json:"value"`
	MetricLabel string `json:"metric_label" jsonschema:"the key as Prometheus-style backends usually spell it (dots flattened to underscores); log pipelines usually keep the dotted form"`
	Source      string `json:"source" jsonschema:"where the key comes from: identity, attribute, or a related entity reached via a relation"`
	Note        string `json:"note,omitempty" jsonschema:"caveat for using this key correctly"`
}

// TelemetryKeysOutput carries the join keys and how to use them.
type TelemetryKeysOutput struct {
	Graph    GraphMeta      `json:"graph" jsonschema:"what the answering graph holds and how fresh it is; read this before treating absence as fact"`
	Entity   Entity         `json:"entity"`
	Keys     []TelemetryKey `json:"keys"`
	Guidance string         `json:"guidance" jsonschema:"how to apply the keys against metric and log backends"`
}

func (s *Server) telemetryKeys(ctx context.Context, _ *mcpsdk.CallToolRequest, in TelemetryKeysInput) (*mcpsdk.CallToolResult, TelemetryKeysOutput, error) {
	if in.EntityID == "" {
		return nil, TelemetryKeysOutput{}, fmt.Errorf("an entity_id is required")
	}
	g, err := s.graphAt(ctx, in.AsOf)
	if err != nil {
		return nil, TelemetryKeysOutput{}, err
	}
	ent, ok, deleted := g.GetEntity(model.EntityID(in.EntityID))
	if !ok {
		return nil, TelemetryKeysOutput{}, fmt.Errorf("no entity found with id %q; use find_entities to discover ids", in.EntityID)
	}

	out := TelemetryKeysOutput{Entity: entityOut(ent, deleted)}
	seen := make(map[string]struct{})
	collect := func(kvs []model.KeyValue, source string) {
		for _, kv := range kvs {
			if _, isJoin := joinKeys[kv.Key]; !isJoin {
				continue
			}
			if _, dup := seen[kv.Key]; dup {
				continue
			}
			seen[kv.Key] = struct{}{}
			val, _ := valueString(kv.Value)
			out.Keys = append(out.Keys, TelemetryKey{
				Key:         kv.Key,
				Value:       val,
				MetricLabel: strings.ReplaceAll(kv.Key, ".", "_"),
				Source:      source,
				Note:        joinKeyNotes[kv.Key],
			})
		}
	}
	collect(ent.Identity, "identity")
	collect(ent.Attributes, "attribute")

	// Context enrichment: the entity's own keys often only narrow within a
	// host or device — walk one hop to the entity that OWNS this one and
	// inherit its join keys (a listener gains its host's host.id via runs_on).
	// Only ownership hops qualify; see ownerDirection.
	edges := edgesOf(g, model.EntityID(in.EntityID), "")
	for i := range edges {
		if ownerDirection[edges[i].rel.Type] != edges[i].direction {
			continue
		}
		other, ok, deleted := g.GetEntity(edges[i].other)
		if !ok || deleted {
			continue
		}
		source := fmt.Sprintf("%s %s via %s", other.Type, edges[i].direction, edges[i].rel.Type)
		collect(other.Identity, source)
		collect(other.Attributes, source)
	}

	sort.SliceStable(out.Keys, func(i, j int) bool { return out.Keys[i].Key < out.Keys[j].Key })
	switch {
	case len(out.Keys) == 0:
		out.Guidance = "No telemetry join key on this entity or on the entity that owns it. Some " +
			"types legitimately have none: an address or a route is a graph fact with no telemetry " +
			"of its own. Read this as \"none exists\", not as \"not found yet\" — locating anything " +
			"for it would need out-of-band knowledge (port or name conventions)."
	default:
		out.Guidance = "Filter the telemetry backend by these keys. Whether the join holds depends " +
			"on how the telemetry reached the backend: it is guaranteed on the OTLP rail, where " +
			"these travel as resource attributes or per-datapoint attributes; on a Prometheus-family " +
			"backend it holds only where the collector was configured to flatten resource attributes " +
			"into labels, which is a deployment choice and not a property of the wire; on a producer's " +
			"own scrape endpoint there is no resource at all, so resource-borne keys are simply " +
			"absent. Prometheus-family backends flatten dotted names to underscores (use " +
			"metric_label); log pipelines keep the dotted form (use key). Values must match exactly " +
			"— a join that almost matches silently returns nothing."
	}
	out.Graph = s.graphMeta(g, in.AsOf)
	return nil, out, nil
}
