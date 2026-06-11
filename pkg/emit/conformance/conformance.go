// Package conformance validates producer output against the Toise entity-event
// wire contract, without a running Toise: feed it the plog.Logs your producer
// emits (or the bytes of an ExportRequest) and fix every Problem it returns.
// A producer that passes never trips Toise's per-record rejection.
//
// The checked-in fixture (testdata/fixture_v1.bin) is the published contract
// v1: the toise-emit SDK reproduces it byte for byte, and Toise's own ingest
// tests accept it with zero rejections — one artifact pins both sides.
package conformance

import (
	"fmt"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

// Problem is one contract violation, with where it was found.
type Problem struct {
	Record string // which record, e.g. "resourceLogs[0].scopeLogs[0].logRecords[2]"
	Issue  string
}

func (p Problem) String() string { return p.Record + ": " + p.Issue }

// Check validates every entity-event record in ld against the wire contract.
// Records that are not entity events (no entity.state/entity.delete EventName)
// are ignored — Toise ignores them too. An empty result means conformant.
func Check(ld plog.Logs) []Problem {
	var out []Problem
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				where := fmt.Sprintf("resourceLogs[%d].scopeLogs[%d].logRecords[%d]", i, j, k)
				out = append(out, checkRecord(where, recs.At(k))...)
			}
		}
	}
	return out
}

func checkRecord(where string, lr plog.LogRecord) []Problem {
	name := lr.EventName()
	if name != "entity.state" && name != "entity.delete" {
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

	typ, ok := attrs.Get("entity.type")
	switch {
	case !ok:
		bad("missing entity.type")
	case typ.Type() != pcommon.ValueTypeStr:
		bad("entity.type must be a string, got %s", typ.Type())
	case typ.Str() == "":
		bad("entity.type is empty")
	}

	id, ok := attrs.Get("entity.id")
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

	if desc, ok := attrs.Get("entity.description"); ok {
		if desc.Type() != pcommon.ValueTypeMap {
			bad("entity.description must be a map, got %s", desc.Type())
		} else {
			checkScalarMap(&out, where, "entity.description", desc.Map())
		}
	}

	if iv, ok := attrs.Get("entity.report.interval"); ok {
		if iv.Type() != pcommon.ValueTypeInt {
			bad("entity.report.interval must be an int (seconds), got %s — a mis-typed interval silently disarms the liveness backstop", iv.Type())
		} else if iv.Int() < 0 {
			bad("entity.report.interval is negative")
		}
	}

	if rels, ok := attrs.Get("entity.relationships"); ok {
		if name == "entity.delete" {
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
				if v, ok := m.Get("relationship.type"); !ok || v.Type() != pcommon.ValueTypeStr || v.Str() == "" {
					out = append(out, Problem{Record: rw, Issue: "missing or non-string relationship.type"})
				}
				if v, ok := m.Get("entity.type"); !ok || v.Type() != pcommon.ValueTypeStr || v.Str() == "" {
					out = append(out, Problem{Record: rw, Issue: "missing or non-string target entity.type"})
				}
				if v, ok := m.Get("entity.id"); !ok || v.Type() != pcommon.ValueTypeMap || v.Map().Len() == 0 {
					out = append(out, Problem{Record: rw, Issue: "missing, non-map, or empty target entity.id"})
				}
			}
		}
	}
	return out
}

// checkScalarMap flags non-scalar values: the contract is flat scalar maps,
// and Toise drops non-scalar values with a warning.
func checkScalarMap(out *[]Problem, where, field string, m pcommon.Map) {
	m.Range(func(k string, v pcommon.Value) bool {
		switch v.Type() {
		case pcommon.ValueTypeStr, pcommon.ValueTypeInt, pcommon.ValueTypeDouble, pcommon.ValueTypeBool:
		default:
			*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf("%s.%s is %s; values must be scalar (string, int, double, bool)", field, k, v.Type())})
		}
		return true
	})
}
