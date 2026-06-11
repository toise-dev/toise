package conformance

import (
	"strings"
	"testing"

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

	// A plain log record is not an entity event: ignored, like Toise does.
	plain := plog.NewLogs()
	plain.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr("hello")
	if problems := Check(plain); len(problems) != 0 {
		t.Errorf("plain log record flagged: %v", problems)
	}
}
