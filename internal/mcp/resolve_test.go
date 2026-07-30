package mcp

import (
	"testing"

	"github.com/toise-dev/toise/internal/model"
)

func sv(s string) model.Value { return model.StringValue(s) }

func endpointEntity(addr, port string) model.Entity {
	return model.Entity{
		ID:   "ep1",
		Type: model.TypeNetworkEndpoint,
		Identity: []model.KeyValue{
			{Key: "server.address", Value: sv(addr)},
			{Key: "server.port", Value: sv(port)},
			{Key: "network.transport", Value: sv("tcp")},
		},
	}
}

// graph with host B reachable by the interface model:
// network.address(10.0.0.5) bound_to iface, host has_interface iface,
// listener(hb-uuid:443/tcp, binds 0.0.0.0) runs_on host.
func interfaceModelGraph() *fakeGraph {
	hb := model.Entity{ID: "hb", Type: model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: sv("hb-uuid")}}}
	lis := model.Entity{ID: "lisB", Type: model.TypeServiceListener,
		Identity:   []model.KeyValue{{Key: "service.endpoint", Value: sv("hb-uuid:443/tcp")}},
		Attributes: []model.KeyValue{{Key: "listen.address", Value: sv("0.0.0.0")}}}
	iface := model.Entity{ID: "ifB", Type: model.TypeNetworkInterface,
		Identity: []model.KeyValue{{Key: "interface.name", Value: sv("eth0")}}}
	addr := model.Entity{ID: "addrB", Type: model.TypeNetworkAddress,
		Identity: []model.KeyValue{{Key: "network.address", Value: sv("10.0.0.5")}}}
	return &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			hb.ID: hb, lis.ID: lis, iface.ID: iface, addr.ID: addr,
		},
		deleted: map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "r1", Type: model.RelRunsOn, From: lis.ID, To: hb.ID},
			{ID: "r2", Type: model.RelHasInterface, From: hb.ID, To: iface.ID},
			{ID: "r3", Type: model.RelBoundTo, From: addr.ID, To: iface.ID},
		},
	}
}

// Wildcard bind (0.0.0.0): resolution must go through the interface model to the
// host, then to its listener on the port.
func TestResolveEndpointViaInterfaceModel(t *testing.T) {
	g := interfaceModelGraph()
	got, ok := resolveEndpoint(g, endpointEntity("10.0.0.5", "443"))
	if !ok {
		t.Fatal("endpoint should resolve via the interface model")
	}
	if got.ID != "lisB" {
		t.Fatalf("resolved to %s, want the listener lisB", got.ID)
	}
}

// A listener that binds a concrete address resolves directly, without needing
// the interface model.
func TestResolveEndpointByDirectBind(t *testing.T) {
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			"lisB": {ID: "lisB", Type: model.TypeServiceListener,
				Identity:   []model.KeyValue{{Key: "service.endpoint", Value: sv("hb-uuid:443/tcp")}},
				Attributes: []model.KeyValue{{Key: "listen.address", Value: sv("10.0.0.5")}}},
		},
		deleted: map[model.EntityID]bool{},
	}
	got, ok := resolveEndpoint(g, endpointEntity("10.0.0.5", "443"))
	if !ok || got.ID != "lisB" {
		t.Fatalf("direct-bind resolution failed: got %q ok=%v", got.ID, ok)
	}
}

// The address resolves to a host but no listener is known on that port: fall
// back to the host.
func TestResolveEndpointFallsBackToHost(t *testing.T) {
	g := interfaceModelGraph()
	got, ok := resolveEndpoint(g, endpointEntity("10.0.0.5", "9999"))
	if !ok {
		t.Fatal("endpoint should resolve to at least the host")
	}
	if got.ID != "hb" {
		t.Fatalf("resolved to %s, want the host hb", got.ID)
	}
}

// An external / off-fleet peer resolves to nothing — a first-class outcome.
func TestResolveEndpointUnknownPeer(t *testing.T) {
	g := interfaceModelGraph()
	if got, ok := resolveEndpoint(g, endpointEntity("203.0.113.9", "443")); ok {
		t.Fatalf("unknown peer must not resolve, got %s", got.ID)
	}
}

