package conformance

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/pkg/emit/wire"
)

// TestCheckCatchesContractViolations pins each rule a producer CI relies on.
func TestCheckCatchesContractViolations(t *testing.T) {
	mk := func(build func(lr plog.LogRecord)) plog.Logs {
		ld := plog.NewLogs()
		lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		build(lr)
		return ld
	}
	cases := []struct {
		name  string
		logs  plog.Logs
		issue string
	}{
		{"missing type", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutEmptyMap("entity.id").PutStr("k", "v")
		}), "missing entity.type"},
		{"empty id", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutStr("entity.type", "host")
			lr.Attributes().PutEmptyMap("entity.id")
		}), "entity.id is empty"},
		{"mistyped interval", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutStr("entity.type", "host")
			lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h")
			lr.Attributes().PutStr("entity.report.interval", "300")
		}), "entity.report.interval must be an int"},
		{"non-scalar id value", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutStr("entity.type", "host")
			m := lr.Attributes().PutEmptyMap("entity.id")
			m.PutEmptySlice("nested")
		}), "values must be scalar"},
		{"empty key in id", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutStr("entity.type", "host")
			lr.Attributes().PutEmptyMap("entity.id").PutStr("", "x")
		}), "empty attribute key"},
		{"empty key in description", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutStr("entity.type", "host")
			lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h")
			lr.Attributes().PutEmptyMap("entity.description").PutStr("", "x")
		}), "empty attribute key"},
		{"incomplete relationship", mk(func(lr plog.LogRecord) {
			lr.Attributes().PutStr("entity.type", "host")
			lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h")
			d := lr.Attributes().PutEmptySlice("entity.relationships").AppendEmpty().SetEmptyMap()
			d.PutStr("relationship.type", "runs_on")
		}), "target entity.type"},
	}
	for _, tc := range cases {
		problems := Check(tc.logs)
		found := false
		for _, p := range problems {
			if strings.Contains(p.Issue, tc.issue) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: problems %v do not include %q", tc.name, problems, tc.issue)
		}
	}

	// A same_as edge with a valid confidence is fully conformant.
	okSameAs := mk(func(lr plog.LogRecord) {
		lr.Attributes().PutStr("entity.type", "network.device")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("name", "sw1")
		d := lr.Attributes().PutEmptySlice("entity.relationships").AppendEmpty().SetEmptyMap()
		d.PutStr("relationship.type", "same_as")
		d.PutStr("entity.type", "network.device")
		d.PutEmptyMap("entity.id").PutStr("mac", "AA-BB-CC-DD-EE-FF")
		d.PutDouble("confidence", 0.9)
	})
	for _, p := range Check(okSameAs) {
		if !p.Advisory {
			t.Errorf("valid same_as flagged: %v", p)
		}
	}

	// A same_as edge missing confidence BLOCKS. Toise stores it and the overlay
	// ignores it, so the producer believes it published an identity join while
	// the consumer reads two entities and nothing reports it. Advisory until
	// 0.23, which assumed a producer reads advisories; the failure is one per
	// machine and scales with the fleet.
	noConf := mk(func(lr plog.LogRecord) {
		lr.Attributes().PutStr("entity.type", "network.device")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("name", "sw1")
		d := lr.Attributes().PutEmptySlice("entity.relationships").AppendEmpty().SetEmptyMap()
		d.PutStr("relationship.type", "same_as")
		d.PutStr("entity.type", "network.device")
		d.PutEmptyMap("entity.id").PutStr("mac", "AA-BB-CC-DD-EE-FF")
	})
	foundBlocking := false
	for _, p := range Check(noConf) {
		if !p.Advisory && strings.Contains(p.Issue, "no confidence") {
			foundBlocking = true
		}
	}
	if !foundBlocking {
		t.Error("same_as without confidence must block, not advise")
	}

	// A plain log record is not an entity event: ignored, like Toise does.
	plain := plog.NewLogs()
	plain.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr("hello")
	if problems := Check(plain); len(problems) != 0 {
		t.Errorf("plain log record flagged: %v", problems)
	}
}

