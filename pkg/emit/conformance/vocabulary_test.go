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

// A non-string under a frozen key is refused HERE, because nothing else
// refuses it. entity.description carries the full AnyValue since 0.9.0, so an
// int leaf is legal there in general — and an int under oper_state is the raw
// ifOperStatus enumeration, the exact defect the frozen vocabulary exists to
// stop.
//
// This test replaces one that asserted the opposite. That test passed, and it
// pinned the hole open: it was written on a comment claiming the type check
// covered this, and the comment was never checked against checkDescriptionValue,
// which accepts every scalar in silence.
func TestFrozenVocabularyRefusesANonString(t *testing.T) {
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("entity.state")
	lr.Attributes().PutStr("entity.type", "network.interface")
	lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "11111111-2222-3333-4444-555555555555")
	lr.Attributes().PutEmptyMap("entity.description").PutInt("oper_state", 7)
	var found string
	for _, p := range Check(ld) {
		if strings.Contains(p.Issue, "oper_state is Int") {
			found = p.Issue
		}
	}
	if found == "" {
		t.Fatalf("a raw ifOperStatus integer under oper_state was accepted; got %v", Check(ld))
	}
	if !strings.Contains(found, "set of NAMES") {
		t.Errorf("the message does not say what was expected instead: %s", found)
	}
}

