package conformance

import (
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
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
		d.PutEmptyMap("entity.id").PutStr("mac", "aa:bb")
		d.PutDouble("confidence", 0.9)
	})
	for _, p := range Check(okSameAs) {
		if !p.Advisory {
			t.Errorf("valid same_as flagged: %v", p)
		}
	}

	// A same_as edge missing confidence is an advisory (inert belief), not a
	// rejection.
	noConf := mk(func(lr plog.LogRecord) {
		lr.Attributes().PutStr("entity.type", "network.device")
		lr.Attributes().PutEmptyMap("entity.id").PutStr("name", "sw1")
		d := lr.Attributes().PutEmptySlice("entity.relationships").AppendEmpty().SetEmptyMap()
		d.PutStr("relationship.type", "same_as")
		d.PutStr("entity.type", "network.device")
		d.PutEmptyMap("entity.id").PutStr("mac", "aa:bb")
	})
	foundAdvisory := false
	for _, p := range Check(noConf) {
		if p.Advisory && strings.Contains(p.Issue, "no confidence") {
			foundAdvisory = true
		}
		if !p.Advisory {
			t.Errorf("same_as without confidence must not be a rejection: %v", p)
		}
	}
	if !foundAdvisory {
		t.Error("same_as without confidence should raise an advisory")
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
