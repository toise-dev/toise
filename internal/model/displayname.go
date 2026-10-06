package model

import "strings"

// displayNameKeys names, per entity type, the attributes that hold what a human
// calls the thing, in order of preference. A key may be an identifying one: an
// entity identified by its own name still has a name to show.
//
// Every type in the registry must appear here, in displayNameComposed, or in
// displayNameNone — a type that is in none of the three is an omission, not a
// decision, and TestEveryRegisteredTypeDecidesOnADisplayName fails on it.
//
// These keys are for RENDERING ONLY. A display name drifts — a host is renamed
// while its host.id does not — so nothing may key, join, or match on one.
var displayNameKeys = map[string][]string{
	TypeHost:            {"host.name"},
	TypeContainer:       {"container.name", "compose.service"},
	TypeServiceInstance: {"service.name"},
	// sys.name first: it is what an SNMP poll actually returns (sysName from the
	// MIB), measured present on 12 of 14 devices on the bench. The other two keys
	// are guesses that no producer has ever emitted — kept only because an
	// operator-set name would be more authoritative than the polled one.
	TypeNetworkDevice: {"network.device.name", "device.name", "sys.name"},
	// network.interface.name is the semconv spelling producers are migrating to;
	// interface.name is what the graph still holds for everything observed
	// before they did.
	TypeNetworkInterface: {"network.interface.name", "interface.name"},
	// A listener's identity is an endpoint carrying a host uuid. The name of the
	// process behind it is the readable part, when the producer observed one.
	TypeServiceListener: {"process.executable.name", "process.name"},
	TypeProcess:         {"process.name", "process.executable.name"},
	TypePod:             {"k8s.pod.name"},
	TypeComputeVM:       {"vm.name", "host.name"},
	TypeNetworkRoute:    {"route.destination"},
	// The address is what a human says for it. Rendering an identifying value is
	// already what network.endpoint does when it composes "127.0.0.1:5432" out of
	// its own identity keys, and LabelName drops the duplicate so a label does not
	// read "network.address 10.0.0.5 network.address=10.0.0.5".
	TypeNetworkAddress: {"network.address"},
}

// displayNameNone holds the types that deliberately have no display name, with
// the reason. Being in this map is a decision; being in none of the three maps
// is an omission, which is the distinction the registry test enforces.
var displayNameNone = map[string]string{
	// An assigned, opaque identifier (ADR 0034): "swarm:<network-id>" names the
	// segment no better than its identity already does, unlike an IP address,
	// which a human reads fluently. No producer emits one yet, so there is no
	// observed attribute to compose from either. Revisit with real segments.
	TypeNetworkSegment: "its id is an assigned opaque value and no producer emits a name",
}

// DisplayNameAbsentReason reports why a type deliberately has no display name,
// and whether that is the case at all. Being in displayNameNone is a decision;
// being in none of the three maps is an omission, and the two must not look
// alike to a consumer either — keeping the reason inside the binary preserved
// the distinction for whoever reads the source and lost it for everyone else,
// which is the half that matters (#408). A consumer that cannot tell a declared
// absence from a missing name writes a fallback for a type we chose not to name.
func DisplayNameAbsentReason(entityType string) (reason string, declared bool) {
	reason, declared = displayNameNone[entityType]
	return reason, declared
}

// DisplayName returns what a human calls an entity — "dash172", "senhub-ping",
// "10.0.0.5:5432" — or "" when nothing observed on it reads better than its
// identity.
//
// It is the value to render as-is. It is never a key: a rename moves it and
// leaves IdentityHash alone, which is the whole reason the two are separate.
//
// Some types have no single attribute holding their name and must have one
// composed. Doing that here rather than in each consumer is the point: every
// consumer needs a readable name, so every consumer would otherwise write its
// own cascade, with its own order and its own edge cases, and two products
// would show the same thing under two names.
func DisplayName(e Entity) string {
	if compose, ok := displayNameComposed[e.Type]; ok {
		if s := compose(e); s != "" {
			return s
		}
	}
	for _, key := range displayNameKeys[e.Type] {
		if s := attrValue(e, key); s != "" {
			return s
		}
	}
	return ""
}

// LabelName is DisplayName minus what a label would duplicate: a label prints
// the identifying attributes right after the name, so an entity identified by
// its own name would otherwise read "host web-server-1 host.name=web-server-1".
func LabelName(e Entity) string {
	name := DisplayName(e)
	if name == "" {
		return ""
	}
	for _, kv := range e.Identity {
		if kv.Value.Display() == name {
			return ""
		}
	}
	return name
}

// displayNameComposed holds the types whose readable name is built from several
// attributes rather than read from one.
var displayNameComposed = map[string]func(Entity) string{
	TypeNetworkEndpoint: endpointName,
	TypeDatabase:        databaseName,
}

// endpointName renders an endpoint as address:port, bracketing an IPv6 literal
// so the port stays legible — "[2a01:db8::1]:443" rather than an address with a
// trailing colon-number that reads as one more group.
func endpointName(e Entity) string {
	addr := attrValue(e, "server.address")
	if addr == "" {
		return ""
	}
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
		addr = "[" + addr + "]"
	}
	if port := attrValue(e, "server.port"); port != "" {
		return addr + ":" + port
	}
	return addr
}

// databaseName renders a database as "postgresql@10.0.0.5:5432". Its identity is
// db.instance.id, which the contract defines as a stable source identifier — a
// PostgreSQL system_identifier or a MySQL server_uuid — correct as a key and
// unreadable as a name, so the name is composed from the descriptive attributes
// the contract already requires. That it moves when a VIP moves is not an
// objection: a display name drifts by definition, which is why it is not a key.
func databaseName(e Entity) string {
	system := attrValue(e, "db.system.name")
	if system == "" {
		return ""
	}
	addr := attrValue(e, "server.address")
	if addr == "" {
		return system
	}
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "[") {
		addr = "[" + addr + "]"
	}
	if port := attrValue(e, "server.port"); port != "" {
		return system + "@" + addr + ":" + port
	}
	return system + "@" + addr
}

// attrValue reads a key from either attribute list. Identity is searched first:
// a key that identifies an entity is the more authoritative of the two when a
// producer sends both.
func attrValue(e Entity, key string) string {
	for _, kv := range e.Identity {
		if kv.Key == key {
			return kv.Value.Display()
		}
	}
	for _, kv := range e.Attributes {
		if kv.Key == key {
			return kv.Value.Display()
		}
	}
	return ""
}
