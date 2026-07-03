// Package conformance validates producer output against the Toise entity-event
// wire contract, without a running Toise: feed it the plog.Logs your producer
// emits (or the bytes of an ExportRequest) and fix every Problem it returns.
// A producer that passes never trips Toise's per-record rejection for shape
// reasons. Type-registry membership is enforced separately: under the default
// strict vocabulary an entity.type outside the registry is still rejected per
// record, unless the deployment opts into accept_unknown_types (#141).
// Problems marked Advisory are not rejections; they flag misconfigurations
// that degrade consumer behavior.
//
// The checked-in fixture (testdata/fixture_v1.bin) is the published contract
// v1: the toise-emit SDK reproduces it byte for byte, and Toise's own ingest
// tests accept it with zero rejections — one artifact pins both sides.
package conformance

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// Problem is one contract violation, with where it was found.
type Problem struct {
	Record string // which record, e.g. "resourceLogs[0].scopeLogs[0].logRecords[2]"
	Issue  string
	// Advisory marks a problem that does not cause per-record rejection but
	// flags a producer misconfiguration that degrades consumer behavior (e.g.
	// a missing service.instance.id collapses multi-producer liveness
	// reference counting, ADR 0019).
	Advisory bool
}

func (p Problem) String() string {
	if p.Advisory {
		return p.Record + ": advisory: " + p.Issue
	}
	return p.Record + ": " + p.Issue
}

// Check validates every entity-event record in ld against the wire contract.
// Records that are not entity events (no entity.state/entity.delete EventName)
// are ignored — Toise ignores them too. An empty result means conformant.
func Check(ld plog.Logs) []Problem {
	var out []Problem
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		entityEvents := false
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				where := fmt.Sprintf("resourceLogs[%d].scopeLogs[%d].logRecords[%d]", i, j, k)
				out = append(out, checkRecord(where, lr)...)
				if name := lr.EventName(); name == wire.EventEntityState || name == wire.EventEntityDelete {
					entityEvents = true
				}
			}
		}
		if entityEvents {
			out = append(out, checkResource(i, rls.At(i).Resource())...)
		}
	}
	return out
}

// checkResource flags producer-identity misconfigurations on the Resource of a
// ResourceLogs that carries entity events. Advisory only: Toise accepts the
// records, but liveness is reference-counted per producer via
// service.instance.id (ADR 0019), and without a stable instance id every
// producer collapses into one anonymous reference.
func checkResource(i int, res pcommon.Resource) []Problem {
	if v, ok := res.Attributes().Get(wire.ResServiceInstanceID); ok && v.Type() == pcommon.ValueTypeStr && v.Str() != "" {
		return nil
	}
	return []Problem{{
		Record:   fmt.Sprintf("resourceLogs[%d].resource", i),
		Issue:    "missing or empty service.instance.id — liveness is reference-counted per producer (ADR 0019); set a stable instance id or all producers share one reference",
		Advisory: true,
	}}
}

