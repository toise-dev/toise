package ingest

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/internal/tenant"
)

func TestExportTenantPolicyRefusalPartialSuccess(t *testing.T) {
	db, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	type tenantEngine struct {
		engine *change.Engine
		graph  *projection.Graph
	}
	var mu sync.Mutex
	tenants := map[string]*tenantEngine{}

	engineFor := func(tenantID string) (*change.Engine, error) {
		if tenantID == "blocked" {
			return nil, fmt.Errorf("%w: policy refusal", tenant.ErrNotAllowed)
		}
		mu.Lock()
		defer mu.Unlock()
		te := tenants[tenantID]
		if te == nil {
			g := projection.New()
			te = &tenantEngine{engine: change.New(g, db), graph: g}
			tenants[tenantID] = te
		}
		return te.engine, nil
	}

	rec := NewRoutedReceiver(engineFor, nil, nil, false, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := plogotlp.NewGRPCClient(conn)

	addResourceLog := func(req plogotlp.ExportRequest, tenantID, hostID string) {
		rl := req.Logs().ResourceLogs().AppendEmpty()
		rl.Resource().Attributes().PutStr(tenant.ResourceAttr, tenantID)
		sl := rl.ScopeLogs().AppendEmpty()
		entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": hostID}, nil)
	}

	graphFor := func(tenantID string) *projection.Graph {
		mu.Lock()
		defer mu.Unlock()
		if te := tenants[tenantID]; te != nil {
			return te.graph
		}
		return nil
	}

	t.Run("mixed ok and refused producers", func(t *testing.T) {
		req := plogotlp.NewExportRequest()
		addResourceLog(req, "ok", "h1")
		addResourceLog(req, "blocked", "h2")
		addResourceLog(req, "ok", "h3")

		resp, err := client.Export(context.Background(), req)
		if err != nil {
			t.Fatalf("Export returned error for mixed accepted/refused export: %v", err)
		}
		if got := resp.PartialSuccess().RejectedLogRecords(); got != 1 {
			t.Fatalf("rejected log records = %d, want 1", got)
		}
		g := graphFor("ok")
		if g == nil {
			t.Fatal("ok tenant graph not created")
		}
		if !hasHost(g, "h1") {
			t.Error("h1 not found")
		}
		if !hasHost(g, "h3") {
			t.Error("h3 not found")
		}
	})

	t.Run("single refused producer", func(t *testing.T) {
		req := plogotlp.NewExportRequest()
		addResourceLog(req, "blocked", "h2")

		_, err := client.Export(context.Background(), req)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("status code = %s, want InvalidArgument", status.Code(err))
		}
	})

	t.Run("invalid tenant.id refused producer", func(t *testing.T) {
		req := plogotlp.NewExportRequest()
		addResourceLog(req, "ok", "h4")
		addResourceLog(req, "../x", "h5")
		addResourceLog(req, "ok", "h6")

		resp, err := client.Export(context.Background(), req)
		if err != nil {
			t.Fatalf("Export returned error for mixed invalid-tenant export: %v", err)
		}
		if got := resp.PartialSuccess().RejectedLogRecords(); got != 1 {
			t.Fatalf("rejected log records = %d, want 1", got)
		}
		g := graphFor("ok")
		if g == nil {
			t.Fatal("ok tenant graph not created")
		}
		if !hasHost(g, "h4") {
			t.Error("h4 not found")
		}
		if !hasHost(g, "h6") {
			t.Error("h6 not found")
		}
	})
}
