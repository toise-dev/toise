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
	"strings"
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
	"github.com/toise-dev/toise/internal/store"
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