// server.port is conventionally an integer; an int-valued port must resolve
// exactly as the string form (identityValue formats it to decimal).
func TestResolveEndpointIntPort(t *testing.T) {
	g := interfaceModelGraph()
	ep := model.Entity{
		ID:   "ep1",
		Type: model.TypeNetworkEndpoint,
		Identity: []model.KeyValue{
			{Key: "server.address", Value: sv("10.0.0.5")},
			{Key: "server.port", Value: model.IntValue(443)},
			{Key: "network.transport", Value: sv("tcp")},
		},
	}
	got, ok := resolveEndpoint(g, ep)
	if !ok || got.ID != "lisB" {
		t.Fatalf("int-port resolution failed: got %q ok=%v, want lisB", got.ID, ok)
	}
}

// An IPv6 server.address resolves through the interface model like any other
// literal; the colons in the address must not confuse the (listener-side) port
// parsing, which only ever parses service.endpoint.
func TestResolveEndpointIPv6(t *testing.T) {
	hb := model.Entity{ID: "hb", Type: model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: sv("hb-uuid")}}}
	lis := model.Entity{ID: "lisB", Type: model.TypeServiceListener,
		Identity:   []model.KeyValue{{Key: "service.endpoint", Value: sv("hb-uuid:443/tcp")}},
		Attributes: []model.KeyValue{{Key: "listen.address", Value: sv("::")}}}
	iface := model.Entity{ID: "ifB", Type: model.TypeNetworkInterface,
		Identity: []model.KeyValue{{Key: "interface.name", Value: sv("eth0")}}}
	addr := model.Entity{ID: "addrB", Type: model.TypeNetworkAddress,
		Identity: []model.KeyValue{{Key: "network.address", Value: sv("fe80::1")}}}
	g := &fakeGraph{
		entities: map[model.EntityID]model.Entity{
			hb.ID: hb, lis.ID: lis, iface.ID: iface, addr.ID: addr,
		},
		deleted: map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "r1", Type: model.RelRunsOn, From: lis.ID, To: hb.ID},
			{ID: "r2", Type: model.RelHasInterface, From: hb.ID, To: iface.ID},
			{ID: "r3", Type: model.RelBoundTo, From: addr.ID, To: iface.ID},
		},
	}
	got, ok := resolveEndpoint(g, endpointEntity("fe80::1", "443"))
	if !ok || got.ID != "lisB" {
		t.Fatalf("IPv6 resolution failed: got %q ok=%v, want lisB", got.ID, ok)
	}
}

// network.transport is part of the endpoint identity: a udp endpoint must not
// resolve to a tcp listener on the same host:port. With no udp listener present,
// resolution falls back to the host rather than mis-binding.
func TestResolveEndpointRejectsProtoMismatch(t *testing.T) {
	g := interfaceModelGraph() // listener lisB is hb-uuid:443/tcp
	ep := model.Entity{
		ID:   "ep1",
		Type: model.TypeNetworkEndpoint,
		Identity: []model.KeyValue{
			{Key: "server.address", Value: sv("10.0.0.5")},
			{Key: "server.port", Value: sv("443")},
			{Key: "network.transport", Value: sv("udp")},
		},
	}
	got, ok := resolveEndpoint(g, ep)
	if !ok {
		t.Fatal("a udp endpoint on a known host should still resolve to the host")
	}
	if got.ID != "hb" {
		t.Fatalf("udp endpoint resolved to %s, want host hb (must not bind the tcp listener)", got.ID)
	}
}

