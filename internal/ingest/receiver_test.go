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
func startReceiver(t *testing.T, dialOpts ...grpc.DialOption) (plogotlp.GRPCClient, *projection.Graph, *store.Store) {
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
	return plogotlp.NewGRPCClient(conn), g, st
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
	client, g, _ := startReceiver(t)

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
	client, g, _ := startReceiver(t, grpc.WithDefaultCallOptions(grpc.UseCompressor("gzip")))

	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)

	export(t, client, ld)

	if g.EntityCount() != 1 {
		t.Errorf("entities = %d, want 1 (gzip'd export must reach the handler)", g.EntityCount())
	}
}

func TestReceiverEntityDelete(t *testing.T) {
	client, g, _ := startReceiver(t)

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
	client, g, _ := startReceiver(t)

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

// TestReceiverRemovalByAbsenceAfterTargetDeleted reproduces the #110 poison
// pill: a source asserts an embedded relation, the target is deleted (the
// cascade removes the edge), then the source drops the descriptor from its next
// export. Per the contract that removal-by-absence is a no-op — the export must
// succeed and the reconciler state must not replay the failure forever.
func TestReceiverRemovalByAbsenceAfterTargetDeleted(t *testing.T) {
	client, g, _ := startReceiver(t)

	// 1. host h1 + service s1 with embedded runs_on -> h1.
	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
	embeddedEntity(sl, t0, model.TypeServiceInstance, map[string]string{"service.instance.id": "s1"},
		[]relDesc{{relType: model.RelRunsOn, toType: model.TypeHost, toID: map[string]string{"host.id": "h1"}}})
	export(t, client, ld)
	if g.RelationCount() != 1 {
		t.Fatalf("after assert: RelationCount = %d, want 1", g.RelationCount())
	}

	// 2. delete h1: the engine cascades the edge away.
	del := plog.NewLogs()
	dsl := del.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(dsl, evEntityDelete, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
	export(t, client, del)
	if g.RelationCount() != 0 {
		t.Fatalf("after target delete: RelationCount = %d, want 0 (cascade)", g.RelationCount())
	}

	// 3. s1 re-emits WITHOUT the descriptor: the removal diff targets an edge
	// whose endpoint no longer resolves — this export and every following one
	// must succeed (the bug failed them all until restart).
	for i := 0; i < 2; i++ {
		ld := plog.NewLogs()
		sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
		embeddedEntity(sl, t0.Add(time.Duration(i+1)*time.Minute), model.TypeServiceInstance,
			map[string]string{"service.instance.id": "s1"}, nil)
		export(t, client, ld)
	}
	if g.EntityCount() != 1 {
		t.Errorf("EntityCount = %d, want 1 (s1 alive, h1 deleted)", g.EntityCount())
	}
}

// TestReceiverRejectsInvalidRecordPerRecord pins the #109 boundary contract: a
// batch mixing valid records with an unknown-type record persists the valid
// ones, rejects the bad one alone via OTLP partial success (no transport
// error, so the producer does not retry), and leaves the projection equal to a
// replay of the durable log.
func TestReceiverRejectsInvalidRecordPerRecord(t *testing.T) {
	client, g, st := startReceiver(t)

	ld := plog.NewLogs()
	sl := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h1"}, nil)
	entityRecord(sl, evEntityState, "not.a.registered.type", map[string]string{"x": "1"}, nil)
	entityRecord(sl, evEntityState, model.TypeHost, map[string]string{"host.id": "h2"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Export(ctx, plogotlp.NewExportRequestFromLogs(ld))
	if err != nil {
		t.Fatalf("export errored (must be partial success, not failure): %v", err)
	}
	ps := resp.PartialSuccess()
	if ps.RejectedLogRecords() != 1 {
		t.Errorf("RejectedLogRecords = %d, want 1", ps.RejectedLogRecords())
	}
	if ps.ErrorMessage() == "" {
		t.Error("partial success must carry the validation error message")
	}

	if g.EntityCount() != 2 {
		t.Fatalf("EntityCount = %d, want 2 (valid siblings persisted)", g.EntityCount())
	}
	// The projection must equal a replay of the durable log: nothing applied or
	// broadcast that was not appended.
	g2 := projection.New()
	if err := g2.Replay(st); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if g2.EntityCount() != g.EntityCount() || g2.RelationCount() != g.RelationCount() {
		t.Errorf("projection diverged from replay(log): entities %d/%d relations %d/%d",
			g.EntityCount(), g2.EntityCount(), g.RelationCount(), g2.RelationCount())
	}

	// A later valid export from the same producer keeps working.
	ld2 := plog.NewLogs()
	sl2 := ld2.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	entityRecord(sl2, evEntityState, model.TypeHost, map[string]string{"host.id": "h3"}, nil)
	export(t, client, ld2)
	if g.EntityCount() != 3 {
		t.Errorf("EntityCount = %d after follow-up export, want 3", g.EntityCount())
	}
}
