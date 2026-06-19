package ingest

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
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
// asserts the resulting graph. Every record uses the agreed conventions — the OTel
// entity-events shape for nodes (EventName entity.state/entity.delete, entity.type/
// entity.id/entity.description), relationships **embedded** on entity state events
// (the sole on-wire edge form, ADR 0022), flat scalar maps, endpoints emitted before
// their edges, and an explicit entity.delete.
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
	// embRel is a single embedded relationship descriptor (OTel spec form, PR #4836):
	// the relationship type and its target entity. The source is the entity carrying it.
	type embRel struct {
		typ, toType string
		toID        map[string]any
	}
	// entity emits an entity lifecycle event. An entity_state MAY carry embedded
	// relationships: each rides on the source entity's state event as an
	// entity.relationships descriptor naming the target (the sole on-wire edge form,
	// ADR 0022). Removal is by absence — a re-emit that drops a descriptor removes it.
	entity := func(event, typ string, id, attrs map[string]any, rels ...embRel) {
		lr := rec()
		lr.SetEventName(event)
		a := lr.Attributes()
		a.PutStr(attrEntityType, typ)
		putMap(a.PutEmptyMap(attrEntityID), id)
		if attrs != nil {
			putMap(a.PutEmptyMap(attrEntityDesc), attrs)
		}
		if len(rels) > 0 {
			slv := a.PutEmptySlice(attrEntityRelationships)
			for _, r := range rels {
				m := slv.AppendEmpty().SetEmptyMap()
				m.PutStr(relDescType, r.typ)
				m.PutStr(relDescEntityType, r.toType)
				putMap(m.PutEmptyMap(relDescEntityID), r.toID)
			}
		}
	}

	hostID := map[string]any{"host.id": "h-001"}
	agentID := map[string]any{"service.instance.id": "agent-7f3a"}
	agentAttrs := map[string]any{"service.name": "senhub-agent", "service.version": "1.0.0"}
	dbID := map[string]any{"db.instance.id": "postgresql:7311168095704935424"} // stable source id (PG system_identifier), NOT network-derived
	// network.device.id is a single key whose value is subtype-prefixed and chosen by
	// the producer's precedence: serial:<PEN>:<n> > engine: > mac: (LLDP) > name: >
	// mgmt:. sw1 is anchored on its immutable ENTITY-MIB chassis serial, namespaced by
	// the vendor PEN (here 9, Cisco) read from sysObjectID; sw2 falls back to its LLDP
	// chassis-id MAC, lowercase colon-hex. Raw parts (sysName, mgmt IP) ride as
	// descriptive attributes, never as identity.
	sw1 := map[string]any{"network.device.id": "serial:9:FOC2150X0AB"}
	sw2 := map[string]any{"network.device.id": "mac:00:1a:2b:3c:4d:5e"}
	porta := map[string]any{"network.device.id": "serial:9:FOC2150X0AB", "interface.name": "Gi1/0/1"} // composite: device id + interface name
	portb := map[string]any{"network.device.id": "mac:00:1a:2b:3c:4d:5e", "interface.name": "Gi1/0/24"}

	// 1-3: nodes. Endpoints exist before any edge references them. The agent carries
	// its runs_on -> host as an EMBEDDED relationship on its state event; the host (1)
	// exists first.
	entity(evEntityState, model.TypeHost, hostID, map[string]any{"host.name": "web-server-1", "os.type": "linux"})
	entity(evEntityState, model.TypeServiceInstance, agentID, agentAttrs,
		embRel{model.RelRunsOn, model.TypeHost, hostID})
	entity(evEntityState, model.TypeDatabase, dbID, map[string]any{"db.system.name": "postgresql", "server.address": "10.0.1.5", "server.port": int64(5432)})
	// 4: the agent now also monitors the db (Lot 2) — the edge appears in its
	// relationships set -> entity.relation added. runs_on is re-asserted (heartbeat).
	entity(evEntityState, model.TypeServiceInstance, agentID, agentAttrs,
		embRel{model.RelRunsOn, model.TypeHost, hostID},
		embRel{model.RelMonitors, model.TypeDatabase, dbID})
	// 5: descriptive attribute added on the db -> entity.attribute_updated.
	entity(evEntityState, model.TypeDatabase, dbID, map[string]any{"db.system.name": "postgresql", "server.address": "10.0.1.5", "server.port": int64(5432), "db.connection.count": int64(12)})
	// 6: the agent stops listing monitors -> removed by absence (no explicit
	// relation-delete on the wire); runs_on stays.
	entity(evEntityState, model.TypeServiceInstance, agentID, agentAttrs,
		embRel{model.RelRunsOn, model.TypeHost, hostID})
	// 7: the db goes away (its monitors edge is already gone, so the delete is clean).
	entity(evEntityDelete, model.TypeDatabase, dbID, nil)
	// 8-12: discovered network assets as the topology-as-entities model (ADR 0022):
	// switches, their **ports as `network.interface` entities**, a routing-table entry
	// as a **`network.route` entity**, with `has_interface` (device->port), `has_route`
	// (device->route) and a **bare `connected_to`** (port-to-port) adjacency, all
	// embedded. No edge attributes — the ports carry their own facts (oper_state,
	// speed) and the route carries its metric/protocol; device-level adjacency is
	// *derived* at read, not stored. Device identity stays anchored on
	// observer-independent SNMP facts; the mutable mgmt IP and sysName are descriptive
	// only. Descriptive keys are dotted lowercase (`sys.name`); state-bearing keys
	// keep their exact recognized spelling — `oper_state` (underscore), not `oper.state`. Each
	// edge's endpoints exist first: portb/porta and the route precede the switch that
	// embeds has_interface/has_route, and porta embeds connected_to -> portb. The route
	// identity is {network.device.id, route.destination}; its next hop rides as the
	// scalar `next_hop.ip` (network.address is deferred).
	route1 := map[string]any{"network.device.id": "serial:9:FOC2150X0AB", "route.destination": "10.20.0.0/16"}
	entity(evEntityState, model.TypeNetworkInterface, portb, map[string]any{"oper_state": "up", "speed": int64(1_000_000_000)})
	entity(evEntityState, model.TypeNetworkInterface, porta, map[string]any{"oper_state": "up", "speed": int64(1_000_000_000)},
		embRel{model.RelConnectedTo, model.TypeNetworkInterface, portb})
	entity(evEntityState, model.TypeNetworkRoute, route1, map[string]any{"metric": int64(10), "route.protocol": "ospf", "next_hop.ip": "10.0.0.254"})
	entity(evEntityState, model.TypeNetworkDevice, sw1, map[string]any{"device.role": "switch", "sys.name": "core-sw-01", "mgmt.ip": "10.0.0.1"},
		embRel{model.RelHasInterface, model.TypeNetworkInterface, porta},
		embRel{model.RelHasRoute, model.TypeNetworkRoute, route1})
	entity(evEntityState, model.TypeNetworkDevice, sw2, map[string]any{"device.role": "switch", "sys.name": "core-sw-02", "mgmt.ip": "10.0.0.2"},
		embRel{model.RelHasInterface, model.TypeNetworkInterface, portb})

	return logs
}