// hostScopedGraph is two hosts; only host A carries a listener binding
// 127.0.0.1:6379. A fleet-wide bind scan would always land on it.
func hostScopedGraph() *fakeGraph {
	ha := model.Entity{ID: "ha", Type: model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: sv("ha-uuid")}}}
	hb := model.Entity{ID: "hb", Type: model.TypeHost,
		Identity: []model.KeyValue{{Key: "host.id", Value: sv("hb-uuid")}}}
	lisA := model.Entity{ID: "lisA", Type: model.TypeServiceListener,
		Identity:   []model.KeyValue{{Key: "service.endpoint", Value: sv("ha-uuid:6379/tcp")}},
		Attributes: []model.KeyValue{{Key: "listen.address", Value: sv("127.0.0.1")}}}
	return &fakeGraph{
		entities: map[model.EntityID]model.Entity{ha.ID: ha, hb.ID: hb, lisA.ID: lisA},
		deleted:  map[model.EntityID]bool{},
		relations: []model.Relation{
			{ID: "r1", Type: model.RelRunsOn, From: lisA.ID, To: ha.ID},
		},
	}
}

// hostScopedEndpoint is the ADR 0032 addendum form: a host-local address
// carrying the observing host's id as a fourth identity key.
func hostScopedEndpoint(hostID string) model.Entity {
	return model.Entity{
		ID:   "ep1",
		Type: model.TypeNetworkEndpoint,
		Identity: []model.KeyValue{
			{Key: "server.address", Value: sv("127.0.0.1")},
			{Key: "server.port", Value: sv("6379")},
			{Key: "network.transport", Value: sv("tcp")},
			{Key: "host.id", Value: sv(hostID)},
		},
	}
}

// A host-scoped endpoint must never resolve through the fleet-wide bind scan:
// host B's 127.0.0.1:6379 with no listener on B resolves to host B itself,
// not to host A's listener that happens to bind the same loopback tuple.
func TestResolveEndpointHostScopedNeverFleetScans(t *testing.T) {
	g := hostScopedGraph()
	got, ok := resolveEndpoint(g, hostScopedEndpoint("hb-uuid"))
	if !ok {
		t.Fatal("host-scoped endpoint on a known host should resolve to the host")
	}
	if got.ID != "hb" {
		t.Fatalf("resolved to %s, want host hb (must not bind host A's loopback listener)", got.ID)
	}
}

// With a listener on the scoped host's port, the host-scoped endpoint resolves
// to that listener — and to that one only, even though host A binds the same
// loopback tuple.
func TestResolveEndpointHostScopedToOwnListener(t *testing.T) {
	g := hostScopedGraph()
	lisB := model.Entity{ID: "lisB", Type: model.TypeServiceListener,
		Identity:   []model.KeyValue{{Key: "service.endpoint", Value: sv("hb-uuid:6379/tcp")}},
		Attributes: []model.KeyValue{{Key: "listen.address", Value: sv("127.0.0.1")}}}
	g.entities[lisB.ID] = lisB
	g.relations = append(g.relations, model.Relation{ID: "r2", Type: model.RelRunsOn, From: lisB.ID, To: "hb"})

	got, ok := resolveEndpoint(g, hostScopedEndpoint("hb-uuid"))
	if !ok || got.ID != "lisB" {
		t.Fatalf("host-scoped resolution: got %q ok=%v, want lisB", got.ID, ok)
	}
}

// A host-scoped endpoint whose host is unknown resolves to nothing — never to
// another host's matching bind. The honest gap beats a fabricated join.
func TestResolveEndpointHostScopedUnknownHost(t *testing.T) {
	g := hostScopedGraph()
	ep := hostScopedEndpoint("hb-uuid")
	ep.Identity[3].Value = sv("nope-uuid")
	if got, ok := resolveEndpoint(g, ep); ok {
		t.Fatalf("unknown scoped host must not resolve, got %s", got.ID)
	}
}

// A soft-deleted listener must not be handed back as the resolution: with lisB
// deleted, resolution falls through to the host rather than returning a dead
// entity.
func TestResolveEndpointSkipsDeletedListener(t *testing.T) {
	g := interfaceModelGraph()
	g.deleted["lisB"] = true
	got, ok := resolveEndpoint(g, endpointEntity("10.0.0.5", "443"))
	if !ok {
		t.Fatal("should still resolve to the host when the listener is gone")
	}
	if got.ID != "hb" {
		t.Fatalf("resolved to %s, want host hb (deleted listener must be skipped)", got.ID)
	}
}