// TestDescriptionAllowsAnyValue pins that entity.description accepts the full
// AnyValue (arrays, nested maps) since 0.9.0 — the conformance kit must not
// reject the RichAttributes output the SDK itself emits (#259). Identity stays
// strict-scalar; only an unsupported leaf (bytes) in a description is flagged.
func TestDescriptionAllowsAnyValue(t *testing.T) {
	mk := func(build func(desc pcommon.Map)) plog.Logs {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr("service.instance.id", "p1") // avoid the unrelated advisory
		lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		lr.Attributes().PutStr("entity.type", "host")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h")
		build(lr.Attributes().PutEmptyMap("entity.description"))
		return ld
	}

	// A rich description: scalars, an array, and a nested map — all accepted.
	rich := mk(func(d pcommon.Map) {
		d.PutStr("os", "linux")
		arr := d.PutEmptySlice("tags")
		arr.AppendEmpty().SetStr("web")
		arr.AppendEmpty().SetStr("prod")
		nested := d.PutEmptyMap("labels")
		nested.PutStr("team", "infra")
		nested.PutInt("tier", 1)
	})
	if problems := Check(rich); len(problems) != 0 {
		t.Errorf("rich AnyValue description was flagged (must be accepted since 0.9.0): %v", problems)
	}

	// An unsupported leaf (bytes) is still flagged — Toise drops it.
	withBytes := mk(func(d pcommon.Map) {
		d.PutEmptyBytes("blob").Append(1, 2, 3)
	})
	found := false
	for _, p := range Check(withBytes) {
		if strings.Contains(p.Issue, "unsupported leaf") {
			found = true
		}
	}
	if !found {
		t.Errorf("a bytes leaf in a description should be flagged as unsupported")
	}
}

// TestAdvisoryServiceInstanceID pins the Resource-level advisory: entity events
// without a service.instance.id are accepted by Toise, but liveness is
// reference-counted per producer (ADR 0019), so the misconfiguration must
// surface as an advisory Problem — and only when the ResourceLogs actually
// carries entity events.
func TestAdvisoryServiceInstanceID(t *testing.T) {
	mk := func(instanceID string, withResource bool) plog.Logs {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		if withResource {
			rl.Resource().Attributes().PutStr("service.instance.id", instanceID)
		}
		lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		lr.Attributes().PutStr("entity.type", "host")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h")
		return ld
	}
	advisories := func(ld plog.Logs) []Problem {
		var out []Problem
		for _, p := range Check(ld) {
			if p.Advisory {
				out = append(out, p)
			}
		}
		return out
	}

	if got := advisories(mk("", false)); len(got) != 1 || !strings.Contains(got[0].Issue, "service.instance.id") {
		t.Errorf("missing service.instance.id: advisories = %v, want exactly one naming it", got)
	}
	if got := advisories(mk("", true)); len(got) != 1 {
		t.Errorf("empty service.instance.id: advisories = %v, want exactly one", got)
	}
	if got := advisories(mk("producer-01", true)); len(got) != 0 {
		t.Errorf("present service.instance.id: advisories = %v, want none", got)
	}

	// The wording IS the fix. A producer runs this, reads "advisory", and ships;
	// so the sentence has to name what it costs, not the rule it breaks. Toise's
	// read surface calls this entity a producer defect, and until this message
	// said so the two surfaces of the same product disagreed about it.
	msg := advisories(mk("", false))[0].Issue
	for _, must := range []string{
		"ANONYMOUSLY", // what the entity becomes
		"release",     // that nobody can ever remove it
		"ADR 0019",    // where the rule lives
		"defect",      // that a reader will be sent back to this producer
	} {
		if !strings.Contains(msg, must) {
			t.Errorf("the advisory omits %q, so a producer cannot see the cost: %q", must, msg)
		}
	}
	if probs := Check(mk("producer-01", true)); len(probs) != 0 {
		t.Errorf("conformant record with instance id: problems = %v, want none", probs)
	}
}

