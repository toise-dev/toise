package conformance

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog"
)

// vocabRecord builds one entity.state record carrying key=value in
// entity.description, which is where descriptive attributes live.
func vocabRecord(key, value string) plog.Logs {
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("entity.state")
	lr.Attributes().PutStr("entity.type", "network.interface")
	lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "11111111-2222-3333-4444-555555555555")
	lr.Attributes().PutEmptyMap("entity.description").PutStr(key, value)
	return ld
}

func TestFrozenVocabularyRefusesAValueOutsideAClosedSet(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		wantProblem      bool
		wantAdvisory     bool
	}{
		// The two spellings #537 measured in one reference producer: the
		// camelCase IF-MIB pass-through, and the collapse onto down.
		{name: "camelCase pass-through", key: "oper_state", value: "lowerLayerDown", wantProblem: true},
		{name: "value in the set", key: "oper_state", value: "lower_layer_down"},
		{name: "every frozen oper_state value is accepted", key: "oper_state", value: "not_present"},
		{name: "uppercase is not the same value", key: "oper_state", value: "UP", wantProblem: true},
		// admin_state's third value is legal even though no reference probe
		// emits it: the contract decides the vocabulary, not the producer.
		{name: "admin_state testing is legal", key: "admin_state", value: "testing"},
		{name: "admin_state outside the set", key: "admin_state", value: "enabled", wantProblem: true},
		{name: "duplex outside the set", key: "duplex", value: "auto", wantProblem: true},
		{name: "interface.type outside the set", key: "interface.type", value: "ethernet", wantProblem: true},
		// Open by contract, and reported anyway: an unseen new member is how
		// the table stops being complete.
		{name: "open enum is advisory", key: "host.virtualization", value: "Hyper-V", wantProblem: true, wantAdvisory: true},
		{name: "open enum member passes", key: "host.virtualization", value: "hyperv"},
		{name: "unverified producer is advisory", key: "host.chassis.type", value: "tower", wantProblem: true, wantAdvisory: true},
		// A key the contract freezes no vocabulary for must not be judged.
		{name: "unfrozen key is left alone", key: "mtu_note", value: "whatever"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var found *Problem
			for _, p := range Check(vocabRecord(tc.key, tc.value)) {
				if strings.Contains(p.Issue, tc.key+" is") {
					got := p
					found = &got
				}
			}
			if !tc.wantProblem {
				if found != nil {
					t.Fatalf("%s=%q was refused: %s", tc.key, tc.value, found.Issue)
				}
				return
			}
			if found == nil {
				t.Fatalf("%s=%q was accepted; the frozen vocabulary does not contain it", tc.key, tc.value)
			}
			if found.Advisory != tc.wantAdvisory {
				t.Fatalf("%s=%q advisory = %v, want %v", tc.key, tc.value, found.Advisory, tc.wantAdvisory)
			}
			// The message has to name the consequence, not restate the table: a
			// producer author who reads only the first line must learn what
			// ignoring it costs a consumer.
			if !strings.Contains(found.Issue, "frozen vocabulary does not contain") {
				t.Errorf("message does not say what was violated: %s", found.Issue)
			}
			if len(found.Issue) < 200 {
				t.Errorf("message names no consequence, only the rule: %s", found.Issue)
			}
		})
	}
}

// A non-string under a frozen key is a type violation, and it is reported as
// one elsewhere. Judging it here too would name one defect twice.
func TestFrozenVocabularyLeavesANonStringToTheTypeCheck(t *testing.T) {
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("entity.state")
	lr.Attributes().PutStr("entity.type", "network.interface")
	lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "11111111-2222-3333-4444-555555555555")
	lr.Attributes().PutEmptyMap("entity.description").PutInt("oper_state", 7)
	for _, p := range Check(ld) {
		if strings.Contains(p.Issue, "frozen vocabulary") {
			t.Fatalf("a non-string oper_state was judged against the vocabulary: %s", p.Issue)
		}
	}
}

// Every advisory vocabulary must say why it is not refused, and every refused
// one must say what was checked before switching refusal on. The decision is
// not reviewable without it, and "it seemed safe" is how a refusal lands on a
// producer that still emits the value.
func TestEveryFrozenVocabularyRecordsItsReasoning(t *testing.T) {
	for _, v := range frozenVocabularies {
		if v.key == "" || len(v.values) == 0 {
			t.Errorf("%q: a vocabulary with no key or no values enforces nothing", v.key)
		}
		if len(v.why) < 80 {
			t.Errorf("%q: why must name the consequence for a consumer, not restate the table: %q", v.key, v.why)
		}
		if len(v.verified) < 40 {
			t.Errorf("%q: verified must record what was checked, or what is still missing: %q", v.key, v.verified)
		}
		seen := map[string]bool{}
		for _, val := range v.values {
			if seen[val] {
				t.Errorf("%q: %q appears twice in the frozen set", v.key, val)
			}
			seen[val] = true
			if val != strings.ToLower(val) {
				t.Errorf("%q: %q is not lowercase; the contract normalizes these values to lowercase snake_case", v.key, val)
			}
		}
	}
}

