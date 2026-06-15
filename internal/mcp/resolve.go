package mcp

import (
	"strings"

	"github.com/toise-dev/toise/internal/model"
)

// resolveEndpoint binds an observed network.endpoint entity (identity
// {server.address, server.port, network.transport}) to the canonical entity it
// denotes: the remote service.listener, or its host when no specific listener
// is known. It is a READ-TIME overlay over already-registered topology and is
// NEVER written back into the log (#184) — the engine keeps only the
// producer-asserted fact "A depends_on <endpoint>", and this resolution is a
// derived projection (the ADR 0020 weighted-multi-source posture).
//
// Resolution tries, in order:
//  1. a service.listener that binds this address:port directly (listen.address);
//  2. the host that owns the address via the interface model
//     (network.address bound_to network.interface, host has_interface), then
//     that host's listener on the port — this covers wildcard (0.0.0.0) binds.
//
// Returns the resolved entity and true; (zero, false) when the endpoint denotes
// nothing known (an external / off-fleet peer), which is itself a useful signal.
func resolveEndpoint(g Graph, endpoint model.Entity) (model.Entity, bool) {
	addr := identityValue(endpoint, "server.address")
	if addr == "" {
		return model.Entity{}, false
	}
	port := identityValue(endpoint, "server.port")
	transport := identityValue(endpoint, "network.transport")

	if l, ok := listenerByBind(g, addr, port, transport); ok {
		return l, true
	}
	host, ok := hostForAddress(g, addr)
	if !ok {
		return model.Entity{}, false
	}
	if port != "" {
		if l, ok := listenerOnHostPort(g, host, port, transport); ok {
			return l, true
		}
	}
	return host, true
}

// identityValue returns the value of an identifying attribute, or "".
func identityValue(e model.Entity, key string) string {
	for _, kv := range e.Identity {
		if kv.Key == key {
			v, _ := valueString(kv.Value)
			return v
		}
	}
	return ""
}

// attrValue returns the value of a descriptive attribute, or "".
func attrValue(e model.Entity, key string) string {
	for _, kv := range e.Attributes {
		if kv.Key == key {
			v, _ := valueString(kv.Value)
			return v
		}
	}
	return ""
}

// listenerPort extracts the port from a service.listener's service.endpoint
// identity, formatted "<host>:<port>[/<proto>]". Parsing relies only on the port
// being the last ':'-delimited segment before an optional trailing "/<proto>";
// the host prefix is opaque (a host id or name, and may itself contain colons).
func listenerPort(l model.Entity) string {
	ep := identityValue(l, "service.endpoint")
	if slash := strings.LastIndex(ep, "/"); slash >= 0 {
		ep = ep[:slash]
	}
	colon := strings.LastIndex(ep, ":")
	if colon < 0 {
		return ""
	}
	return ep[colon+1:]
}

// listenerProto extracts the transport from a service.listener's service.endpoint
// ("<host>:<port>/<proto>"), or "" when the listener does not encode one.
func listenerProto(l model.Entity) string {
	ep := identityValue(l, "service.endpoint")
	if slash := strings.LastIndex(ep, "/"); slash >= 0 {
		return ep[slash+1:]
	}
	return ""
}

// protoMismatch reports whether a listener's transport is known to differ from
// the endpoint's. Both must be known to count: a listener that encodes no proto
// stays a candidate rather than being wrongly excluded. This keeps a tcp endpoint
// from resolving to a udp listener on the same host:port (they are distinct
// entities — network.transport is part of the endpoint identity).
func protoMismatch(l model.Entity, transport string) bool {
	p := listenerProto(l)
	return p != "" && transport != "" && p != transport
}

// listenerByBind finds a listener that binds addr (a concrete, non-wildcard IP)
// on port and transport. Works without the interface model, for listeners that
// bind a specific address.
func listenerByBind(g Graph, addr, port, transport string) (model.Entity, bool) {
	for _, l := range g.ListEntities(model.TypeServiceListener) {
		if attrValue(l, "listen.address") != addr {
			continue
		}
		if port != "" && listenerPort(l) != port {
			continue
		}
		if protoMismatch(l, transport) {
			continue
		}
		return l, true
	}
	return model.Entity{}, false
}

// hostForAddress resolves an IP to the host that owns it via the interface
// model: network.address(addr) bound_to network.interface, host has_interface.
func hostForAddress(g Graph, addr string) (model.Entity, bool) {
	var addrID model.EntityID
	found := false
	for _, a := range g.ListEntities(model.TypeNetworkAddress) {
		if identityValue(a, "network.address") == addr {
			addrID = a.ID
			found = true
			break
		}
	}
	if !found {
		return model.Entity{}, false
	}
	for _, bt := range g.ListRelations(model.RelBoundTo, addrID, "") {
		for _, hi := range g.ListRelations(model.RelHasInterface, "", bt.To) {
			if h, ok, deleted := g.GetEntity(hi.From); ok && !deleted && h.Type == model.TypeHost {
				return h, true
			}
		}
	}
	return model.Entity{}, false
}

// listenerOnHostPort finds the service.listener on the given host bound to port,
// following the runs_on edges into the host.
func listenerOnHostPort(g Graph, host model.Entity, port, transport string) (model.Entity, bool) {
	for _, r := range g.ListRelations(model.RelRunsOn, "", host.ID) {
		l, ok, deleted := g.GetEntity(r.From)
		if !ok || deleted || l.Type != model.TypeServiceListener {
			continue
		}
		if listenerPort(l) == port && !protoMismatch(l, transport) {
			return l, true
		}
	}
	return model.Entity{}, false
}