// TestHostIDSpelling pins the check that stops the duplicate-per-host defect at
// the producer rather than months later in the graph: the same machine-id
// written two ways is two entities, and nothing in the graph says they are one.
func TestHostIDSpelling(t *testing.T) {
	mk := func(typ, hostID string) plog.Logs {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr("service.instance.id", "agent-1")
		lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		lr.Attributes().PutStr("entity.type", typ)
		lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", hostID)
		return ld
	}

	t.Run("raw machine-id fails", func(t *testing.T) {
		got := Check(mk("host", "8b86170405bc4382b0577eac3df5e730"))
		if len(got) != 1 {
			t.Fatalf("problems = %v, want exactly one", got)
		}
		if got[0].Advisory {
			t.Error("the raw machine-id must fail, not advise: the defect is silent and permanent")
		}
		if !strings.Contains(got[0].Issue, "lowercase hyphenated UUID") {
			t.Errorf("issue = %q, want the expected rendering named", got[0].Issue)
		}
		if !strings.Contains(got[0].Issue, "re-keys every host") {
			t.Errorf("issue = %q, want the migration caveat: telling a live producer to switch renderings is destructive", got[0].Issue)
		}
	})

	t.Run("uppercase UUID fails", func(t *testing.T) {
		got := Check(mk("host", "8B861704-05BC-4382-B057-7EAC3DF5E730"))
		if len(got) != 1 || got[0].Advisory {
			t.Fatalf("problems = %v, want one non-advisory problem", got)
		}
	})

	t.Run("the contract rendering passes", func(t *testing.T) {
		if got := Check(mk("host", "8b861704-05bc-4382-b057-7eac3df5e730")); len(got) != 0 {
			t.Fatalf("problems = %v, want none", got)
		}
	})

	t.Run("checked wherever host.id appears", func(t *testing.T) {
		// compute.vm carries the hypervisor's host.id, and a host-local
		// network.endpoint carries the observing host's as a fourth key. Same key,
		// same rule — a per-type check would have missed both.
		for _, typ := range []string{"compute.vm", "network.endpoint"} {
			if got := Check(mk(typ, "8b86170405bc4382b0577eac3df5e730")); len(got) != 1 {
				t.Errorf("%s: problems = %v, want the spelling flagged", typ, got)
			}
		}
	})

	t.Run("checked on a relationship target", func(t *testing.T) {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr("service.instance.id", "agent-1")
		lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		lr.Attributes().PutStr("entity.type", "process")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("process.pid", "100")
		d := lr.Attributes().PutEmptySlice("entity.relationships").AppendEmpty().SetEmptyMap()
		d.PutStr("relationship.type", "runs_on")
		d.PutStr("entity.type", "host")
		d.PutEmptyMap("entity.id").PutStr("host.id", "8b86170405bc4382b0577eac3df5e730")

		got := Check(ld)
		if len(got) != 1 {
			t.Fatalf("problems = %v, want the target spelling flagged", got)
		}
		if !strings.Contains(got[0].Record, "entity.relationships[0]") {
			t.Errorf("record = %q, want the descriptor located", got[0].Record)
		}
	})

	t.Run("a non-machine-id host.id is left alone", func(t *testing.T) {
		// A cloud instance id is a legitimate host.id and looks nothing like a
		// machine-id; flagging it would make the check noise.
		for _, id := range []string{"i-0abcdef1234567890", "h-1", "3990048576770958836"} {
			if got := Check(mk("host", id)); len(got) != 0 {
				t.Errorf("%q: problems = %v, want none", id, got)
			}
		}
	})
}

