// Command toise-probe is a real OTLP/gRPC producer for demos and end-to-end
// testing. It speaks the producer contract (standard otel.entity.* nodes, the
// vendor-neutral entity.relation.* edge extension, a Resource service.instance.id
// for per-producer reference counting) and, unlike toise-demo (which writes the
// store directly), exercises the live ingestion path over the network.
//
// By default it heartbeats a small infrastructure topology and plays an evolving
// scenario (a process restart, an interface flap, a container appearing and
// crashing, fluctuating attributes), so the graph stays live and the change feed
// is rich enough for a POC. Run several instances with distinct --producer values
// to demonstrate multi-agent reference counting.
//
//	toise-server &
//	toise-probe                       # heartbeat + evolving scenario forever
//	toise-probe --once                # emit one batch and exit
//	toise-probe --producer agent-b    # a second agent observing the shared host/db
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/toise-dev/toise/internal/model"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:4317", "toise-server OTLP/gRPC address")
	producer := flag.String("producer", "toise-probe-1", "this agent's service.instance.id (OTLP Resource)")
	interval := flag.Duration("interval", 30*time.Second, "otel.entity.interval emitted (liveness TTL)")
	heartbeat := flag.Duration("heartbeat", 0, "re-emit period; default interval/3 so entities stay live")
	once := flag.Bool("once", false, "emit one batch and exit (no heartbeat, no scenario)")
	flag.Parse()

	if *heartbeat <= 0 {
		*heartbeat = *interval / 3
	}

	p, err := newProducer(*addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer p.close()

	topo := initialTopology(*producer)

	if *once {
		if err := p.emit(*producer, topo, nil, *interval); err != nil {
			return fmt.Errorf("emit: %w", err)
		}
		fmt.Printf("producer %q: emitted %d entities, %d relations once -> %s\n",
			*producer, len(topo.order), len(topo.relOrder), *addr)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("producer %q: heartbeating every %s (interval %s) -> %s; Ctrl-C to stop\n",
		*producer, *heartbeat, *interval, *addr)

	ticker := time.NewTicker(*heartbeat)
	defer ticker.Stop()
	for tick := 0; ; tick++ {
		deleted, note := scenarioStep(topo, tick)
		if err := p.emit(*producer, topo, deleted, *interval); err != nil {
			log.Printf("emit (tick %d): %v", tick, err)
		} else if note != "" {
			fmt.Printf("tick %3d  %s\n", tick, note)
		}
		select {
		case <-ctx.Done():
			fmt.Println("\nstopped.")
			return nil
		case <-ticker.C:
		}
	}
}

// --- topology -----------------------------------------------------------------

type entity struct {
	typ   string
	id    map[string]any
	attrs map[string]any
}

type relation struct {
	typ              string
	fromType, toType string
	fromID, toID     map[string]any
}

// topo is the producer's current view of the world, keyed by stable handles so
// the scenario can mutate it. It is re-emitted as full state each heartbeat.
type topo struct {
	ents     map[string]*entity
	order    []string
	rels     map[string]relation
	relOrder []string
}

func (t *topo) addEntity(handle string, e *entity) {
	if _, ok := t.ents[handle]; !ok {
		t.order = append(t.order, handle)
	}
	t.ents[handle] = e
}

func (t *topo) addRel(handle string, r relation) {
	if _, ok := t.rels[handle]; !ok {
		t.relOrder = append(t.relOrder, handle)
	}
	t.rels[handle] = r
}

// removeEntity drops an entity and its incident relations from the live set and
// returns the entity so the caller can emit an explicit entity_delete. Toise
// cascades the edges, so we just stop heartbeating them.
func (t *topo) removeEntity(handle string) *entity {
	e := t.ents[handle]
	if e == nil {
		return nil
	}
	delete(t.ents, handle)
	t.order = removeStr(t.order, handle)
	for _, rh := range append([]string(nil), t.relOrder...) {
		r := t.rels[rh]
		if sameID(r.fromID, e.id) || sameID(r.toID, e.id) {
			delete(t.rels, rh)
			t.relOrder = removeStr(t.relOrder, rh)
		}
	}
	return e
}

func initialTopology(producer string) *topo {
	t := &topo{ents: map[string]*entity{}, rels: map[string]relation{}}
	host := id("host.id", "srv-web-1")
	agent := id("service.instance.id", producer)
	eth0 := id("host.id", "srv-web-1", "interface.name", "eth0")
	addr := id("network.address", "10.0.1.20")
	route := id("host.id", "srv-web-1", "network.route.destination", "0.0.0.0/0")
	nginx := id("service.endpoint", "srv-web-1:80")
	dbid := id("db.instance.id", "postgresql:7311168095704935424")

	t.addEntity("host", &entity{model.TypeHost, host, attr("host.name", "web-server-1", "os.type", "linux", "os.description", "Ubuntu 24.04")})
	t.addEntity("agent", &entity{model.TypeServiceInstance, agent, attr("service.name", "senhub-agent", "service.version", "1.4.0")})
	t.addEntity("eth0", &entity{model.TypeNetworkInterface, eth0, attr("oper_state", "up", "mac.address", "02:42:ac:11:00:14")})
	t.addEntity("addr", &entity{model.TypeNetworkAddress, addr, attr("prefix.length", int64(24))})
	t.addEntity("route", &entity{model.TypeNetworkRoute, route, attr("next_hop", "10.0.1.1")})
	t.addEntity("nginx", &entity{model.TypeServiceListener, nginx, attr("process.executable.name", "nginx", "process.pid", int64(1001), "transport", "tcp")})
	t.addEntity("db", &entity{model.TypeDatabase, dbid, attr("db.system.name", "postgresql", "server.address", "10.0.1.45", "server.port", int64(5432), "db.connection.count", int64(7))})

	t.addRel("agent-runs", relation{model.RelRunsOn, model.TypeServiceInstance, model.TypeHost, agent, host})
	t.addRel("agent-mon-host", relation{model.RelMonitors, model.TypeServiceInstance, model.TypeHost, agent, host})
	t.addRel("agent-mon-db", relation{model.RelMonitors, model.TypeServiceInstance, model.TypeDatabase, agent, dbid})
	t.addRel("host-eth0", relation{model.RelHasInterface, model.TypeHost, model.TypeNetworkInterface, host, eth0})
	t.addRel("addr-eth0", relation{model.RelBoundTo, model.TypeNetworkAddress, model.TypeNetworkInterface, addr, eth0})
	t.addRel("route-addr", relation{model.RelNextHopVia, model.TypeNetworkRoute, model.TypeNetworkAddress, route, addr})
	t.addRel("nginx-eth0", relation{model.RelListensOn, model.TypeServiceListener, model.TypeNetworkInterface, nginx, eth0})
	return t
}

// --- scenario -----------------------------------------------------------------

// scenarioStep mutates the topology for the given heartbeat tick and returns the
// entities deleted this tick (for explicit entity_delete records) plus a short
// human note. After the scripted beats it cycles a couple of them so the change
// feed keeps moving in a long-running demo.
func scenarioStep(t *topo, tick int) (deleted []*entity, note string) {
	switch tick {
	case 0:
		return nil, "discovery: host, agent, eth0, address, route, nginx, db + topology"
	case 3:
		t.ents["nginx"].attrs["process.pid"] = int64(1042) // restart -> attribute_updated
		return nil, "nginx restarts (pid 1001 -> 1042) — attribute_updated"
	case 5:
		dock := id("service.endpoint", "srv-web-1:2375")
		t.addEntity("dockerd", &entity{model.TypeServiceListener, dock, attr("process.executable.name", "dockerd", "process.pid", int64(2002), "transport", "tcp")})
		t.addRel("dockerd-eth0", relation{model.RelListensOn, model.TypeServiceListener, model.TypeNetworkInterface, dock, id("host.id", "srv-web-1", "interface.name", "eth0")})
		return nil, "container daemon appears (dockerd) — entity.created + relation.added"
	case 7:
		t.ents["eth0"].attrs["oper_state"] = "down" // state key -> state_changed
		return nil, "eth0 goes DOWN — state_changed (structural-ish)"
	case 9:
		t.ents["eth0"].attrs["oper_state"] = "up"
		return nil, "eth0 back UP — state_changed"
	case 11:
		return []*entity{t.removeEntity("dockerd")}, "container crashes (dockerd) — entity.deleted + edge cascade"
	default:
		// keep it alive and gently moving: fluctuate the db connection count, and
		// nudge the nginx pid occasionally, so recent-changes is never empty.
		t.ents["db"].attrs["db.connection.count"] = int64(5 + tick%40)
		if tick%17 == 0 {
			t.ents["nginx"].attrs["process.pid"] = int64(1042 + tick)
			return nil, "nginx pid drift — attribute_updated"
		}
		return nil, ""
	}
}

// --- OTLP producer ------------------------------------------------------------

type producerConn struct {
	conn   *grpc.ClientConn
	client plogotlp.GRPCClient
}

func newProducer(addr string) (*producerConn, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &producerConn{conn: conn, client: plogotlp.NewGRPCClient(conn)}, nil
}

func (p *producerConn) close() { _ = p.conn.Close() }

func (p *producerConn) emit(producer string, t *topo, deleted []*entity, interval time.Duration) error {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("service.instance.id", producer)
	sl := rl.ScopeLogs().AppendEmpty()
	now := pcommon.NewTimestampFromTime(time.Now())
	ms := interval.Milliseconds()

	rec := func() plog.LogRecord {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(now)
		return lr
	}
	for _, e := range deleted {
		a := rec().Attributes()
		a.PutStr("otel.entity.event.type", "entity_delete")
		a.PutStr("otel.entity.type", e.typ)
		putMap(a.PutEmptyMap("otel.entity.id"), e.id)
	}
	for _, h := range t.order {
		e := t.ents[h]
		a := rec().Attributes()
		a.PutStr("otel.entity.event.type", "entity_state")
		a.PutStr("otel.entity.type", e.typ)
		putMap(a.PutEmptyMap("otel.entity.id"), e.id)
		putMap(a.PutEmptyMap("otel.entity.attributes"), e.attrs)
		a.PutInt("otel.entity.interval", ms)
	}
	for _, h := range t.relOrder {
		r := t.rels[h]
		a := rec().Attributes()
		a.PutStr("entity.relation.event.type", "state")
		a.PutStr("entity.relation.type", r.typ)
		a.PutStr("entity.relation.from.type", r.fromType)
		putMap(a.PutEmptyMap("entity.relation.from.id"), r.fromID)
		a.PutStr("entity.relation.to.type", r.toType)
		putMap(a.PutEmptyMap("entity.relation.to.id"), r.toID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.client.Export(ctx, plogotlp.NewExportRequestFromLogs(logs))
	return err
}

// --- helpers ------------------------------------------------------------------

// id builds an identity map; attr builds an attribute map. They are the same
// shape; the two names document intent at the call site.
func id(kvs ...any) map[string]any   { return mkmap(kvs...) }
func attr(kvs ...any) map[string]any { return mkmap(kvs...) }

func mkmap(kvs ...any) map[string]any {
	m := make(map[string]any, len(kvs)/2)
	for i := 0; i+1 < len(kvs); i += 2 {
		m[kvs[i].(string)] = kvs[i+1]
	}
	return m
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

func sameID(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func removeStr(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
