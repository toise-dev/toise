package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/registry"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

const bootToken = "boot-test-token"

func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

// TestServerBoot is the boot-level black-box test (#114): it runs the real
// run() assembly — auth middleware and its public-paths whitelist, tenant
// routing, --production lockdown, ops endpoints, the OTLP receiver, and the
// maintenance loops — and asserts the composition end-to-end, then shuts down
// via SIGTERM while the maintenance tickers are actively firing (#112).
func TestServerBoot(t *testing.T) {
	httpAddr, otlpAddr := freeAddr(t), freeAddr(t)
	cfg, err := config.Load([]string{
		"--listen", httpAddr,
		"--otlp-listen", otlpAddr,
		"--data-dir", t.TempDir(),
		"--production",
		"--liveness-sweep-interval", "50ms",
		"--retention-compaction-interval", "50ms",
		"--snapshot-interval", "50ms",
		"--retention-max-age", "24h",
	}, func(k string) string {
		if k == "TOISE_AUTH_TOKENS" {
			return bootToken
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	storeCfg := store.DefaultConfig()
	storeCfg.RetentionMaxAge = cfg.RetentionMaxAge.D()
	storeCfg.CompactionInterval = cfg.CompactionInterval.D()

	done := make(chan error, 1)
	go func() { done <- run(cfg, storeCfg, slog.New(slog.DiscardHandler)) }()

	base := "http://" + httpAddr
	waitReady(t, base+"/readyz")

	// OTLP ingest: one entity in the default tenant, one in tenant "acme"
	// (X-Scope-OrgID), both with the bearer token (gRPC interceptor is on).
	exportEntity(t, otlpAddr, "", "h-default")
	exportEntity(t, otlpAddr, "acme", "h-acme")

	// GraphQL per tenant: each tenant sees exactly its own entity.
	if n := gqlTotal(t, base, ""); n != 1 {
		t.Errorf("default tenant totalCount = %d, want 1", n)
	}
	if n := gqlTotal(t, base, "acme"); n != 1 {
		t.Errorf("acme tenant totalCount = %d, want 1", n)
	}

	// Auth: the data surface rejects unauthenticated calls; ops stay public.
	if code := httpCode(t, base+"/graphql", "POST", `{"query":"{ entities { totalCount } }"}`, false); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /graphql = %d, want 401", code)
	}
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if code := httpCode(t, base+path, "GET", "", false); code != http.StatusOK {
			t.Errorf("unauthenticated %s = %d, want 200", path, code)
		}
	}

	// The ingest counters are on /metrics end-to-end (#113).
	body := httpBody(t, base+"/metrics")
	for _, m := range []string{"toise_ingest_exports_total", "toise_ingest_records_total", "toise_auth_failures_total"} {
		if !strings.Contains(body, m) {
			t.Errorf("/metrics missing %s", m)
		}
	}

	// --production hides the development surfaces.
	if code := httpCode(t, base+"/playground", "GET", "", true); code != http.StatusNotFound {
		t.Errorf("/playground under --production = %d, want 404", code)
	}
	if code := httpCode(t, base+"/", "GET", "", true); code != http.StatusNotFound {
		t.Errorf("debug UI under --production = %d, want 404", code)
	}

	// MCP initialize handshake answers 200 with the token.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"boot-test","version":"0"}}}`
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bootToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("mcp initialize = %d, want 200", resp.StatusCode)
	}

	// SIGTERM while the 50ms maintenance tickers are firing: the loops must be
	// joined before the stores close — no "panic: pebble: closed" (#112).
	time.Sleep(120 * time.Millisecond) // let every ticker fire at least once
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("sigterm: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() returned %v after SIGTERM, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of SIGTERM")
	}
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become ready within 5s")
}