// TestCheckFlagsMACRendering pins the second value whose rendering is normative
// (#459). A MAC identifies a network.neighbor and is the "mac:" rung of a
// network.device.id, and it is what a same_as belief joins on when SNMP later
// names the device a neighbor was first seen as: two renderings of the same six
// bytes are two entities, and the join that would have united them finds nothing.
func TestCheckFlagsMACRendering(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   string
		flagged bool
	}{
		{"canonical", "mac", "AA-BB-CC-DD-EE-FF", false},
		{"lowercase hyphens", "mac", "aa-bb-cc-dd-ee-ff", true},
		{"colon form", "mac", "AA:BB:CC:DD:EE:FF", true},
		{"lowercase colon form, what a reader writes by hand", "mac", "aa:bb:cc:dd:ee:ff", true},
		{"no separator", "mac", "AABBCCDDEEFF", true},
		{"too short", "mac", "AA-BB-CC", true},
		{"device id, mac rung canonical", "network.device.id", "mac:AA-BB-CC-DD-EE-FF", false},
		{"device id, mac rung lowercase", "network.device.id", "mac:aa:bb:cc:dd:ee:ff", true},
		// The other rungs are serials, engine ids and names: judging them against
		// the MAC form would reject valid identities.
		{"device id, serial rung", "network.device.id", "serial:9:FOC1234X5YZ", false},
		{"device id, engine rung", "network.device.id", "engine:800000090300aabbccddeeff", false},
		{"device id, name rung", "network.device.id", "name:sw-core-01", false},
	}
	for _, c := range cases {
		logs := plog.NewLogs()
		lr := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		a := lr.Attributes()
		a.PutStr("entity.type", "network.neighbor")
		a.PutEmptyMap("entity.id").PutStr(c.key, c.value)

		var hit bool
		for _, p := range Check(logs) {
			if strings.Contains(p.Issue, "IEEE RA hexadecimal") {
				hit = true
				if p.Advisory {
					t.Errorf("%s: the MAC rendering must be a rejection, not an advisory", c.name)
				}
			}
		}
		if hit != c.flagged {
			t.Errorf("%s (%s=%q): flagged=%v, want %v", c.name, c.key, c.value, hit, c.flagged)
		}
	}
}

// TestCheckFlagsRetiredIdentitySpelling pins the split that makes this check
// meaningful, and the negative control is the half that matters.
//
// `interface.name` is deliberately still accepted — as a display name and as a
// telemetry join key — because a graph holds interfaces observed before the
// semconv spelling existed. Tolerance there reads as tolerance everywhere, and
// it must not be: an identity is hashed over the keys AS SENT, with no
// normalization, so one port named two ways is two entities joined by nothing.
//
// So the check must fire on the identity and stay silent on the description. A
// version that flagged both would be refused by every producer that correctly
// carries the old spelling as an attribute, and the check would be turned off.
func TestCheckFlagsRetiredIdentitySpelling(t *testing.T) {
	const marker = "no longer an identity key"
	cases := []struct {
		name    string
		inIdent map[string]string
		inDesc  map[string]string
		flagged bool
	}{
		{
			name:    "the semconv spelling identifies",
			inIdent: map[string]string{"host.id": "h-1", "network.interface.name": "eth0"},
			flagged: false,
		},
		{
			name:    "the retired spelling must not identify",
			inIdent: map[string]string{"host.id": "h-1", "interface.name": "eth0"},
			flagged: true,
		},
		{
			name:    "a port on a device, retired spelling",
			inIdent: map[string]string{"network.device.id": "serial:9:FOC1", "interface.name": "Gi1/0/1"},
			flagged: true,
		},
		{
			// The negative control: the same key as a DESCRIPTIVE attribute is
			// correct and must pass. This is why the check is on identity only.
			name:    "the retired spelling is fine as an attribute",
			inIdent: map[string]string{"host.id": "h-1", "network.interface.name": "eth0"},
			inDesc:  map[string]string{"interface.name": "eth0", "mtu": "1500"},
			flagged: false,
		},
	}
	for _, c := range cases {
		logs := plog.NewLogs()
		lr := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		a := lr.Attributes()
		a.PutStr("entity.type", "network.interface")
		id := a.PutEmptyMap("entity.id")
		for k, v := range c.inIdent {
			id.PutStr(k, v)
		}
		if len(c.inDesc) > 0 {
			d := a.PutEmptyMap("entity.description")
			for k, v := range c.inDesc {
				d.PutStr(k, v)
			}
		}

		var hit bool
		for _, p := range Check(logs) {
			if strings.Contains(p.Issue, marker) {
				hit = true
				if p.Advisory {
					t.Errorf("%s: a retired identity spelling must be a rejection, not an advisory — "+
						"the damage is a silent duplicate, which is exactly what an advisory does not prevent", c.name)
				}
			}
		}
		if hit != c.flagged {
			t.Errorf("%s: flagged=%v, want %v (identity %v, description %v)", c.name, hit, c.flagged, c.inIdent, c.inDesc)
		}
	}
}

