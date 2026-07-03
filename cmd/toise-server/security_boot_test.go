package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

// gqlTotalAs queries entities.totalCount with an explicit bearer token and an
// optional X-Scope-OrgID header (empty = none), so a test can probe how the
// server resolves a request's tenant.
func gqlTotalAs(t *testing.T, base, token, tenantHeader string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/graphql", bytes.NewReader([]byte(`{"query":"{ entities { totalCount } }"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if tenantHeader != "" {
		req.Header.Set("X-Scope-OrgID", tenantHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("graphql: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graphql status = %d, want 200 (token %q, header %q)", resp.StatusCode, token, tenantHeader)
	}
	var out struct {
		Data struct {
			Entities struct {
				TotalCount int `json:"totalCount"`
			} `json:"entities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode graphql: %v", err)
	}
	return out.Data.Entities.TotalCount
}

// exportEntityAs exports one entity.state with an explicit bearer token and an
// optional x-scope-orgid, returning any export error (not fatal) so a test can
// assert acceptance or rejection.
func exportEntityAs(otlpAddr, token, tenantHeader, hostID string) error {
	conn, err := grpc.NewClient(otlpAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	lr.SetEventName("entity.state")
	a := lr.Attributes()
	a.PutStr("entity.type", "host")
	a.PutEmptyMap("entity.id").PutStr("host.id", hostID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	if tenantHeader != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-scope-orgid", tenantHeader)
	}
	_, err = plogotlp.NewGRPCClient(conn).Export(ctx, plogotlp.NewExportRequestFromLogs(ld))
	return err
}

// TestRunDeriveOnlyTenantSecurity boots the full server through run() with the
// ADR 0028 tier-2 composition wired — a global token, a tenant-scoped token, and
// tenant_trust_mode=derive-only with an audit log — and asserts the anti-spoofing
// guarantees that composition exists to provide. This is the run()-level security
// coverage the 360 review (C4-1) flagged as entirely absent: the pieces are unit
// tested, but nothing proved run() assembles them so a scoped token cannot reach
// another tenant by spoofing X-Scope-OrgID.
func TestRunDeriveOnlyTenantSecurity(t *testing.T) {
	httpAddr, otlpAddr := freeAddr(t), freeAddr(t)
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	const globalTok = "global-full-token"
	const acmeTok = "acme-scoped-token"

	cfg, err := config.Load([]string{
		"--listen", httpAddr,
		"--otlp-listen", otlpAddr,
		"--data-dir", t.TempDir(),
		"--production",
		"--tenant-trust-mode", "derive-only",
		"--audit-log", auditPath,
	}, func(k string) string {
		switch k {
		case "TOISE_AUTH_TOKENS":
			return globalTok
		case "TOISE_TENANT_TOKENS":
			return "acme:" + acmeTok
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	storeCfg := store.DefaultConfig()

	done := make(chan error, 1)
	go func() { done <- run(cfg, storeCfg, slog.New(slog.DiscardHandler)) }()
	base := "http://" + httpAddr
	waitReady(t, base+"/readyz")

	// The audit branch of run() is wired: enabling it creates the file.
	if _, serr := os.Stat(auditPath); serr != nil {
		t.Errorf("audit log not created by run(): %v", serr)
	}

	// Seed with the GLOBAL token, choosing the tenant by header: acme gets 1
	// entity, victim gets 2 — distinct counts so a leak is unambiguous.
	for _, seed := range []struct{ tenant, host string }{
		{"acme", "h-acme-1"}, {"victim", "h-victim-1"}, {"victim", "h-victim-2"},
	} {
		if eerr := exportEntityAs(otlpAddr, globalTok, seed.tenant, seed.host); eerr != nil {
			t.Fatalf("seed export to %s: %v", seed.tenant, eerr)
		}
	}

	// Anti-spoofing on the read surface: the acme-scoped token presenting a
	// spoofed victim header still reads ACME (1), never victim's 2. If derive-only
	// were not wired in run(), the header would win and this would be 2.
	if n := gqlTotalAs(t, base, acmeTok, "victim"); n != 1 {
		t.Errorf("scoped token + spoofed header read %d entities, want 1 (acme) — derive-only not enforced", n)
	}
	if n := gqlTotalAs(t, base, acmeTok, ""); n != 1 {
		t.Errorf("scoped token (no header) read %d, want 1 (its own tenant)", n)
	}

	// The global token is not tenant-bound: it crosses tenants by header.
	if n := gqlTotalAs(t, base, globalTok, "acme"); n != 1 {
		t.Errorf("global token on acme read %d, want 1", n)
	}
	if n := gqlTotalAs(t, base, globalTok, "victim"); n != 2 {
		t.Errorf("global token on victim read %d, want 2 (global crosses tenants)", n)
	}

	// Anti-spoofing on ingest: the acme-scoped token exporting with a spoofed
	// victim header lands in ACME (its derived, locked tenant), not victim.
	if eerr := exportEntityAs(otlpAddr, acmeTok, "victim", "h-acme-2"); eerr != nil {
		t.Fatalf("scoped ingest export: %v", eerr)
	}
	if n := gqlTotalAs(t, base, globalTok, "acme"); n != 2 {
		t.Errorf("acme has %d entities after scoped ingest, want 2 — derive-only did not lock the ingest tenant", n)
	}
	if n := gqlTotalAs(t, base, globalTok, "victim"); n != 2 {
		t.Errorf("victim has %d entities, want 2 — a spoofed scoped ingest leaked into victim", n)
	}

	// Clean shutdown.
	if kerr := syscall.Kill(os.Getpid(), syscall.SIGTERM); kerr != nil {
		t.Fatalf("sigterm: %v", kerr)
	}
	select {
	case rerr := <-done:
		if rerr != nil {
			t.Fatalf("run() returned %v after SIGTERM, want nil", rerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not return within 10s of SIGTERM")
	}
}
