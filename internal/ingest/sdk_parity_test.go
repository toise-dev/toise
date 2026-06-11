package ingest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
	"github.com/toise-dev/toise/pkg/emit"
)

// TestSDKParityWithIngest is the two-sided pin of #142: what the toise-emit
// SDK produces, the real ingest accepts in full — entities, interval-armed
// liveness, and embedded relationships — and the published fixture v1 bytes
// are accepted with zero rejections. If either side drifts from the contract,
// this fails before any producer does.
func TestSDKParityWithIngest(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	g := projection.New()
	eng := change.New(g, st, change.WithRelationBuffer(30*time.Second))
	rec := NewReceiver(eng, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)

	// The SDK path, end to end.
	client, err := emit.New(emit.Options{
		Endpoint:          lis.Addr().String(),
		ServiceName:       "parity-producer",
		ServiceInstanceID: "parity-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hostID := map[string]string{"host.id": "p-1111"}
	if serr := client.State(ctx,
		emit.Entity{Type: "host", ID: hostID, Attributes: map[string]string{"host.name": "parity-1"}, Interval: 5 * time.Minute},
		emit.Entity{Type: "service.listener", ID: map[string]string{"service.endpoint": "p-1111:8080/tcp"},
			Relationships: []emit.Relationship{{Type: "runs_on", TargetType: "host", TargetID: hostID}}},
	); serr != nil {
		t.Fatalf("SDK State: %v", serr)
	}
	if g.EntityCount() != 2 || g.RelationCount() != 1 {
		t.Fatalf("after SDK emit: %d/%d, want 2 entities / 1 relation", g.EntityCount(), g.RelationCount())
	}
	if derr := client.Delete(ctx, emit.Entity{Type: "service.listener", ID: map[string]string{"service.endpoint": "p-1111:8080/tcp"}}); derr != nil {
		t.Fatalf("SDK Delete: %v", derr)
	}
	if g.EntityCount() != 1 || g.RelationCount() != 0 {
		t.Fatalf("after SDK delete: %d/%d, want 1/0 (cascade)", g.EntityCount(), g.RelationCount())
	}

	// The published fixture v1, raw bytes, against the same receiver.
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "emit", "testdata", "fixture_v1.bin"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	req := plogotlp.NewExportRequest()
	if uerr := req.UnmarshalProto(raw); uerr != nil {
		t.Fatalf("fixture does not unmarshal: %v", uerr)
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	resp, err := plogotlp.NewGRPCClient(conn).Export(ctx, req)
	if err != nil {
		t.Fatalf("fixture export rejected by ingest: %v", err)
	}
	if n := resp.PartialSuccess().RejectedLogRecords(); n != 0 {
		t.Fatalf("fixture export: %d records rejected (%s) — the contract drifted", n, resp.PartialSuccess().ErrorMessage())
	}
	// The fixture's host + listener (+ runs_on) are now in the graph too.
	if _, ok := g.MatchIdentity(model.TypeHost, []model.KeyValue{{Key: "host.id", Value: model.StringValue("0f7a3c1e-aaaa-bbbb-cccc-000000000001")}}); !ok {
		t.Fatal("fixture host did not land in the graph")
	}
}