func checkRecord(where string, lr plog.LogRecord) []Problem {
	name := lr.EventName()
	if name != wire.EventEntityState && name != wire.EventEntityDelete {
		if name == "" {
			return nil // ordinary log record; not an entity event
		}
		return nil // other event families pass through Toise untouched
	}
	var out []Problem
	bad := func(format string, args ...any) {
		out = append(out, Problem{Record: where, Issue: fmt.Sprintf(format, args...)})
	}
	attrs := lr.Attributes()

	typ, ok := attrs.Get(wire.AttrEntityType)
	switch {
	case !ok:
		bad("missing entity.type")
	case typ.Type() != pcommon.ValueTypeStr:
		bad("entity.type must be a string, got %s", typ.Type())
	case typ.Str() == "":
		bad("entity.type is empty")
	}

	id, ok := attrs.Get(wire.AttrEntityID)
	switch {
	case !ok:
		bad("missing entity.id")
	case id.Type() != pcommon.ValueTypeMap:
		bad("entity.id must be a map, got %s", id.Type())
	case id.Map().Len() == 0:
		bad("entity.id is empty — identity is required")
	default:
		checkScalarMap(&out, where, "entity.id", id.Map())
	}

	if desc, ok := attrs.Get(wire.AttrEntityDescription); ok {
		if desc.Type() != pcommon.ValueTypeMap {
			bad("entity.description must be a map, got %s", desc.Type())
		} else {
			checkScalarMap(&out, where, "entity.description", desc.Map())
		}
	}

	if iv, ok := attrs.Get(wire.AttrEntityReportInterval); ok {
		if iv.Type() != pcommon.ValueTypeInt {
			bad("entity.report.interval must be an int (seconds), got %s — a mis-typed interval silently disarms the liveness backstop", iv.Type())
		} else if iv.Int() < 0 {
			bad("entity.report.interval is negative")
		}
	}

	if rels, ok := attrs.Get(wire.AttrEntityRelationships); ok {
		if name == wire.EventEntityDelete {
			bad("entity.relationships on an entity.delete record is ignored by consumers; emit them on entity.state")
		}
		if rels.Type() != pcommon.ValueTypeSlice {
			bad("entity.relationships must be a slice, got %s", rels.Type())
		} else {
			sl := rels.Slice()
			for i := 0; i < sl.Len(); i++ {
				el := sl.At(i)
				rw := fmt.Sprintf("%s.entity.relationships[%d]", where, i)
				if el.Type() != pcommon.ValueTypeMap {
					out = append(out, Problem{Record: rw, Issue: fmt.Sprintf("descriptor must be a map, got %s", el.Type())})
					continue
				}
				m := el.Map()
				if v, ok := m.Get(wire.RelType); !ok || v.Type() != pcommon.ValueTypeStr || v.Str() == "" {
					out = append(out, Problem{Record: rw, Issue: "missing or non-string relationship.type"})
				}
				if v, ok := m.Get(wire.RelTargetType); !ok || v.Type() != pcommon.ValueTypeStr || v.Str() == "" {
					out = append(out, Problem{Record: rw, Issue: "missing or non-string target entity.type"})
				}
				if v, ok := m.Get(wire.RelTargetID); !ok || v.Type() != pcommon.ValueTypeMap || v.Map().Len() == 0 {
					out = append(out, Problem{Record: rw, Issue: "missing, non-map, or empty target entity.id"})
				}
				if rt, _ := m.Get(wire.RelType); rt.Str() == wire.RelTypeSameAs {
					out = append(out, checkSameAsBelief(rw, m)...)
				}
			}
		}
	}
	return out
}

// checkSameAsBelief advises on a same_as edge's belief attributes. A same_as
// edge exists to feed the read-time canonical overlay (ADR 0020), which collapses
// only edges whose confidence is a number in [0,1] at or above the threshold. A
// missing or out-of-range confidence is not a rejection — Toise stores the edge —
// but the overlay ignores it, so the belief is inert. Advisory, so a producer
// catches a same_as that will never merge anything.
func checkSameAsBelief(where string, m pcommon.Map) []Problem {
	v, ok := m.Get(wire.RelConfidence)
	if !ok {
		return []Problem{{Record: where, Issue: "same_as edge has no confidence; the canonical overlay collapses only edges with a confidence in [0,1], so this belief is inert", Advisory: true}}
	}
	var c float64
	switch v.Type() {
	case pcommon.ValueTypeDouble:
		c = v.Double()
	case pcommon.ValueTypeInt:
		c = float64(v.Int())
	default:
		return []Problem{{Record: where, Issue: fmt.Sprintf("same_as confidence is %s; it must be a number in [0,1] or the canonical overlay ignores the belief", v.Type()), Advisory: true}}
	}
	if c < 0 || c > 1 {
		return []Problem{{Record: where, Issue: fmt.Sprintf("same_as confidence %g is outside [0,1]; the canonical overlay ignores it", c), Advisory: true}}
	}
	return nil
}

// checkScalarMap flags empty keys and non-scalar values: the contract is flat
// scalar maps with non-empty keys. Toise rejects an empty key in every mode
// (strict and accept_unknown_types alike) and drops non-scalar values with a
// warning.
func checkScalarMap(out *[]Problem, where, field string, m pcommon.Map) {
	m.Range(func(k string, v pcommon.Value) bool {
		if k == "" {
			*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf("%s has an empty attribute key; Toise rejects it in every mode", field)})
		}
		switch v.Type() {
		case pcommon.ValueTypeStr, pcommon.ValueTypeInt, pcommon.ValueTypeDouble, pcommon.ValueTypeBool:
		default:
			*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf("%s.%s is %s; values must be scalar (string, int, double, bool)", field, k, v.Type())})
		}
		return true
	})
}
