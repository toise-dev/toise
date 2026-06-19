// Command toise-probe is a real OTLP/gRPC producer for demos and end-to-end
// testing. It speaks the producer contract (OTel entity-events: `entity.state` /
// `entity.delete` LogRecords with relationships embedded on each state event, a
// Resource service.instance.id for per-producer reference counting) and, unlike
// toise-demo (which writes the store directly), exercises the live ingestion path
// over the network.
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
	"sort"
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
	interval := flag.Duration("interval", 30*time.Second, "entity.report.interval emitted (liveness TTL; sent as whole seconds)")
	heartbeat := flag.Duration("heartbeat", 0, "re-emit period; default interval/3 so entities stay live")
	once := flag.Bool("once", false, "emit one batch and exit (no heartbeat, no scenario)")
	hosts := flag.Int("hosts", 1, "number of hosts; 1 plays the narrative scenario, >1 generates a multi-machine fabric")
	devices := flag.Int("devices", 0, "number of network.device switches in the fabric; default ~hosts/10")
	flag.Parse()

	if *heartbeat <= 0 {
		*heartbeat = *interval / 3
	}
	if *devices <= 0 {
		*devices = max(2, *hosts/10)
	}

	p, err := newProducer(*addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer p.close()

	topo, step := initialTopology(*producer), scenarioStep
	if *hosts > 1 {
		topo, step = fabricTopology(*producer, *hosts, *devices), scaleStep
	}

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
	fmt.Printf("producer %q: heartbeating every %s (interval %s); %d entities, %d relations -> %s; Ctrl-C to stop\n",
		*producer, *heartbeat, *interval, len(topo.order), len(topo.relOrder), *addr)

	ticker := time.NewTicker(*heartbeat)
	defer ticker.Stop()
	for tick := 0; ; tick++ {
		deleted, note := step(topo, tick)
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
	hosts    int // number of hosts in a fabric topology (0 for the narrative one)
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
	route := id("host.id", "srv-web-1", "route.destination", "0.0.0.0/0")
	nginx := id("service.endpoint", "srv-web-1:80")
	dbid := id("db.instance.id", "postgresql:7311168095704935424")

	t.addEntity("host", &entity{model.TypeHost, host, attr("host.name", "web-server-1", "os.type", "linux", "os.description", "Ubuntu 24.04")})
	t.addEntity("agent", &entity{model.TypeServiceInstance, agent, attr("service.name", "senhub-agent", "service.version", "1.4.0")})
	t.addEntity("eth0", &entity{model.TypeNetworkInterface, eth0, attr("oper_state", "up", "mac.address", "02:42:ac:11:00:14")})
	t.addEntity("addr", &entity{model.TypeNetworkAddress, addr, attr("prefix.length", int64(24))})
	t.addEntity("route", &entity{model.TypeNetworkRoute, route, attr("next_hop.ip", "10.0.1.1")})
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

// --- fabric topology (multi-machine, hundreds of entities) --------------------

const subnetSize = 24 // hosts sharing one gateway address

// fabricTopology generates `hosts` machines (each: host + interface + address +
// route + a listener, a db on every 8th), grouped into subnets that share a
// gateway address, plus a ring of `devices` network switches. The agent monitors
// every host, db and switch. It produces several hundred entities and relations.
func fabricTopology(producer string, hosts, devices int) *topo {
	t := &topo{ents: map[string]*entity{}, rels: map[string]relation{}, hosts: hosts}
	agentID := id("service.instance.id", producer)
	t.addEntity("agent", &entity{model.TypeServiceInstance, agentID, attr("service.name", "senhub-agent", "service.version", "1.4.0")})

	gateways := map[int]bool{}
	for i := 0; i < hosts; i++ {
		s := i / subnetSize
		gwHandle := fmt.Sprintf("gw-%d", s)
		gwAddr := id("network.address", fmt.Sprintf("10.%d.0.1", s+1))
		if !gateways[s] {
			gateways[s] = true
			t.addEntity(gwHandle, &entity{model.TypeNetworkAddress, gwAddr, attr("role", "gateway")})
		}

		hostID := id("host.id", fmt.Sprintf("srv-%04d", i))
		ifID := id("host.id", fmt.Sprintf("srv-%04d", i), "interface.name", "eth0")
		addrID := id("network.address", fmt.Sprintf("10.%d.%d.%d", s+1, (i%subnetSize)/250+1, i%subnetSize+10))
		routeID := id("host.id", fmt.Sprintf("srv-%04d", i), "route.destination", "0.0.0.0/0")
		listenID := id("service.endpoint", fmt.Sprintf("srv-%04d:80", i))

		t.addEntity(fmt.Sprintf("host-%d", i), &entity{model.TypeHost, hostID, attr("host.name", fmt.Sprintf("host-%04d", i), "os.type", "linux", "zone", fmt.Sprintf("rack-%02d", s))})
		t.addEntity(fmt.Sprintf("iface-%d", i), &entity{model.TypeNetworkInterface, ifID, attr("oper_state", "up", "mac.address", fmt.Sprintf("02:42:ac:%02x:%02x:01", s, i%subnetSize))})
		t.addEntity(fmt.Sprintf("addr-%d", i), &entity{model.TypeNetworkAddress, addrID, attr("prefix.length", int64(24))})
		t.addEntity(fmt.Sprintf("route-%d", i), &entity{model.TypeNetworkRoute, routeID, attr("next_hop.ip", fmt.Sprintf("10.%d.0.1", s+1))})
		t.addEntity(fmt.Sprintf("listener-%d", i), &entity{model.TypeServiceListener, listenID, attr("process.executable.name", "nginx", "process.pid", int64(1000+i), "transport", "tcp")})

		t.addRel(fmt.Sprintf("hi-%d", i), relation{model.RelHasInterface, model.TypeHost, model.TypeNetworkInterface, hostID, ifID})
		t.addRel(fmt.Sprintf("bt-%d", i), relation{model.RelBoundTo, model.TypeNetworkAddress, model.TypeNetworkInterface, addrID, ifID})
		t.addRel(fmt.Sprintf("nh-%d", i), relation{model.RelNextHopVia, model.TypeNetworkRoute, model.TypeNetworkAddress, routeID, gwAddr})
		t.addRel(fmt.Sprintf("lo-%d", i), relation{model.RelListensOn, model.TypeServiceListener, model.TypeNetworkInterface, listenID, ifID})
		t.addRel(fmt.Sprintf("mon-h-%d", i), relation{model.RelMonitors, model.TypeServiceInstance, model.TypeHost, agentID, hostID})

		if i%8 == 0 {
			dbID := id("db.instance.id", fmt.Sprintf("postgresql:%d", 7311168095704935000+i))
			t.addEntity(fmt.Sprintf("db-%d", i), &entity{model.TypeDatabase, dbID, attr("db.system.name", "postgresql", "server.address", fmt.Sprintf("10.%d.0.%d", s+1, i%subnetSize+10), "server.port", int64(5432), "db.connection.count", int64(7))})
			t.addRel(fmt.Sprintf("mon-db-%d", i), relation{model.RelMonitors, model.TypeServiceInstance, model.TypeDatabase, agentID, dbID})
		}
	}

	for m := 0; m < devices; m++ {
		devVal := fmt.Sprintf("name:sw-%03d", m)
		dID := id("network.device.id", devVal)
		t.addEntity(fmt.Sprintf("dev-%d", m), &entity{model.TypeNetworkDevice, dID, attr("device.role", "switch", "sys.name", fmt.Sprintf("sw-%03d", m), "vendor", "acme")})
		t.addRel(fmt.Sprintf("mon-dev-%d", m), relation{model.RelMonitors, model.TypeServiceInstance, model.TypeNetworkDevice, agentID, dID})
		// A switch's ports are network.interface entities (topology-as-entities, ADR
		// 0022): Gi0/1 faces the previous switch, Gi0/2 the next. has_interface
		// attaches them; the ring is bare port-to-port connected_to, not adjacent_to.
		portPrev := id("network.device.id", devVal, "interface.name", "Gi0/1")
		portNext := id("network.device.id", devVal, "interface.name", "Gi0/2")
		t.addEntity(fmt.Sprintf("swport-%d-a", m), &entity{model.TypeNetworkInterface, portPrev, attr("oper_state", "up", "speed", int64(10_000_000_000))})
		t.addEntity(fmt.Sprintf("swport-%d-b", m), &entity{model.TypeNetworkInterface, portNext, attr("oper_state", "up", "speed", int64(10_000_000_000))})
		t.addRel(fmt.Sprintf("dev-hi-%d-a", m), relation{model.RelHasInterface, model.TypeNetworkDevice, model.TypeNetworkInterface, dID, portPrev})
		t.addRel(fmt.Sprintf("dev-hi-%d-b", m), relation{model.RelHasInterface, model.TypeNetworkDevice, model.TypeNetworkInterface, dID, portNext})
	}
	// Wire the ring with bare connected_to: sw-m's "next" port (Gi0/2) links to
	// sw-(m+1)'s "prev" port (Gi0/1). All ports exist by now (endpoints first).
	if devices > 1 {
		for m := 0; m < devices; m++ {
			next := (m + 1) % devices
			a := id("network.device.id", fmt.Sprintf("name:sw-%03d", m), "interface.name", "Gi0/2")
			b := id("network.device.id", fmt.Sprintf("name:sw-%03d", next), "interface.name", "Gi0/1")
			t.addRel(fmt.Sprintf("conn-%d", m), relation{model.RelConnectedTo, model.TypeNetworkInterface, model.TypeNetworkInterface, a, b})
		}
	}
	return t
}

// scaleStep applies a few bounded, deterministic mutations per tick so a large
// fabric's change feed keeps moving (interface flaps, pid restarts, db-stat
// drift) without flooding it.
func scaleStep(t *topo, tick int) (deleted []*entity, note string) {
	n := t.hosts
	if n == 0 {
		return nil, ""
	}
	if tick == 0 {
		return nil, fmt.Sprintf("fabric online: %d hosts, %d entities, %d relations", n, len(t.order), len(t.relOrder))
	}
	for k := 0; k < 4; k++ {
		i := (tick*5 + k*97) % n
		switch (tick + k) % 3 {
		case 0:
			if e := t.ents[fmt.Sprintf("iface-%d", i)]; e != nil {
				if e.attrs["oper_state"] == "up" {
					e.attrs["oper_state"] = "down"
				} else {
					e.attrs["oper_state"] = "up"
				}
			}
		case 1:
			if e := t.ents[fmt.Sprintf("listener-%d", i)]; e != nil {
				e.attrs["process.pid"] = int64(1000 + tick + i)
			}
		case 2:
			if e := t.ents[fmt.Sprintf("db-%d", i)]; e != nil {
				e.attrs["db.connection.count"] = int64(3 + (tick+i)%50)
			}
		}
	}
	if tick%10 == 0 {
		return nil, fmt.Sprintf("steady: %d hosts live, churn on a few interfaces/pids/db-stats", n)
	}
	return nil, ""
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
	secs := int64(interval.Seconds())

	rec := func() plog.LogRecord {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetTimestamp(now)
		return lr
	}
	for _, e := range deleted {
		lr := rec()
		lr.SetEventName("entity.delete")
		a := lr.Attributes()
		a.PutStr("entity.type", e.typ)
		putMap(a.PutEmptyMap("entity.id"), e.id)
	}
	// Relationships ride embedded on their source entity's state event (the OTel
	// standard, the sole on-wire edge form). Index each relation under its source so
	// the entity loop can attach the full current set; removal is by absence — an edge
	// dropped from t.rels simply stops appearing.
	relsBySource := make(map[string][]relation, len(t.relOrder))
	for _, h := range t.relOrder {
		r := t.rels[h]
		k := endpointKey(r.fromType, r.fromID)
		relsBySource[k] = append(relsBySource[k], r)
	}
	for _, h := range t.order {
		e := t.ents[h]
		lr := rec()
		lr.SetEventName("entity.state")
		a := lr.Attributes()
		a.PutStr("entity.type", e.typ)
		putMap(a.PutEmptyMap("entity.id"), e.id)
		putMap(a.PutEmptyMap("entity.description"), e.attrs)
		a.PutInt("entity.report.interval", secs)
		if rels := relsBySource[endpointKey(e.typ, e.id)]; len(rels) > 0 {
			slv := a.PutEmptySlice("entity.relationships")
			for _, r := range rels {
				m := slv.AppendEmpty().SetEmptyMap()
				m.PutStr("relationship.type", r.typ)
				m.PutStr("entity.type", r.toType)
				putMap(m.PutEmptyMap("entity.id"), r.toID)
			}
		}
	}

	// A large fabric is a big batch; the ingest boundary commits each record with
	// its own durable append, so give a generous deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

// endpointKey builds a stable string from an entity type + identity map so a
// relation can be indexed by its source endpoint (the entity whose state will carry
// it as an embedded relationship).
func endpointKey(typ string, idm map[string]any) string {
	keys := make([]string, 0, len(idm))
	for k := range idm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	key := typ
	for _, k := range keys {
		key += fmt.Sprintf("\x00%s=%v", k, idm[k])
	}
	return key
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