func exportEntity(t *testing.T, otlpAddr, tenantID, hostID string) {
	t.Helper()
	conn, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial otlp: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	lr := sl.LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	lr.SetEventName("entity.state")
	a := lr.Attributes()
	a.PutStr("entity.type", "host")
	a.PutEmptyMap("entity.id").PutStr("host.id", hostID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+bootToken)
	if tenantID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-scope-orgid", tenantID)
	}
	if _, err := plogotlp.NewGRPCClient(conn).Export(ctx, plogotlp.NewExportRequestFromLogs(ld)); err != nil {
		t.Fatalf("otlp export (tenant %q): %v", tenantID, err)
	}
}

func gqlTotal(t *testing.T, base, tenantID string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/graphql", bytes.NewReader([]byte(`{"query":"{ entities { totalCount } }"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bootToken)
	if tenantID != "" {
		req.Header.Set("X-Scope-OrgID", tenantID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("graphql: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data struct {
			Entities struct {
				TotalCount int `json:"totalCount"`
			} `json:"entities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode graphql response: %v", err)
	}
	return out.Data.Entities.TotalCount
}

func httpCode(t *testing.T, url, method, body string, withAuth bool) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+bootToken)
	}
	resp, rerr := http.DefaultClient.Do(req)
	if rerr != nil {
		t.Fatalf("%s %s: %v", method, url, rerr)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func httpBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCheckpointSubcommand pins the operator backup path (#115): every tenant
// is checkpointed into <dst>/<tenant>, and the copy is a complete, replayable
// store.
func TestCheckpointSubcommand(t *testing.T) {
	dataDir := t.TempDir()
	reg, err := registry.Open(dataDir, store.DefaultConfig(), 0, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	st, err := reg.For("acme")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1_700_000_000, 0).UTC()
	ev := model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityCreated,
		Entity: model.Entity{ID: "e1", Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}},
		EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
	if aerr := st.Store.Append(ev); aerr != nil {
		t.Fatal(aerr)
	}
	if cerr := reg.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	dst := filepath.Join(t.TempDir(), "backup")
	if rerr := runCheckpoint([]string{"--data-dir", dataDir, dst}, func(string) string { return "" }); rerr != nil {
		t.Fatalf("runCheckpoint: %v", rerr)
	}

	restored, err := store.Open(filepath.Join(dst, "acme"), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open checkpoint: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	g := projection.New()
	if err := g.Replay(restored); err != nil {
		t.Fatalf("replay checkpoint: %v", err)
	}
	if g.EntityCount() != 1 {
		t.Errorf("restored EntityCount = %d, want 1", g.EntityCount())
	}

	if err := runCheckpoint([]string{"--data-dir", dataDir}, func(string) string { return "" }); err == nil {
		t.Error("missing destination must error with usage")
	}
}

// TestCheckpointRefusesMissingDataDir pins #162: a typo'd --data-dir (or a
// wrong cwd) must be a hard error, not a freshly minted empty store backed up
// with exit 0.
func TestCheckpointRefusesMissingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "no-such-dir")
	dst := filepath.Join(t.TempDir(), "backup")
	if err := runCheckpoint([]string{"--data-dir", dataDir, dst}, func(string) string { return "" }); err == nil {
		t.Fatal("checkpoint of a missing data dir must error")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("checkpoint created the data dir %s (stat err = %v)", dataDir, err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("checkpoint created the destination %s (stat err = %v)", dst, err)
	}
}

// TestCheckpointRefusesEmptyDataDir pins #162: a data dir holding no tenant
// stores means the operator pointed at the wrong place — backing up nothing
// must not look like success.
func TestCheckpointRefusesEmptyDataDir(t *testing.T) {
	dataDir := t.TempDir()
	dst := filepath.Join(t.TempDir(), "backup")
	err := runCheckpoint([]string{"--data-dir", dataDir, dst}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "no tenant stores") {
		t.Fatalf("err = %v, want a no-tenant-stores refusal", err)
	}
	ents, rerr := os.ReadDir(dataDir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(ents) != 0 {
		t.Errorf("checkpoint left %d entries in the data dir", len(ents))
	}
}

// TestCheckpointLeavesSourceUntouched pins the read-only contract of #162:
// checkpointing must not mint a default tenant in the source nor write to it
// (the format stamp included) — the source listing is identical before and
// after, and only the persisted tenant is backed up.
func TestCheckpointLeavesSourceUntouched(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "acme"), store.DefaultConfig())
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	when := time.Unix(1_700_000_000, 0).UTC()
	ev := model.Event{Entity: &model.EntityEvent{
		EventID: model.NewEventID(), ChangeType: model.EntityCreated,
		Entity: model.Entity{ID: "e1", Type: model.TypeHost,
			Identity: []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}}},
		EventTime: when, RecordedAt: when, SchemaVersion: model.SchemaVersion,
	}}
	if aerr := st.Append(ev); aerr != nil {
		t.Fatal(aerr)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	before := dirListing(t, dataDir)

	dst := filepath.Join(t.TempDir(), "backup")
	if rerr := runCheckpoint([]string{"--data-dir", dataDir, dst}, func(string) string { return "" }); rerr != nil {
		t.Fatalf("runCheckpoint: %v", rerr)
	}

	if after := dirListing(t, dataDir); before != after {
		t.Errorf("source modified by checkpoint:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, serr := os.Stat(filepath.Join(dataDir, "default")); !os.IsNotExist(serr) {
		t.Errorf("checkpoint minted a default tenant in the source (stat err = %v)", serr)
	}

	restored, err := store.Open(filepath.Join(dst, "acme"), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open checkpoint: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	g := projection.New()
	if err := g.Replay(restored); err != nil {
		t.Fatalf("replay checkpoint: %v", err)
	}
	if g.EntityCount() != 1 {
		t.Errorf("restored EntityCount = %d, want 1", g.EntityCount())
	}
}

// TestCheckpointHonorsConfigFile pins #162's second aggravator: the shipped
// systemd setup puts data_dir in the YAML config, so the subcommand must
// resolve it through the same layers as the server.
func TestCheckpointHonorsConfigFile(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "acme"), store.DefaultConfig())
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	cfgPath := filepath.Join(t.TempDir(), "toise-server.yaml")
	if werr := os.WriteFile(cfgPath, []byte("data_dir: "+dataDir+"\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}

	dst := filepath.Join(t.TempDir(), "backup")
	if rerr := runCheckpoint([]string{"--config", cfgPath, dst}, func(string) string { return "" }); rerr != nil {
		t.Fatalf("runCheckpoint with --config: %v", rerr)
	}
	if _, serr := os.Stat(filepath.Join(dst, "acme")); serr != nil {
		t.Errorf("config-file data dir not honored: %v", serr)
	}

	env := func(k string) string {
		if k == "TOISE_CONFIG" {
			return cfgPath
		}
		return ""
	}
	dst2 := filepath.Join(t.TempDir(), "backup-env")
	if rerr := runCheckpoint([]string{dst2}, env); rerr != nil {
		t.Fatalf("runCheckpoint with TOISE_CONFIG: %v", rerr)
	}
	if _, serr := os.Stat(filepath.Join(dst2, "acme")); serr != nil {
		t.Errorf("TOISE_CONFIG data dir not honored: %v", serr)
	}
}

// dirListing returns a recursive name+size listing of dir, to assert a
// directory was not modified.
func dirListing(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if info.IsDir() {
			b.WriteString(rel + "/\n")
			return nil
		}
		b.WriteString(rel + " " + strconv.FormatInt(info.Size(), 10) + "\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return b.String()
}

// TestShutdownWithOpenStream pins #130: a deploy with a connected streaming
// client (the MCP SSE listening stream here) must still exit clean — before
// the fix, Shutdown waited its full grace for streams that never drain and
// run() returned "context deadline exceeded" (observed on every real deploy
// with a connected MCP client).
func TestShutdownWithOpenStream(t *testing.T) {
	httpAddr, otlpAddr := freeAddr(t), freeAddr(t)
	cfg, err := config.Load([]string{
		"--listen", httpAddr,
		"--otlp-listen", otlpAddr,
		"--data-dir", t.TempDir(),
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- run(cfg, store.DefaultConfig(), slog.New(slog.DiscardHandler)) }()
	base := "http://" + httpAddr
	waitReady(t, base+"/readyz")

	// Open a real MCP session and its GET listening stream, and keep it open
	// across the shutdown.
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	ireq, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(initBody))
	ireq.Header.Set("Content-Type", "application/json")
	ireq.Header.Set("Accept", "application/json, text/event-stream")
	iresp, err := http.DefaultClient.Do(ireq)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	_, _ = io.Copy(io.Discard, iresp.Body)
	_ = iresp.Body.Close()
	sid := iresp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no session id")
	}
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	greq, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, base+"/mcp", nil)
	greq.Header.Set("Accept", "text/event-stream")
	greq.Header.Set("Mcp-Session-Id", sid)
	gresp, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer func() { _ = gresp.Body.Close() }()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("SSE stream = %d, want 200", gresp.StatusCode)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() with an open stream returned %v after SIGTERM, want nil (#130)", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run() did not return within 15s of SIGTERM")
	}
}

// syncWriter is a goroutine-safe log sink: run()'s servers log concurrently.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestShutdownWritesFinalSnapshot pins #164: producer references acquired
// since the last periodic snapshot must survive a graceful SIGTERM. With the
// ticker at 1h it never fires inside the test, so the only possible snapshot
// is the one the shutdown path writes.
func TestShutdownWritesFinalSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	httpAddr, otlpAddr := freeAddr(t), freeAddr(t)
	cfg, err := config.Load([]string{
		"--listen", httpAddr,
		"--otlp-listen", otlpAddr,
		"--data-dir", dataDir,
		"--snapshot-interval", "1h",
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- run(cfg, store.DefaultConfig(), slog.New(slog.DiscardHandler)) }()
	waitReady(t, "http://"+httpAddr+"/readyz")
	exportEntity(t, otlpAddr, "", "h-final")

	if kerr := syscall.Kill(os.Getpid(), syscall.SIGTERM); kerr != nil {
		t.Fatal(kerr)
	}
	select {
	case rerr := <-done:
		if rerr != nil {
			t.Fatalf("run() returned %v after SIGTERM, want nil", rerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of SIGTERM")
	}

	st, err := store.Open(filepath.Join(dataDir, tenant.Default), store.DefaultConfig())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, events, liveness, ok, err := st.ReadSnapshot()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !ok {
		t.Fatal("no snapshot after graceful shutdown: refs since the last periodic snapshot are lost")
	}
	if len(events) != 1 {
		t.Errorf("snapshot events = %d, want 1", len(events))
	}
	if len(liveness) == 0 {
		t.Error("final snapshot carries no liveness memento")
	}
}

// TestWarnsWhenSweepingWithoutSnapshots pins #164: sweeping without snapshots
// means the liveness backstop silently forgets every producer at each
// restart — the combination deserves a loud startup line.
func TestWarnsWhenSweepingWithoutSnapshots(t *testing.T) {
	httpAddr, otlpAddr := freeAddr(t), freeAddr(t)
	cfg, err := config.Load([]string{
		"--listen", httpAddr,
		"--otlp-listen", otlpAddr,
		"--data-dir", t.TempDir(),
		"--snapshot-interval", "0",
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.LivenessSweepInterval.D() <= 0 {
		t.Fatal("precondition: sweeping must be on by default")
	}
	logs := &syncWriter{}
	done := make(chan error, 1)
	go func() { done <- run(cfg, store.DefaultConfig(), slog.New(slog.NewTextHandler(logs, nil))) }()
	waitReady(t, "http://"+httpAddr+"/readyz")

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() returned %v after SIGTERM, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of SIGTERM")
	}
	if !strings.Contains(logs.String(), "snapshots are disabled") {
		t.Error("no startup warning for sweeping-on/snapshots-off")
	}
}