// TestBeliefAttributes pins the 0.23 belief contract on both relation types
// that carry one.
//
// Two separate failures hide here, and the producer cannot tell them apart from
// the outside. On same_as, a confidence the overlay cannot read leaves a stored
// edge that collapses nothing. On resolves_to, a perfectly readable confidence
// is stored and consulted by no decision at all, because every reader of the
// attribute gates on same_as. Both end in a stated belief that changes no
// answer, which is why one blocks and the other is said out loud.
func TestBeliefAttributes(t *testing.T) {
	mk := func(relType string, build func(d pcommon.Map)) plog.Logs {
		ld := plog.NewLogs()
		rl := ld.ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr("service.instance.id", "p1") // avoid the unrelated advisory
		lr := rl.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
		lr.SetEventName("entity.state")
		lr.Attributes().PutStr("entity.type", "host")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h1")
		d := lr.Attributes().PutEmptySlice("entity.relationships").AppendEmpty().SetEmptyMap()
		d.PutStr("relationship.type", relType)
		d.PutStr("entity.type", "cloud.resource")
		d.PutEmptyMap("entity.id").PutStr("azure.resource.id", "/subscriptions/x/rg/y")
		build(d)
		return ld
	}

	find := func(problems []Problem, advisory bool, substr string) bool {
		for _, p := range problems {
			if p.Advisory == advisory && strings.Contains(p.Issue, substr) {
				return true
			}
		}
		return false
	}

	// A contractual basis at its contractual confidence: nothing to say.
	exact := mk(wire.RelTypeSameAs, func(d pcommon.Map) {
		d.PutStr(wire.RelBasis, wire.BasisVMIDMatch)
		d.PutDouble(wire.RelConfidence, 1.0)
	})
	if problems := Check(exact); len(problems) != 0 {
		t.Errorf("an exact-match basis at 1.0 was flagged: %v", problems)
	}

	// The same basis below its contractual value: it claims an exact identity
	// match and then asks the reader to doubt it. Blocks, because under the
	// default threshold of 0.9 a value like this one collapses nothing while
	// reading as a deliberate belief.
	doubted := mk(wire.RelTypeSameAs, func(d pcommon.Map) {
		d.PutStr(wire.RelBasis, wire.BasisVMIDMatch)
		d.PutDouble(wire.RelConfidence, 0.8)
	})
	if !find(Check(doubted), false, "contractual confidence") {
		t.Errorf("basis %s at 0.8 must block: %v", wire.BasisVMIDMatch, Check(doubted))
	}

	// An unknown basis is left alone: the vocabulary is open and weaker evidence
	// legitimately carries a lower value.
	weak := mk(wire.RelTypeSameAs, func(d pcommon.Map) {
		d.PutStr(wire.RelBasis, "hyperv-kvp")
		d.PutDouble(wire.RelConfidence, 0.6)
	})
	for _, p := range Check(weak) {
		if !p.Advisory {
			t.Errorf("an open-vocabulary basis below the threshold must not block: %v", p)
		}
	}

	// No basis at all: usable, but a reader cannot tell what the belief rests on.
	noBasis := mk(wire.RelTypeSameAs, func(d pcommon.Map) {
		d.PutDouble(wire.RelConfidence, 1.0)
	})
	if !find(Check(noBasis), true, "no basis") {
		t.Errorf("a same_as without basis should advise: %v", Check(noBasis))
	}

	// resolves_to with a confidence: parsed, stored, read by nothing.
	resolves := mk(wire.RelTypeResolvesTo, func(d pcommon.Map) {
		d.PutStr(wire.RelBasis, "lldp")
		d.PutDouble(wire.RelConfidence, 1.0)
	})
	if !find(Check(resolves), true, "read by nothing") {
		t.Errorf("a resolves_to confidence should advise that nothing reads it: %v", Check(resolves))
	}
	for _, p := range Check(resolves) {
		if !p.Advisory {
			t.Errorf("resolves_to must not block: %v", p)
		}
	}

	// resolves_to without a confidence is silent: the basis alone is the useful
	// half, and demanding a value nothing reads would be worse than saying
	// nothing.
	bare := mk(wire.RelTypeResolvesTo, func(d pcommon.Map) {
		d.PutStr(wire.RelBasis, "arp")
	})
	if problems := Check(bare); len(problems) != 0 {
		t.Errorf("a resolves_to with a basis and no confidence was flagged: %v", problems)
	}
}
