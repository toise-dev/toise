package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDisabledIsNoOp(t *testing.T) {
	// A nil sink disables auditing; a nil *Auditor must also be a safe no-op.
	if a := New(nil, nil); a != nil {
		t.Fatalf("New(nil) = %v, want nil (disabled)", a)
	}
	var a *Auditor
	if a.Enabled() {
		t.Error("nil *Auditor must report disabled")
	}
	a.Record(Event{Action: "annotate_entity"}) // must not panic
}

func TestRecordWritesJSONLine(t *testing.T) {
	var buf bytes.Buffer
	a := New(&buf, nil)
	if !a.Enabled() {
		t.Fatal("auditor with a sink must be enabled")
	}
	ts := time.Unix(1_700_000_000, 0).UTC()
	a.Record(Event{Time: ts, Tenant: "acme", Surface: "mcp", Action: "annotate_entity", Target: "host-1"})
	a.Record(Event{Time: ts, Tenant: "acme", Surface: "graphql", Action: "annotate_entity", Target: "host-2"})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d audit lines, want 2: %q", len(lines), buf.String())
	}
	var ev Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("audit line is not valid JSON: %v", err)
	}
	if ev.Tenant != "acme" || ev.Surface != "mcp" || ev.Action != "annotate_entity" || ev.Target != "host-1" || !ev.Time.Equal(ts) {
		t.Errorf("decoded event = %+v, want the recorded fields", ev)
	}
}

func TestRecordConcurrent(t *testing.T) {
	var buf bytes.Buffer
	a := New(&buf, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.Record(Event{Action: "annotate_entity", Target: "x"}) }()
	}
	wg.Wait()
	if n := strings.Count(buf.String(), "\n"); n != 50 {
		t.Errorf("got %d lines, want 50 (no interleaved/lost writes)", n)
	}
}
