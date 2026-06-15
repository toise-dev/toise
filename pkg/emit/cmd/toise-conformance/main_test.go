package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"

	"github.com/toise-dev/toise/pkg/emit"
)

func fixedClock() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

// conformantProto returns the OTLP protobuf bytes of a clean entity.state batch
// produced by the SDK, including a service.instance.id (no advisory).
func conformantProto(t *testing.T) []byte {
	t.Helper()
	c, err := emit.New(emit.Options{
		Endpoint:          "127.0.0.1:0",
		ServiceName:       "test-producer",
		ServiceInstanceID: "producer-01",
	}.WithClock(fixedClock))
	if err != nil {
		t.Fatalf("emit.New: %v", err)
	}
	defer func() { _ = c.Close() }()
	ld, err := c.Build("entity.state", []emit.Entity{
		{Type: "host", ID: map[string]string{"host.id": "h1"}, Attributes: map[string]string{"status": "up"}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestRunConformant(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-"}, bytes.NewReader(conformantProto(t)), &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s out=%s", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "conformant") {
		t.Errorf("want a conformant message, got %q", out.String())
	}
}

func TestRunRejection(t *testing.T) {
	// An entity.state record missing entity.type — a rejection.
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("entity.state")
	lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h1")
	b, err := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := run(nil, bytes.NewReader(b), &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "rejection") || !strings.Contains(out.String(), "entity.type") {
		t.Errorf("want a rejection naming entity.type, got %q", out.String())
	}
}

func TestRunAdvisoryStrict(t *testing.T) {
	// Clean records but no service.instance.id => advisory only.
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("entity.state")
	lr.Attributes().PutStr("entity.type", "host")
	lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h1")
	b, err := plogotlp.NewExportRequestFromLogs(ld).MarshalProto()
	if err != nil {
		t.Fatal(err)
	}

	// Default: advisory does not fail.
	var out, errBuf bytes.Buffer
	if code := run(nil, bytes.NewReader(b), &out, &errBuf); code != 0 {
		t.Fatalf("advisory-only exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "advisory") {
		t.Errorf("want an advisory reported, got %q", out.String())
	}

	// -strict turns the advisory into a failure.
	out.Reset()
	if code := run([]string{"-strict"}, bytes.NewReader(b), &out, &errBuf); code != 1 {
		t.Errorf("advisory under -strict exit = %d, want 1", code)
	}
}

func TestRunJSON(t *testing.T) {
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetEventName("entity.state")
	lr.Attributes().PutStr("entity.type", "host")
	lr.Attributes().PutEmptyMap("entity.id").PutStr("host.id", "h1")
	b, err := plogotlp.NewExportRequestFromLogs(ld).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	// auto-detect should treat a leading '{' as JSON.
	if code := run(nil, bytes.NewReader(b), &out, &errBuf); code != 0 {
		t.Fatalf("json auto-detect exit = %d, want 0; stderr=%s", code, errBuf.String())
	}
}

func TestRunBadInput(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run(nil, bytes.NewReader(nil), &out, &errBuf); code != 2 {
		t.Errorf("empty input exit = %d, want 2", code)
	}
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-format", "proto"}, bytes.NewReader([]byte("not protobuf")), &out, &errBuf); code != 2 {
		t.Errorf("garbage proto exit = %d, want 2", code)
	}
}
