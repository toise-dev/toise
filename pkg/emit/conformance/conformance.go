// Package conformance validates producer output against the Toise entity-event
// wire contract, without a running Toise: feed it the plog.Logs your producer
// emits (or the bytes of an ExportRequest) and fix every Problem it returns.
// A producer that passes never trips Toise's per-record rejection for shape
// reasons. Type-registry membership is enforced separately: under the default
// strict vocabulary an entity.type outside the registry is still rejected per
// record, unless the deployment opts into accept_unknown_types (#141).
// Problems marked Advisory do not fail a run; they flag a misconfiguration
// that degrades consumer behavior. A problem NOT marked Advisory must not
// ship, which is not the same statement as "Toise rejects the record": some
// of them Toise accepts and the damage is then silent and permanent, which is
// precisely why the kit refuses them. A mis-rendered host.id is stored and
// duplicates a machine for good; a same_as whose confidence cannot be read is
// stored and collapses nothing. Neither is a rejection and neither may be
// shipped.
//
// The checked-in fixture (testdata/fixture_v1.bin) is the published contract
// v1: the toise-emit SDK reproduces it byte for byte, and Toise's own ingest
// tests accept it with zero rejections — one artifact pins both sides.
package conformance

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// Problem is one contract violation, with where it was found.
type Problem struct {
	Record string // which record, e.g. "resourceLogs[0].scopeLogs[0].logRecords[2]"
	Issue  string
	// Advisory marks a problem the kit reports without failing the run: a
	// producer misconfiguration that degrades consumer behavior (e.g. a
	// missing service.instance.id collapses multi-producer liveness reference
	// counting, ADR 0019). Its absence means "do not ship this", and says
	// nothing about whether Toise would reject the record — see the package
	// comment.
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
// ResourceLogs that carries entity events. Advisory only, because Toise does
// accept the records — but the cost is larger than a lost distinction between
// producers, and the message has to name it, or a producer reads exit 0 and
// ships the one condition the read surface calls a defect.
//
// Without a stable instance id the reference is anonymous, and releaseSpeaker
// matches the producer that sends the delete: no named producer can ever
// release what was asserted anonymously. A production host sat in a customer
// graph that way for eight weeks, reading as an entity nobody wanted.
func checkResource(i int, res pcommon.Resource) []Problem {
	if v, ok := res.Attributes().Get(wire.ResServiceInstanceID); ok && v.Type() == pcommon.ValueTypeStr && v.Str() != "" {
		return nil
	}
	return []Problem{{
		Record: fmt.Sprintf("resourceLogs[%d].resource", i),
		Issue: "missing or empty service.instance.id — the entity will be asserted ANONYMOUSLY: " +
			"no named producer can ever release it (a delete is matched to the producer that sends it), " +
			"every producer collapses into one shared reference (ADR 0019), and Toise reports the entity " +
			"to its readers as a producer defect to come back to you about. Set a stable instance id",
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
		checkHostID(&out, where, "entity.id", id.Map())
		checkMAC(&out, where, "entity.id", id.Map())
		checkIdentityKeySpelling(&out, where, "entity.id", id.Map())
	}

	if desc, ok := attrs.Get(wire.AttrEntityDescription); ok {
		if desc.Type() != pcommon.ValueTypeMap {
			bad("entity.description must be a map, got %s", desc.Type())
		} else {
			// Unlike identity, entity.description carries the full AnyValue since
			// 0.9.0 (#259): arrays and nested maps are ingested faithfully. Only an
			// empty key or an unsupported leaf (bytes/empty) is a problem.
			checkDescriptionMap(&out, where, "entity.description", desc.Map())
			// A descriptive mac is not identity, and it is still joined on: a
			// consumer matching telemetry to an interface compares this value, so a
			// second rendering breaks that join as thoroughly as it would an
			// identity. Same rule, same failure.
			checkMAC(&out, where, "entity.description", desc.Map())
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
				} else {
					// An edge names its target by identity, so a mis-rendered host.id
					// here points at a host that does not exist rather than at the
					// wrong one — the edge is parked, then dropped.
					checkHostID(&out, rw, "entity.id", v.Map())
					checkMAC(&out, rw, "entity.id", v.Map())
					checkIdentityKeySpelling(&out, rw, "entity.id", v.Map())
				}
				switch rt, _ := m.Get(wire.RelType); rt.Str() {
				case wire.RelTypeSameAs:
					out = append(out, checkSameAsBelief(rw, m)...)
				case wire.RelTypeResolvesTo:
					out = append(out, checkResolvesToBelief(rw, m)...)
				}
			}
		}
	}
	return out
}