func putMap(m pcommon.Map, kvs map[string]any) {
	keys := make([]string, 0, len(kvs)) // sort for a reproducible fixture
	for k := range kvs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch x := kvs[k].(type) {
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
	recon := newEmbeddedReconciler()

	rls := logs.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		sls := rls.At(i).ScopeLogs()
		for j := 0; j < sls.Len(); j++ {
			recs := sls.At(j).LogRecords()
			for k := 0; k < recs.Len(); k++ {
				lr := recs.At(k)
				handled, _, rerr := routeRecord(eng, lr, "")
				if rerr != nil {
					t.Fatalf("record %d: ingest error: %v", k, rerr)
				}
				if !handled {
					t.Fatalf("record %d: not recognized as an entity event", k)
				}
				// Embedded relationships ride on entity-state events (mirrors the
				// receiver's Export path).
				if _, herr := recon.handle(eng, lr); herr != nil {
					t.Fatalf("record %d: embedded reconcile error: %v", k, herr)
				}
			}
		}
	}

	// Final live graph: host, service.instance, the two network.devices (serial:… and
	// mac:…), their two network.interface ports, and one network.route; the db was
	// deleted.
	if got := graph.EntityCount(); got != 7 {
		t.Errorf("EntityCount = %d, want 7", got)
	}
	counts := graph.CountByType()
	for typ, want := range map[string]int{
		model.TypeHost: 1, model.TypeServiceInstance: 1, model.TypeNetworkDevice: 2,
		model.TypeNetworkInterface: 2, model.TypeNetworkRoute: 1,
	} {
		if counts[typ] != want {
			t.Errorf("live %s = %d, want %d", typ, counts[typ], want)
		}
	}
	if counts[model.TypeDatabase] != 0 {
		t.Errorf("db should be deleted, but %d live", counts[model.TypeDatabase])
	}
	// runs_on (embedded) + 2× has_interface + connected_to + has_route; monitors was removed.
	if got := graph.RelationCount(); got != 5 {
		t.Errorf("RelationCount = %d, want 5", got)
	}
	if n := len(graph.ListRelations(model.RelRunsOn, "", "")); n != 1 {
		t.Errorf("runs_on relations = %d, want 1 (agent runs on the host)", n)
	}
	if n := len(graph.ListRelations(model.RelMonitors, "", "")); n != 0 {
		t.Errorf("monitors relations = %d, want 0 (the one to db was removed)", n)
	}
	if n := len(graph.ListRelations(model.RelHasInterface, "", "")); n != 2 {
		t.Errorf("has_interface relations = %d, want 2 (each switch has its port)", n)
	}
	if n := len(graph.ListRelations(model.RelConnectedTo, "", "")); n != 1 {
		t.Errorf("connected_to relations = %d, want 1 (port-to-port adjacency)", n)
	}
	if n := len(graph.ListRelations(model.RelHasRoute, "", "")); n != 1 {
		t.Errorf("has_route relations = %d, want 1 (sw1 holds the route)", n)
	}

	// The contract's classification outcomes are all exercised.
	for _, ct := range []model.ChangeType{
		model.EntityCreated, model.EntityAttributeUpdated, model.EntityDeleted,
		model.RelationAdded, model.RelationRemoved,
	} {
		evs, rerr := st.ReadByType(context.Background(), ct)
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
