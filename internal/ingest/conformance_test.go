package ingest

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/toise-dev/toise/internal/change"
	"github.com/toise-dev/toise/internal/model"
	"github.com/toise-dev/toise/internal/projection"
	"github.com/toise-dev/toise/internal/store"
)

// updateConformance regenerates the committed conformance fixture from
// buildConformanceLogs. Run: go test ./internal/ingest -run Conformance -update-conformance
var updateConformance = flag.Bool("update-conformance", false, "regenerate the conformance fixture")

const conformanceFile = "testdata/conformance/entity-events.json"

// buildConformanceLogs constructs the canonical example entity events that
// define the producer<->consumer contract (docs/data-model/otel-mapping.md and
// senhub-agent-contract.md). The marshaled OTLP/JSON is the shared conformance
// artifact: senhub-agent (#185) emits to reproduce it, Toise ingests it here and
// asserts the resulting graph. Every record uses the agreed conventions — the
// standard otel.entity.* shape for nodes, the toise.relation.* extension for
// edges, flat scalar maps, exact-identity endpoints emitted before their edges,
// and an explicit entity_delete.
func buildConformanceLogs() plog.Logs {
	logs := plog.NewLogs()
	sl := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	n := 0
	rec := func() plog.LogRecord {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(pcommon.NewTimestampFromTime(base.Add(time.Duration(n) * time.Minute)))
		n++
		return lr
	}
	entity := func(event, typ string, id, attrs map[string]any) {
		a := rec().Attributes()
		a.PutStr(attrEventType, event)
		a.PutStr(attrEntityType, typ)
		putMap(a.PutEmptyMap(attrEntityID), id)
		if attrs != nil {
			putMap(a.PutEmptyMap(attrEntityAttrs), attrs)
		}
	}
	relation := func(event, relType, fromType string, fromID map[string]any, toType string, toID map[string]any) {
		a := rec().Attributes()
		a.PutStr(attrEventType, event)
		a.PutStr(attrRelType, relType)
		a.PutStr(attrRelFromType, fromType)
		putMap(a.PutEmptyMap(attrRelFromID), fromID)
		a.PutStr(attrRelToType, toType)
		putMap(a.PutEmptyMap(attrRelToID), toID)
	}

	hostID := map[string]any{"host.id": "h-001"}
	agentID := map[string]any{"service.instance.id": "agent-7f3a"}
	dbID := map[string]any{"db.instance.id": "pg@web-server-1:5432"} // single composite key (not {system,address,port})
	sw1 := map[string]any{"network.device.id": "sw-01"}
	sw2 := map[string]any{"network.device.id": "sw-02"}

	// 1-3: nodes (standard OTel). Endpoints exist before the edges reference them.
	entity(evEntityState, model.TypeHost, hostID, map[string]any{"host.name": "web-server-1", "os.type": "linux"})
	entity(evEntityState, model.TypeServiceInstance, agentID, map[string]any{"service.name": "senhub-agent", "service.version": "1.0.0"})
	entity(evEntityState, model.TypeDatabase, dbID, map[string]any{"db.system.name": "postgresql", "server.address": "10.0.1.5", "server.port": int64(5432)})
	// 4-5: edges (toise.relation.* extension). monitors targets a host and a db.
	relation(evRelationState, model.RelMonitors, model.TypeServiceInstance, agentID, model.TypeHost, hostID)
	relation(evRelationState, model.RelMonitors, model.TypeServiceInstance, agentID, model.TypeDatabase, dbID)
	// 6: descriptive attribute added -> entity.attribute_updated.
	entity(evEntityState, model.TypeDatabase, dbID, map[string]any{"db.system.name": "postgresql", "server.address": "10.0.1.5", "server.port": int64(5432), "db.connection.count": int64(12)})
	// 7-8: the db goes away — remove its edge first (endpoints must be live), then delete it.
	relation(evRelationDelete, model.RelMonitors, model.TypeServiceInstance, agentID, model.TypeDatabase, dbID)
	entity(evEntityDelete, model.TypeDatabase, dbID, nil)
	// 9-11: discovered network assets and a link-layer adjacency.
	entity(evEntityState, model.TypeNetworkDevice, sw1, map[string]any{"device.role": "switch"})
	entity(evEntityState, model.TypeNetworkDevice, sw2, map[string]any{"device.role": "switch"})
	relation(evRelationState, model.RelAdjacentTo, model.TypeNetworkDevice, sw1, model.TypeNetworkDevice, sw2)

	return logs
}