// beliefConfidenceByBasis pins the confidence a named basis carries. See
// wire.BasisRekey and its neighbors for why these three are fixed at 1.0 and
// why a basis outside the set is left alone.
var beliefConfidenceByBasis = map[string]float64{
	wire.BasisRekey:     1.0,
	wire.BasisK8sAPI:    1.0,
	wire.BasisVMIDMatch: 1.0,
}

// checkSameAsBelief validates a same_as edge's belief attributes, and refuses
// the edge rather than advising on it.
//
// A same_as exists to feed the read-time canonical overlay (ADR 0020), which
// collapses only edges whose confidence is a number in [0,1] at or above the
// server's threshold. Toise STORES an edge whose confidence is absent,
// non-numeric or out of range — it is not a rejection — and the overlay then
// ignores it. That is the whole problem: the producer believes it published an
// identity join, the consumer reads two separate entities, and nothing
// anywhere reports it. Before 0.23 this was advisory, which assumed a producer
// reads advisories; a per-machine failure that scales with the fleet (one
// same_as per Azure VM, per re-keyed host, per cluster node) has to stop a
// producer before it ships.
//
// A confidence BELOW the threshold is not flagged: a deliberately weak belief
// is a legitimate statement, and the threshold is the server's to set.
func checkSameAsBelief(where string, m pcommon.Map) []Problem {
	var out []Problem

	basis, hasBasis := m.Get(wire.RelBasis)
	if !hasBasis || strings.TrimSpace(basis.Str()) == "" {
		out = append(out, Problem{Record: where, Issue: "same_as edge has no basis; the belief is usable but a reader cannot tell what it rests on, which is the half a human or an LLM reads", Advisory: true})
	}

	v, ok := m.Get(wire.RelConfidence)
	if !ok {
		return append(out, Problem{Record: where, Issue: "same_as edge has no confidence; Toise stores the edge and the canonical overlay collapses only edges with a confidence in [0,1], so this belief is inert and nothing reports it"})
	}
	var c float64
	switch v.Type() {
	case pcommon.ValueTypeDouble:
		c = v.Double()
	case pcommon.ValueTypeInt:
		c = float64(v.Int())
	default:
		return append(out, Problem{Record: where, Issue: fmt.Sprintf("same_as confidence is %s; it must be a number in [0,1] or the overlay ignores the belief, which is stored either way", v.Type())})
	}
	if c < 0 || c > 1 {
		return append(out, Problem{Record: where, Issue: fmt.Sprintf("same_as confidence %g is outside [0,1]; the overlay ignores it and the edge is still stored", c)})
	}

	if hasBasis {
		if want, fixed := beliefConfidenceByBasis[basis.Str()]; fixed && c != want {
			out = append(out, Problem{Record: where, Issue: fmt.Sprintf("same_as basis %q carries a contractual confidence of %g, not %g; this basis is an exact identity match, and a lower value collapses nothing once it falls under the server threshold", basis.Str(), want, c)})
		}
	}
	return out
}

// checkResolvesToBelief advises that a resolution belief's confidence is
// accepted and read by nothing.
//
// Ingest carries confidence and basis for resolves_to exactly as it does for
// same_as, but every reader of the attribute gates on same_as alone: the
// canonical walk and the alias-aware traversal both do. So a producer that
// states a resolution belief with a confidence has it parsed, stored and
// consumed by no decision — and cannot tell that from a belief that was simply
// too weak to act on. Advisory, not a refusal: the basis is genuinely useful
// as provenance, and withdrawing acceptance would narrow the wire contract.
func checkResolvesToBelief(where string, m pcommon.Map) []Problem {
	if _, ok := m.Get(wire.RelConfidence); !ok {
		return nil
	}
	return []Problem{{Record: where, Issue: "resolves_to carries a confidence, which is accepted and stored but read by nothing: no grouping and no path decision uses it, so only the basis is of any use here", Advisory: true}}
}

