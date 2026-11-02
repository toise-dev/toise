package conformance

import (
	"fmt"
	"sort"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// frozenVocabulary is one value vocabulary the producer contract freezes.
//
// It is data rather than prose because prose is how we kept arriving here. A
// frozen vocabulary costs one table in a Markdown file, nothing reads that
// file, and a producer author gets no signal — so the divergence is found by
// whoever reads the graph months later, and a non-groupable value does not read
// as an error: it reads as a smaller fleet (toise-dev/toise#579).
type frozenVocabulary struct {
	// key is the attribute key, matched inside entity.description.
	key string
	// values is the frozen set.
	values []string
	// refuse is false for a vocabulary reported as advisory rather than failing
	// the run. Two reasons put a vocabulary there, and only two: the contract
	// declares the enum OPEN, or the reference producer has not yet been
	// verified to emit only the frozen set. Refusing a value somebody emits
	// turns their conformance suite red, which is why enforcement ships with or
	// after the producer change, never before.
	refuse bool
	// why says what a non-conformant value costs a consumer. It is not a
	// restatement of the rule: a message that only repeats the table teaches a
	// producer author nothing about the consequence of ignoring it.
	why string
	// verified records what was checked before switching refusal on, or what is
	// still missing. It is kept beside the decision because the decision is not
	// reviewable without it.
	verified string
}

// frozenVocabularies is the kit's half of the agreement; the contract document
// is the other half, and TestFrozenVocabulariesMatchTheContract compares them.
var frozenVocabularies = []frozenVocabulary{
	{
		key:    "oper_state",
		values: []string{"up", "down", "testing", "unknown", "dormant", "not_present", "lower_layer_down"},
		refuse: true,
		why: "an interface carries traffic if and only if oper_state is up, so a consumer deciding whether a hop is cut " +
			"compares this value against the frozen set. A value outside it matches no comparison: the interface reads as " +
			"neither up nor down, and a filter written against the contract returns a clean empty list that reads as an " +
			"empty fleet rather than as an unanswered question",
		verified: "both reference probes map through one shared table (senhub-agent internal/agent/services/entity/operstate), " +
			"omit anything outside 1..7 or the sysfs strings, and the mapping was accepted on the release candidate",
	},
	{
		key:    "admin_state",
		values: []string{"up", "down", "testing"},
		refuse: true,
		why: "oper_state forbids the administrative fallback precisely because a reader has admin_state in view; a value " +
			"outside the set withdraws that, and the reader is left inferring an administrative state from an operational " +
			"one, which is the conflation the two keys exist to separate",
		verified: "the reference host source emits only up or down, read from the interface's administrative flag, and omits " +
			"the attribute when the flags are unreadable; the SNMP source does not emit the key yet, so no emitted value " +
			"falls outside the set",
	},
	{
		key:    "duplex",
		values: []string{"full", "half", "unknown"},
		refuse: true,
		why: "duplex is renegotiable, so it is read as a current setting and compared between the two ends of a link. A " +
			"third spelling makes two ends that agree look like two ends that differ",
		verified: "both reference probes close the mapping to these three and omit otherwise (duplexName on the SNMP side, " +
			"normDuplex on the host side)",
	},
	{
		key:    "interface.type",
		values: []string{"physical", "virtual", "wireless", "loopback"},
		refuse: true,
		why: "the type is what separates a physical port from the virtual interfaces stacked on it, so a value outside the " +
			"set puts an interface in neither group — and a topology drawn by grouping on it silently loses the port",
		verified: "both reference probes return only members of this set and omit otherwise. They do disagree on one " +
			"member — the SNMP side renders a software loopback as loopback, the host side classifies it as virtual " +
			"because it is a virtual device on Linux — but both values are in the set, so that is a source asymmetry to " +
			"settle in the contract, not a vocabulary violation",
	},
	{
		key: "host.virtualization",
		values: []string{"none", "kvm", "vmware", "xen", "hyperv", "virtualbox", "qemu", "lxc", "openvz", "bhyve",
			"unknown"},
		refuse: false,
		why: "a value outside the set is not comparable, so two hosts on the same hypervisor do not group — and the " +
			"grouping is the whole point of normalizing a value that every source spells differently. Advisory because " +
			"the contract declares this enum OPEN: new hypervisors arrive, and refusing one would be wrong. But an " +
			"unseen new member is how the table silently stops being complete, which is why it is still reported",
		verified: "open by contract; nothing to verify on the producer side, since no value is refused",
	},
	{
		key:    "host.chassis.type",
		values: []string{"desktop", "laptop", "server", "blade", "vm", "other"},
		refuse: false,
		why: "the contract normalizes about thirty numeric chassis codes onto these six because the raw codes are " +
			"high-cardinality and unfriendly; a seventh value defeats that and leaves a fleet view with a bucket nobody " +
			"asked for",
		verified: "NOT yet verified on the reference producer: the value reaches the attribute through a detection path " +
			"and a separate store strategy, and neither was read against this set. Advisory until it is — refusing a " +
			"value somebody emits turns their conformance suite red",
	},
}

// checkValueVocabularies refuses a value outside a frozen vocabulary.
//
// It is keyed on the attribute KEY and not on the entity type, which is the
// contract's own framing: the vocabulary belongs to the key, and the key means
// what the contract says wherever it appears. A producer that puts one of these
// keys on an unexpected type still has its value judged, which is right — the
// alternative is a per-type allowlist that silently exempts every type nobody
// thought of.
//
// It judges only what entity.description carries, because that is where the
// wire contract puts descriptive attributes. One of these keys arriving as a
// resource or record attribute is a different violation — an attribute in the
// wrong envelope — and is not this check's business.
//
// Three shapes of wrong value, three messages, because they are three defects
// with three different fixes. A value outside the set is a mapping that was
// never written; an EMPTY value is an attribute emitted where the contract says
// to omit; a NON-STRING is almost always the raw source enumeration passed
// through. Collapsing them into one message would name the wrong defect in two
// cases out of three.
func checkValueVocabularies(out *[]Problem, where, field string, m pcommon.Map) {
	for _, v := range frozenVocabularies {
		got, ok := m.Get(v.key)
		if !ok {
			continue
		}
		add := func(issue string) {
			*out = append(*out, Problem{Record: where, Advisory: !v.refuse, Issue: issue})
		}
		// A non-string is not reported anywhere else: entity.description carries
		// the full AnyValue since 0.9.0, so an int leaf is perfectly legal there
		// in general. Under a frozen key it is the raw enumeration — ifOperStatus
		// 7 instead of lower_layer_down — which is the exact defect the frozen
		// vocabulary exists to stop, and it would otherwise pass in silence.
		if got.Type() != pcommon.ValueTypeStr {
			add(fmt.Sprintf("%s.%s is %s, not a string; the contract's vocabulary is a set of NAMES (%s). A raw source "+
				"enumeration passed through matches no comparison a consumer can write, and entity.description accepts "+
				"any scalar, so nothing else refuses it. %s", field, v.key, got.Type(), v.list(), v.why))
			continue
		}
		if got.Str() == "" {
			add(fmt.Sprintf("%s.%s is present and empty; the contract says a measurement not taken is OMITTED, and that "+
				"an empty string reads as an answer. This is not a missing mapping — it is an attribute emitted where "+
				"the contract asks for absence, and the two have different fixes", field, v.key))
			continue
		}
		if v.contains(got.Str()) {
			continue
		}
		add(fmt.Sprintf("%s.%s is %q, which the contract's frozen vocabulary does not contain (%s). %s",
			field, v.key, got.Str(), v.list(), v.why))
	}
}

func (v frozenVocabulary) contains(s string) bool {
	for _, want := range v.values {
		if s == want {
			return true
		}
	}
	return false
}

// list renders the frozen set in a stable order, so a failure message is
// diffable between runs.
func (v frozenVocabulary) list() string {
	sorted := make([]string, len(v.values))
	copy(sorted, v.values)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}