// contractPath is the producer contract, relative to this package. The test
// below reads it, so moving or renaming the document means updating this
// constant — and the failure says exactly that rather than reporting a missing
// file.
const contractPath = "../../../docs/data-model/senhub-agent-contract.md"

var (
	// A section whose heading announces a value vocabulary: "### AT11 —
	// `host.virtualization` value vocabulary".
	vocabHeadingRe = regexp.MustCompile("(?m)^#+ .*`[^`]+`.*value vocabular")
	// And an attribute table row that declares its type as an enum:
	// "| `duplex` | enum | `full`/`half`/`unknown` | renegotiable |".
	enumRowRe   = regexp.MustCompile("(?m)^\\|\\s*`([^`]+)`\\s*\\|\\s*enum\\s*\\|")
	backtickKey = regexp.MustCompile("`([^`]+)`")
)

// TestFrozenVocabulariesMatchTheContract is the half nothing enforced. The
// contract freezes a vocabulary in prose, the kit refuses values in code, and
// the two have no reason to stay in step: the document costs one table and
// nothing reads it. So a vocabulary added to the document and not to the kit is
// exactly the silence this issue is about, and it fails here.
//
// It deliberately does NOT compare the VALUES of each set. The document writes
// them in prose, in tables, with glosses and abridged mappings, and a parser
// strict enough to extract them would fail on an editorial change while a
// parser loose enough to survive would extract the glosses too. Comparing the
// set of KEYS is the half that drifts in silence; a wrong value inside a set
// the kit knows about shows up as a refused or accepted value in the tests
// above.
func TestFrozenVocabulariesMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("cannot read the producer contract at %s: %v\n"+
			"This test asserts that every value vocabulary the contract freezes is one the kit\n"+
			"knows about. If the contract moved or was renamed, point contractPath in this file\n"+
			"at its new location. Do not delete the test: a frozen vocabulary nothing reads is\n"+
			"what #579 is about.", contractPath, err)
	}
	text := string(raw)

	fromContract := map[string]string{} // key -> where it was found
	for _, heading := range vocabHeadingRe.FindAllString(text, -1) {
		for _, m := range backtickKey.FindAllStringSubmatch(heading, -1) {
			fromContract[m[1]] = "heading: " + strings.TrimSpace(heading)
		}
	}
	for _, m := range enumRowRe.FindAllStringSubmatch(text, -1) {
		if _, already := fromContract[m[1]]; !already {
			fromContract[m[1]] = "enum table row"
		}
	}
	if len(fromContract) == 0 {
		t.Fatalf("no value vocabulary was found in %s.\n"+
			"This test looks for headings containing \"value vocabular\" beside a backticked key,\n"+
			"and for attribute table rows whose type column is `enum`. If that wording changed,\n"+
			"update the patterns in this file so the two lists keep being compared. The\n"+
			"comparison is what matters, not the wording.", contractPath)
	}

	known := map[string]bool{}
	for _, v := range frozenVocabularies {
		known[v.key] = true
	}
	var unenforced []string
	for key, where := range fromContract {
		if !known[key] {
			unenforced = append(unenforced, key+" ("+where+")")
		}
	}
	sort.Strings(unenforced)
	if len(unenforced) > 0 {
		t.Errorf("the contract freezes %d value vocabular(y|ies) the kit does not know about:\n  %s\n"+
			"Add each one to frozenVocabularies in vocabulary.go, in the same commit as the change\n"+
			"that added it to the contract. Ship it as advisory until the reference producer is\n"+
			"verified to emit only the frozen set: a refusal lands on a producer that still emits\n"+
			"the value and turns their conformance suite red, while an advisory breaks nothing.\n"+
			"A vocabulary frozen in the document and enforced nowhere hands a producer author no\n"+
			"signal at all, which is how every one of these was found by reading a graph instead.",
			len(unenforced), strings.Join(unenforced, "\n  "))
	}

	// And the reverse: a key the kit enforces and the document does not freeze
	// means the kit is inventing a contract.
	var invented []string
	for _, v := range frozenVocabularies {
		if _, ok := fromContract[v.key]; !ok {
			invented = append(invented, v.key)
		}
	}
	sort.Strings(invented)
	if len(invented) > 0 {
		t.Errorf("the kit enforces %v, which the contract does not freeze.\n"+
			"Either the document lost the vocabulary, or the kit is refusing values on a rule no\n"+
			"producer author can read. Fix whichever side is wrong.", invented)
	}
}