// checkHostID flags a host.id whose rendering is not the contract's. Not
// advisory: the failure it prevents is silent and permanent — the same machine
// written two ways is two entities under exact identity (ADR 0018), and nothing
// in the graph says they are one. It surfaces months later against an entity
// everything already points at, which is why it must stop a producer before it
// ships rather than warn once and be filed away.
//
// Two renderings are flagged, both unambiguous:
//
//   - the raw 32-hex contents of /etc/machine-id, which a producer reading the
//     file directly emits while one going through a library that formats it
//     emits the hyphenated UUID of the same bytes;
//   - a hyphenated UUID carrying uppercase hex, which differs from the same id
//     in lowercase by nothing a reader would notice.
func checkHostID(out *[]Problem, where, field string, m pcommon.Map) {
	v, ok := m.Get(wire.IdentityHostID)
	if !ok || v.Type() != pcommon.ValueTypeStr {
		return
	}
	s := v.Str()
	switch {
	case isRawMachineID(s):
		*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf(
			"%s.%s is the raw 32-hex machine-id; the contract spells it as a lowercase hyphenated UUID (8-4-4-4-12). "+
				"A producer already emitting this form in production must coordinate the change — it re-keys every host — "+
				"rather than switch renderings on upgrade", field, wire.IdentityHostID)})
	case isUUIDWithUppercase(s):
		*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf(
			"%s.%s carries uppercase hex; the contract spells host.id lowercase, and the same id in two cases is two entities. "+
				"A producer already emitting this form in production must coordinate the change — it re-keys every host",
			field, wire.IdentityHostID)})
	}
}

// checkMAC flags a hardware address whose rendering is not the contract's.
//
// Not advisory, and for the reason that made host.id non-advisory: a MAC becomes
// an identity. It identifies a network.neighbor, it is the "mac:" rung of a
// network.device.id, and it is the value a same_as belief joins on when SNMP
// later names the device a neighbor was first seen as. Two renderings of the
// same six bytes are two entities under exact identity (ADR 0018): nothing says
// they are one, the join silently finds nothing, and the duplicate surfaces
// months later.
//
// The form is IEEE RA hexadecimal — uppercase, hyphen-separated octets, most
// significant first — which is what semconv requires for host.mac. It is not
// ours to choose: the only argument that could outweigh the standard is that our
// own spelling reads better, which is the argument that produces a model nobody
// else can join.
//
// The test is positive rather than a list of wrong forms, unlike host.id's: there
// is exactly one right rendering, so anything else is flagged, including the
// lowercase colon form a reader is most likely to write by hand.
func checkMAC(out *[]Problem, where, field string, m pcommon.Map) {
	flag := func(key, got string) {
		*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf(
			"%s.%s is %q; the contract renders a MAC in IEEE RA hexadecimal — uppercase, hyphen-separated octets "+
				"(AA-BB-CC-DD-EE-FF), the form semconv requires for host.mac. The same address written two ways is two "+
				"entities, and the same_as join that would have united them finds nothing, silently. A producer already "+
				"emitting another rendering in production must coordinate the change — it re-keys every entity whose "+
				"identity carries a MAC", field, key, got)})
	}
	if v, ok := m.Get(wire.IdentityMAC); ok && v.Type() == pcommon.ValueTypeStr && !isCanonicalMAC(v.Str()) {
		flag(wire.IdentityMAC, v.Str())
	}
	// Only the mac: rung of a device id is a MAC; the other rungs are serials,
	// engine ids and names, and judging them against this form would be wrong.
	if v, ok := m.Get(wire.IdentityNetworkDeviceID); ok && v.Type() == pcommon.ValueTypeStr {
		if rest, found := strings.CutPrefix(v.Str(), "mac:"); found && !isCanonicalMAC(rest) {
			flag(wire.IdentityNetworkDeviceID, v.Str())
		}
	}
}

