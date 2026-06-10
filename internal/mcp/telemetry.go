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

// joinKeys is the curated set of OTel resource attributes that telemetry
// backends commonly carry, making them usable as exact join keys between a
// Toise entity and the entity's metrics and logs.
var joinKeys = map[string]struct{}{
	"host.id":                 {},
	"host.name":               {},
	"service.instance.id":     {},
	"service.name":            {},
	"service.namespace":       {},
	"network.device.id":       {},
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
}

// TelemetryKeysInput names the entity.
type TelemetryKeysInput struct {
	EntityID string `json:"entity_id" jsonschema:"the entity whose telemetry join keys to derive"`
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
	Entity   Entity         `json:"entity"`
	Keys     []TelemetryKey `json:"keys"`
	Guidance string         `json:"guidance" jsonschema:"how to apply the keys against metric and log backends"`
}

func (s *Server) telemetryKeys(_ context.Context, _ *mcpsdk.CallToolRequest, in TelemetryKeysInput) (*mcpsdk.CallToolResult, TelemetryKeysOutput, error) {
	if in.EntityID == "" {
		return nil, TelemetryKeysOutput{}, fmt.Errorf("an entity_id is required")
	}
	ent, ok, deleted := s.graph.GetEntity(model.EntityID(in.EntityID))
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
	// host or device — walk one hop and inherit the join keys of the entities
	// it is attached to (a listener gains its host's host.id via runs_on).
	edges := s.edgesOf(model.EntityID(in.EntityID), "")
	for i := range edges {
		other, ok, deleted := s.graph.GetEntity(edges[i].other)
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
		out.Guidance = "No known telemetry join keys on this entity or its direct neighbors. " +
			"Its telemetry, if any, must be located by out-of-band knowledge (port, name conventions)."
	default:
		out.Guidance = "Filter the telemetry backend by these resource attributes. Metric backends " +
			"in the Prometheus family usually flatten dotted attribute names to underscores (use " +
			"metric_label); log pipelines usually keep the dotted form (use key). Values must match " +
			"exactly — a join that almost matches silently returns nothing."
	}
	return nil, out, nil
}
