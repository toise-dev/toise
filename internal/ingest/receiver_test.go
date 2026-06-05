package ingest

import (
	"context"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

var t0 = time.Unix(1_700_000_000, 0).UTC()

// startReceiver wires a real store + projection + engine behind an OTLP gRPC
// receiver on a loopback port and returns an OTLP logs client plus the graph.
// Extra dial options (e.g. a default call compressor) let a test exercise the
// wire path real producers use.
func startReceiver(t *testing.T, dialOpts ...grpc.DialOption) (plogotlp.GRPCClient, *projection.Graph) {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	g := projection.New()
	eng := change.New(g, st, change.WithClock(func() time.Time { return t0 }))
	rec := NewReceiver(eng, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = rec.Serve(lis) }()
	t.Cleanup(rec.Stop)

	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, dialOpts...)
	conn, err := grpc.NewClient(lis.Addr().String(), opts...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return plogotlp.NewGRPCClient(conn), g
}

func export(t *testing.T, c plogotlp.GRPCClient, ld plog.Logs) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Export(ctx, plogotlp.NewExportRequestFromLogs(ld)); err != nil {
		t.Fatalf("export: %v", err)
	}
}

func entityRecord(sl plog.ScopeLogs, eventName, entType string, ident, attrs map[string]string) {
	lr := sl.LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(t0))
	lr.SetEventName(eventName)
	a := lr.Attributes()
	a.PutStr(attrEntityType, entType)
	idm := a.PutEmptyMap(attrEntityID)
	for k, v := range ident {
		idm.PutStr(k, v)
	}
	if attrs != nil {
		am := a.PutEmptyMap(attrEntityDesc)
		for k, v := range attrs {
			am.PutStr(k, v)
		}
	}
}

func TestReceiverEntityAndRelation(t *testing.T) {
	client, g := startReceiver(t)

	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	// the host exists before the process that embeds a runs_on -> host edge.
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, map[string]string{"status": "up"})
	embeddedEntity(sl, t0, model.TypeProcess, map[string]string{"pid": "100"},
		[]relDesc{{relType: model.RelRunsOn, toType: model.TypeHost, toID: map[string]string{"host.id": "h1"}}})
	// a non-entity log record must be ignored
	sl.LogRecords().AppendEmpty().Body().SetStr("unrelated log line")

	export(t, client, ld)

	if g.EntityCount() != 2 {
		t.Errorf("entities = %d, want 2", g.EntityCount())
	}
	if g.RelationCount() != 1 {
		t.Errorf("relations = %d, want 1", g.RelationCount())
	}
	// the host carries its descriptive attribute
	id, found := g.MatchIdentity(model.TypeHost, []model.KeyValue{{Key: "host.id", Value: model.StringValue("h1")}})
	if !found {
		t.Fatal("host not found in projection")
	}
	host, ok, _ := g.GetEntity(id)
	if !ok || len(host.Attributes) != 1 || host.Attributes[0].Key != "status" {
		t.Errorf("host attributes = %+v, want [status]", host.Attributes)
	}
}

// TestReceiverAcceptsGzip guards the gzip decompressor registration: real
// producers (the OTel SDK, senhub-agent) compress exports with gzip by
// default, and gRPC-Go does not install the codec unless the encoding package
// is imported. Without it, a gzip'd export fails at the transport with
// "Decompressor is not installed" before reaching the handler.
//
// The codec lives in a process-wide registry, so this test deliberately
// references it by name only ("gzip") and does NOT import the encoding
// package: the sole thing registering gzip in the test binary is the
// production blank import in receiver.go. Drop that import and this test
// fails to even compress the request — exactly the regression we guard.
func TestReceiverAcceptsGzip(t *testing.T) {
	client, g := startReceiver(t, grpc.WithDefaultCallOptions(grpc.UseCompressor("gzip")))

	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)

	export(t, client, ld)

	if g.EntityCount() != 1 {
		t.Errorf("entities = %d, want 1 (gzip'd export must reach the handler)", g.EntityCount())
	}
}

func TestReceiverEntityDelete(t *testing.T) {
	client, g := startReceiver(t)

	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
	export(t, client, ld)
	if g.EntityCount() != 1 {
		t.Fatalf("after create: %d entities, want 1", g.EntityCount())
	}

	del := plog.NewLogs()
	dsl := del.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(dsl, evEntityDelete, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
	export(t, client, del)
	if g.EntityCount() != 0 {
		t.Errorf("after delete: %d live entities, want 0", g.EntityCount())
	}
}

// embeddedEntity appends an entity-state record carrying an embedded
// `entity.relationships` array (the OTel spec form, PR #4836) to sl.
func embeddedEntity(sl plog.ScopeLogs, when time.Time, typ string, id map[string]string, rels []relDesc) {
	lr := sl.LogRecords().AppendEmpty()
	lr.SetTimestamp(pcommon.NewTimestampFromTime(when))
	lr.SetEventName(evEntityState)
	a := lr.Attributes()
	a.PutStr(attrEntityType, typ)
	idm := a.PutEmptyMap(attrEntityID)
	for k, v := range id {
		idm.PutStr(k, v)
	}
	if rels != nil {
		slv := a.PutEmptySlice(attrEntityRelationships)
		for _, d := range rels {
			m := slv.AppendEmpty().SetEmptyMap()
			m.PutStr(relDescType, d.relType)
			m.PutStr(relDescEntityType, d.toType)
			tid := m.PutEmptyMap(relDescEntityID)
			for k, v := range d.toID {
				tid.PutStr(k, v)
			}
		}
	}
}

// TestReceiverEmbeddedRelationships exercises the spec's embedded relationship
// model end-to-end: a relation embedded on an entity-state event is ingested, and
// re-emitting the source without it removes the relation.
func TestReceiverEmbeddedRelationships(t *testing.T) {
	client, g := startReceiver(t)

	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
	embeddedEntity(sl, t0, model.TypeServiceInstance, map[string]string{"service.instance.id": "s1"},
		[]relDesc{{relType: model.RelRunsOn, toType: model.TypeHost, toID: map[string]string{"host.id": "h1"}}})
	export(t, client, ld)

	if g.RelationCount() != 1 {
		t.Fatalf("RelationCount = %d, want 1 (embedded runs_on)", g.RelationCount())
	}
	if n := len(g.ListRelations(model.RelRunsOn, "", "")); n != 1 {
		t.Errorf("runs_on relations = %d, want 1", n)
	}

	// Re-emit s1 with no relationships — the runs_on is removed by absence.
	ld2 := plog.NewLogs()
	sl2 := ld2.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	embeddedEntity(sl2, t0.Add(time.Minute), model.TypeServiceInstance, map[string]string{"service.instance.id": "s1"}, nil)
	export(t, client, ld2)

	if g.RelationCount() != 0 {
		t.Errorf("RelationCount = %d after dropping the embedded relationship, want 0", g.RelationCount())
	}
}