// An attribute emitted EMPTY is not a missing mapping, and saying "outside the
// vocabulary" would send a producer author looking for the wrong fix. The
// contract is explicit that a measurement not taken is omitted and that an
// empty string reads as an answer.
func TestFrozenVocabularyNamesAnEmptyValueAsItsOwnDefect(t *testing.T) {
	var found string
	for _, p := range Check(vocabRecord("oper_state", "")) {
		if strings.Contains(p.Issue, "oper_state is present and empty") {
			found = p.Issue
		}
	}
	if found == "" {
		t.Fatalf("an empty oper_state was not reported as an empty value; got %v", Check(vocabRecord("oper_state", "")))
	}
	if strings.Contains(found, "frozen vocabulary does not contain") {
		t.Errorf("an empty value was reported as a missing mapping, which has a different fix: %s", found)
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
	// `host.virtualization` value vocabulary". Case-insensitive, because a
	// Markdown heading gets title-cased by whoever edits it next and a detector
	// that stops matching is a detector that passes in silence.
	vocabHeadingRe = regexp.MustCompile("(?im)^#+ .*`[^`]+`.*value vocabular")
	// And an attribute table row whose type column declares an enum:
	// "| `duplex` | enum | `full`/`half`/`unknown` | renegotiable |". The cell
	// is matched loosely so "open enum" or "enum (provisional)" still counts.
	enumRowRe = regexp.MustCompile("(?im)^\\|\\s*`([^`]+)`\\s*\\|[^|]*\\benum\\b[^|]*\\|")
	// Only tokens shaped like an attribute key are taken from a heading. The
	// heading also carries section numbers and prose, and a phantom key makes
	// the test fail loudly but wrongly — which is how a test gets weakened.
	backtickKey      = regexp.MustCompile("`([^`]+)`")
	attributeKeyLike = regexp.MustCompile(`^[a-z][a-z0-9]*([._][a-z0-9]+)*$`)
	// A section boundary, for cutting the span a heading governs.
	sectionBreakRe = regexp.MustCompile("(?m)^#{1,6} ")
	// The contract declares an enum open in these words (AT11).
	openEnumRe = regexp.MustCompile(`(?i)open enum`)
)

// TestFrozenVocabulariesMatchTheContract is the half nothing enforced. The
// contract freezes a vocabulary in prose, the kit refuses values in code, and
// the two have no reason to stay in step: the document costs one table and
// nothing reads it.
//
// It checks three agreements, and is honest about the fourth it cannot.
//
//  1. every vocabulary the document freezes is one the kit knows about, and
//     every key the kit enforces is one the document freezes. The second
//     direction is also the detector's own witness: if a pattern below stops
//     matching a section, the six known keys become "invented" and this test
//     fails loudly. Only a NEW vocabulary written in an unmatched format can
//     slip through, which is the residual risk and the reason the patterns are
//     deliberately loose;
//  2. every value the kit holds still appears, backticked, inside the span of
//     the document that freezes it. So a value removed or renamed in the
//     document fails here instead of leaving the kit accepting something the
//     contract no longer permits. Its real strength is narrower than it looks,
//     and the first witness written for it was wrong: the span includes the
//     mapping table, whose SOURCE columns repeat several canonical spellings
//     verbatim, so removing `dormant` from the vocabulary still leaves
//     `dormant` in the IF-MIB column and this check stays green. It catches a
//     value whose canonical form is distinctive — `lower_layer_down`, whose
//     sources are spelled `lowerLayerDown` and `lowerlayerdown` — and not one
//     that happens to equal its source. Written down rather than overclaimed,
//     because overclaiming what a check covers is how the hole below got
//     pinned open;
//  3. a vocabulary the document declares OPEN is not refused by the kit.
//     Refusing a value an open enum exists to admit is the harmful direction;
//     the reverse — closed in the document, advisory in the kit — is the
//     deliberate ordering rule and must stay allowed.
//
// What it does NOT check: that the kit's set is exactly the document's. The
// document writes values in prose, in tables, with glosses and abridged
// mappings, and a parser strict enough to extract the SET would break on an
// editorial change. So a value ADDED to the document and not to the kit is not
// caught here, and the kit would refuse a value the contract permits. That gap
// is stated rather than papered over — an earlier version of this comment
// claimed the behavior tests covered it, which was false: those tests assert
// against the kit's own set, so both halves would have stayed green together.
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

	// span is the stretch of the document that freezes a key: the section a
	// heading governs, or the table row itself.
	type locus struct{ where, span string }
	fromContract := map[string]locus{}
	for _, loc := range vocabHeadingRe.FindAllStringIndex(text, -1) {
		heading := text[loc[0]:loc[1]]
		rest := text[loc[1]:]
		end := len(rest)
		if b := sectionBreakRe.FindStringIndex(rest); b != nil {
			end = b[0]
		}
		span := heading + rest[:end]
		for _, m := range backtickKey.FindAllStringSubmatch(heading, -1) {
			if attributeKeyLike.MatchString(m[1]) {
				fromContract[m[1]] = locus{"heading: " + strings.TrimSpace(heading), span}
			}
		}
	}
	for _, loc := range enumRowRe.FindAllStringSubmatchIndex(text, -1) {
		key := text[loc[2]:loc[3]]
		if _, already := fromContract[key]; !already {
			fromContract[key] = locus{"enum table row", text[loc[0]:loc[1]] + lineAfter(text, loc[1])}
		}
	}
	if len(fromContract) == 0 {
		t.Fatalf("no value vocabulary was found in %s.\n"+
			"This test looks for headings containing \"value vocabular\" beside a backticked key,\n"+
			"and for attribute table rows whose type column declares an enum. If that wording\n"+
			"changed, update the patterns in this file so the two lists keep being compared. The\n"+
			"comparison is what matters, not the wording.", contractPath)
	}

	known := map[string]bool{}
	for _, v := range frozenVocabularies {
		known[v.key] = true
	}
	var unenforced []string
	for key, l := range fromContract {
		if !known[key] {
			unenforced = append(unenforced, key+" ("+l.where+")")
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

	var invented []string
	for _, v := range frozenVocabularies {
		l, ok := fromContract[v.key]
		if !ok {
			invented = append(invented, v.key)
			continue
		}
		// 2. the kit's values must still be written in the document.
		for _, val := range v.values {
			if !strings.Contains(l.span, "`"+val+"`") {
				t.Errorf("the kit holds %s = %q, which no longer appears backticked in the contract (%s).\n"+
					"Either the document dropped the value and the kit is now accepting something the\n"+
					"contract does not permit, or it was renamed on one side only. Fix whichever is wrong.",
					v.key, val, l.where)
			}
		}
		// 3. an open enum must not be refused.
		if v.refuse && openEnumRe.MatchString(l.span) {
			t.Errorf("the kit REFUSES values outside %s, but the contract declares that enum open (%s).\n"+
				"An open enum exists so new members can arrive; refusing one rejects a value the\n"+
				"contract permits. Report it as advisory instead.", v.key, l.where)
		}
	}
	sort.Strings(invented)
	if len(invented) > 0 {
		t.Errorf("the kit enforces %v, which the contract does not freeze.\n"+
			"Either the document lost the vocabulary, or the kit is refusing values on a rule no\n"+
			"producer author can read. Fix whichever side is wrong. This is also the detector's\n"+
			"own witness: if the patterns in this file stopped matching a section, a known key\n"+
			"lands here rather than disappearing in silence.", invented)
	}
}

// lineAfter returns the remainder of the line starting at i, so an enum row's
// span covers the whole row including its values column.
func lineAfter(text string, i int) string {
	if n := strings.IndexByte(text[i:], '\n'); n >= 0 {
		return text[i : i+n]
	}
	return text[i:]
}
