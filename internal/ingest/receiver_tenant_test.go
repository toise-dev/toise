package ingest

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

// startRoutedReceiver wires a routed receiver that lazily opens one
// store+projection+engine per tenant under a temp dir, and returns the OTLP client
// plus a lookup of the per-tenant projection (nil for a tenant never touched).
func startRoutedReceiver(t *testing.T) (plogotlp.GRPCClient, func(tenant string) *projection.Graph) {
	t.Helper()
	base := t.TempDir()
	var mu sync.Mutex
	graphs := make(map[string]*projection.Graph)
	engines := make(map[string]*change.Engine)
	var stores []*store.Store

	engineFor := func(tenant string) (*change.Engine, error) {
		mu.Lock()
		defer mu.Unlock()
		if e, ok := engines[tenant]; ok {
			return e, nil
		}
		st, err := store.Open(filepath.Join(base, tenant), store.DefaultConfig())
		if err != nil {
			return nil, err
		}
		stores = append(stores, st)
		g := projection.New()
		engines[tenant] = change.New(g, st, change.WithClock(func() time.Time { return t0 }))
		graphs[tenant] = g
		return engines[tenant], nil
	}

	rec := NewRoutedReceiver(engineFor, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, st := range stores {
			_ = st.Close()
		}
	})

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	graphFor := func(tenant string) *projection.Graph {
		mu.Lock()
		defer mu.Unlock()
		return graphs[tenant]
	}
	return plogotlp.NewGRPCClient(conn), graphFor
}

func exportTenant(t *testing.T, c plogotlp.GRPCClient, orgID string, ld plog.Logs) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if orgID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-scope-orgid", orgID)
	}
	if _, err := c.Export(ctx, plogotlp.NewExportRequestFromLogs(ld)); err != nil {
		t.Fatalf("export: %v", err)
	}
}

func hostLogs(hostID string) plog.Logs {
	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": hostID}, nil)
	return ld
}

func hasHost(g *projection.Graph, hostID string) bool {
	if g == nil {
		return false
	}
	_, found := g.MatchIdentity(model.TypeHost, []model.KeyValue{{Key: "host.id", Value: model.StringValue(hostID)}})
	return found
}

// TestReceiverRoutesByOrgID is the core isolation guarantee: two tenants ingesting
// into one receiver get physically separate graphs, and a tenant never touched is
// never created.
func TestReceiverRoutesByOrgID(t *testing.T) {
	client, graphFor := startRoutedReceiver(t)

	exportTenant(t, client, "acme", hostLogs("h-acme"))
	exportTenant(t, client, "globex", hostLogs("h-globex"))

	if !hasHost(graphFor("acme"), "h-acme") {
		t.Error("tenant acme missing its own host")
	}
	if hasHost(graphFor("acme"), "h-globex") {
		t.Error("tenant acme leaked globex's host")
	}
	if !hasHost(graphFor("globex"), "h-globex") {
		t.Error("tenant globex missing its own host")
	}
	if hasHost(graphFor("globex"), "h-acme") {
		t.Error("tenant globex leaked acme's host")
	}
	if g := graphFor(tenant.Default); g != nil {
		t.Errorf("default tenant should never have been created, got %d entities", g.EntityCount())
	}
}

// TestReceiverNoOrgIDIsDefault: an export with no X-Scope-OrgID lands in the
// default tenant, preserving single-tenant behavior.
func TestReceiverNoOrgIDIsDefault(t *testing.T) {
	client, graphFor := startRoutedReceiver(t)

	exportTenant(t, client, "", hostLogs("h1"))

	if !hasHost(graphFor(tenant.Default), "h1") {
		t.Error("host with no X-Scope-OrgID did not land in the default tenant")
	}
}

// TestReceiverResourceAttrOverridesTenant: a single OTLP stream can carry several
// tenants — each ResourceLogs is routed by its own tenant.id resource attribute,
// overriding the request-level metadata.
func TestReceiverResourceAttrOverridesTenant(t *testing.T) {
	client, graphFor := startRoutedReceiver(t)

	ld := plog.NewLogs()
	rlA := ld.ResourceLogs().AppendEmpty()
	rlA.Resource().Attributes().PutStr(tenant.ResourceAttr, "acme")
	entityRecord(rlA.ScopeLogs().AppendEmpty(), evEntityState, model.TypeHost, map[string]string{"host.id": "h-acme"}, nil)
	rlB := ld.ResourceLogs().AppendEmpty()
	rlB.Resource().Attributes().PutStr(tenant.ResourceAttr, "globex")
	entityRecord(rlB.ScopeLogs().AppendEmpty(), evEntityState, model.TypeHost, map[string]string{"host.id": "h-globex"}, nil)

	// Request metadata points elsewhere; the resource attribute must win.
	exportTenant(t, client, "ignored", ld)

	if !hasHost(graphFor("acme"), "h-acme") {
		t.Error("resource attr tenant acme did not receive its host")
	}
	if !hasHost(graphFor("globex"), "h-globex") {
		t.Error("resource attr tenant globex did not receive its host")
	}
	if g := graphFor("ignored"); hasHost(g, "h-acme") || hasHost(g, "h-globex") {
		t.Error("records leaked into the request-metadata tenant despite resource-attr override")
	}
}

// TestReceiverInvalidOrgIDRejected: an X-Scope-OrgID that cannot be sanitized is a
// caller error, surfaced rather than silently coerced to default.
func TestReceiverInvalidOrgIDRejected(t *testing.T) {
	client, _ := startRoutedReceiver(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-scope-orgid", "../escape")
	if _, err := client.Export(ctx, plogotlp.NewExportRequestFromLogs(hostLogs("h1"))); err == nil {
		t.Error("expected an error for an invalid X-Scope-OrgID, got nil")
	}
}

// TestReceiverReconcilerIsolation guards the per-tenant embedded-relationship
// state: two tenants may assert the same source entity key, and one tenant
// dropping its embedded relation must not remove the other's.
func TestReceiverReconcilerIsolation(t *testing.T) {
	client, graphFor := startRoutedReceiver(t)

	runsOn := func() plog.Logs {
		ld := plog.NewLogs()
		sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
		entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
		embeddedEntity(sl, t0, model.TypeServiceInstance, map[string]string{"service.instance.id": "s1"},
			[]relDesc{{relType: model.RelRunsOn, toType: model.TypeHost, toID: map[string]string{"host.id": "h1"}}})
		return ld
	}
	exportTenant(t, client, "acme", runsOn())
	exportTenant(t, client, "globex", runsOn())

	if n := graphFor("acme").RelationCount(); n != 1 {
		t.Fatalf("acme relations = %d, want 1", n)
	}
	if n := graphFor("globex").RelationCount(); n != 1 {
		t.Fatalf("globex relations = %d, want 1", n)
	}

	// acme re-emits s1 without the relationship — its runs_on is removed.
	drop := plog.NewLogs()
	dsl := drop.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	embeddedEntity(dsl, t0.Add(time.Minute), model.TypeServiceInstance, map[string]string{"service.instance.id": "s1"}, nil)
	exportTenant(t, client, "acme", drop)

	if n := graphFor("acme").RelationCount(); n != 0 {
		t.Errorf("acme relations = %d after drop, want 0", n)
	}
	if n := graphFor("globex").RelationCount(); n != 1 {
		t.Errorf("globex relations = %d, want 1 (must be unaffected by acme's drop)", n)
	}
}