func putMap(m pcommon.Map, kvs map[string]any) {
	for k, v := range kvs {
		switch x := v.(type) {
		case string:
			m.PutStr(k, x)
		case int64:
			m.PutInt(k, x)
		case float64:
			m.PutDouble(k, x)
		case bool:
			m.PutBool(k, x)
		}
	}
}

// TestConformanceFixture ingests the committed conformance fixture through the
// real change engine and asserts the resulting graph matches the documented
// contract. It is the executable producer<->consumer interface: a change on
// either side that breaks the contract fails this test.
func TestConformanceFixture(t *testing.T) {
	if *updateConformance {
		writeConformanceFixture(t)
	}

	data, err := os.ReadFile(conformanceFile)
	if err != nil {
		t.Fatalf("read fixture (regenerate with -update-conformance): %v", err)
	}
	logs, err := (&plog.JSONUnmarshaler{}).UnmarshalLogs(data)
	if err != nil {
		t.Fatalf("unmarshal OTLP/JSON fixture: %v", err)
	}

	st, err := store.Open(t.TempDir(), store.DefaultConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	graph := projection.New()
	eng := change.New(graph, st)

	rls := logs.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				handled, rerr := routeRecord(eng, recs.At(k))
				if rerr != nil {
					t.Fatalf("record %d: ingest error: %v", k, rerr)
				}
				if !handled {
					t.Fatalf("record %d: not recognized as an entity event", k)
				}
			}
		}
	}

	// Final live graph: host, service.instance, sw-01, sw-02 (the db was deleted).
	if got := graph.EntityCount(); got != 4 {
		t.Errorf("EntityCount = %d, want 4", got)
	}
	counts := graph.CountByType()
	for typ, want := range map[string]int{
		model.TypeHost: 1, model.TypeServiceInstance: 1, model.TypeNetworkDevice: 2,
	} {
		if counts[typ] != want {
			t.Errorf("live %s = %d, want %d", typ, counts[typ], want)
		}
	}
	if counts[model.TypeDatabase] != 0 {
		t.Errorf("db should be deleted, but %d live", counts[model.TypeDatabase])
	}
	if got := graph.RelationCount(); got != 2 {
		t.Errorf("RelationCount = %d, want 2 (monitors->host, adjacent_to)", got)
	}
	if n := len(graph.ListRelations(model.RelMonitors, "", "")); n != 1 {
		t.Errorf("monitors relations = %d, want 1 (the one to db was removed)", n)
	}
	if n := len(graph.ListRelations(model.RelAdjacentTo, "", "")); n != 1 {
		t.Errorf("adjacent_to relations = %d, want 1", n)
	}

	// The contract's classification outcomes are all exercised.
	for _, ct := range []model.ChangeType{
		model.EntityCreated, model.EntityAttributeUpdated, model.EntityDeleted,
		model.RelationAdded, model.RelationRemoved,
	} {
		evs, rerr := st.ReadByType(ct)
		if rerr != nil {
			t.Fatalf("ReadByType(%s): %v", ct, rerr)
		}
		if len(evs) == 0 {
			t.Errorf("fixture produced no %s event", ct)
		}
	}
}

func writeConformanceFixture(t *testing.T) {
	t.Helper()
	raw, err := (&plog.JSONMarshaler{}).MarshalLogs(buildConformanceLogs())
	if err != nil {
		t.Fatalf("marshal logs: %v", err)
	}
	var asAny any
	if uerr := json.Unmarshal(raw, &asAny); uerr != nil {
		t.Fatalf("reindent: %v", uerr)
	}
	indented, err := json.MarshalIndent(asAny, "", "  ")
	if err != nil {
		t.Fatalf("reindent: %v", err)
	}
	if mkErr := os.MkdirAll(filepath.Dir(conformanceFile), 0o755); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}
	if wErr := os.WriteFile(conformanceFile, append(indented, '\n'), 0o644); wErr != nil {
		t.Fatalf("write fixture: %v", wErr)
	}
	t.Logf("regenerated %s", conformanceFile)
}