// checkIdentityKeySpelling refuses a retired spelling in an IDENTITY, while the
// same key stays perfectly acceptable as a descriptive attribute.
//
// That split is the whole point, and it is why tolerance elsewhere is not
// tolerance here. `interface.name` is still read as a display name and still
// accepted as a telemetry join key, because the graph holds interfaces observed
// before the semconv spelling existed and dropping it would blind the pivot on
// them. But an identity is hashed over the keys AS SENT — no normalization, by
// design (ADR 0017, ADR 0022: the engine does not decide what a producer meant)
// — so one port named two ways is two entities, joined by nothing, and the
// duplicate surfaces months later.
//
// Measured before switching it on, like the MAC rendering: no producer emits the
// retired spelling as identity, and none has since the migration. So this is a
// lint that catches the first regression, not a migration.
func checkIdentityKeySpelling(out *[]Problem, where, field string, m pcommon.Map) {
	for retired, use := range retiredIdentityKeys {
		if _, ok := m.Get(retired); !ok {
			continue
		}
		*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf(
			"%s carries %q, which is no longer an identity key; use %q. The spelling is still fine as a "+
				"descriptive attribute and as a telemetry join key — the graph holds entities observed before the "+
				"semconv spelling existed. But an identity is hashed over the keys as sent, with no normalization: "+
				"one object named two ways is two entities, nothing joins them, and the duplicate surfaces months "+
				"later", field, retired, use)})
	}
}

// retiredIdentityKeys maps a spelling that must not identify anything to the one
// that must. Both stay valid as descriptive attributes; only identity is closed.
var retiredIdentityKeys = map[string]string{
	"interface.name": wire.IdentityNetworkInterfaceName,
}

// isCanonicalMAC reports whether s is six uppercase hex octets separated by
// hyphens. EUI-64 (eight octets) is not accepted: no producer emits one, and
// accepting a second length would make the check describe the data rather than
// the contract.
func isCanonicalMAC(s string) bool {
	const want = 17 // 6 octets, 5 separators
	if len(s) != want {
		return false
	}
	for i := 0; i < len(s); i++ {
		if i%3 == 2 {
			if s[i] != '-' {
				return false
			}
			continue
		}
		c := s[i]
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// isRawMachineID reports whether s is 32 hex digits with no separator — the
// literal contents of /etc/machine-id.
func isRawMachineID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

// isUUIDWithUppercase reports whether s is an 8-4-4-4-12 hyphenated UUID holding
// at least one uppercase hex letter.
func isUUIDWithUppercase(s string) bool {
	if len(s) != 36 {
		return false
	}
	upper := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !isHexDigit(c) {
			return false
		}
		if c >= 'A' && c <= 'F' {
			upper = true
		}
	}
	return upper
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// checkScalarMap flags empty keys and non-scalar values: identity is a flat
// scalar map with non-empty keys (ADR 0018). Toise rejects an empty key in every
// mode (strict and accept_unknown_types alike) and, for identity, drops a
// non-scalar value with a warning.
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

// checkDescriptionMap validates entity.description, which carries the full
// AnyValue since 0.9.0 (#259): scalars, arrays, and nested maps are all fine and
// ingested faithfully. Only an empty key (rejected in every mode) or an
// unsupported leaf (bytes/empty, which Toise drops) is flagged.
func checkDescriptionMap(out *[]Problem, where, field string, m pcommon.Map) {
	m.Range(func(k string, v pcommon.Value) bool {
		if k == "" {
			*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf("%s has an empty attribute key; Toise rejects it in every mode", field)})
		}
		checkDescriptionValue(out, where, field+"."+k, v)
		return true
	})
}

func checkDescriptionValue(out *[]Problem, where, path string, v pcommon.Value) {
	switch v.Type() {
	case pcommon.ValueTypeStr, pcommon.ValueTypeInt, pcommon.ValueTypeDouble, pcommon.ValueTypeBool:
	case pcommon.ValueTypeSlice:
		sl := v.Slice()
		for i := 0; i < sl.Len(); i++ {
			checkDescriptionValue(out, where, fmt.Sprintf("%s[%d]", path, i), sl.At(i))
		}
	case pcommon.ValueTypeMap:
		checkDescriptionMap(out, where, path, v.Map())
	default: // bytes, empty — unsupported leaves Toise drops
		*out = append(*out, Problem{Record: where, Issue: fmt.Sprintf("%s is %s; unsupported leaf (Toise keeps scalars, arrays, and nested maps but drops this)", path, v.Type())})
	}
}
