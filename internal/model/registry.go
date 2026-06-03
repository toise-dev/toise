package model

// Phase-1 entity types. See ADR 0004. New types are added here without breaking
// existing ones.
const (
	TypeHost             = "host"
	TypeProcess          = "process"
	TypeNetworkInterface = "network.interface"
	TypeNetworkAddress   = "network.address"
	TypeNetworkRoute     = "network.route"
	TypeServiceListener  = "service.listener"
)

// Producer-vocabulary entity types, agreed with the senhub-agent producer for
// the first real-producer integration. See docs/data-model/senhub-agent-contract.md.
// Adding them is non-breaking (the phase-1 types are unchanged).
const (
	// TypeServiceInstance is an OTel service instance (e.g. the agent itself, or
	// a monitored service). Identity: a single service.instance.id.
	TypeServiceInstance = "service.instance"
	// TypeDatabase is a database instance. Identity SHOULD be a single composite
	// immutable key (e.g. db.instance.id). See the contract doc and ADR 0018
	// (exact identity matching).
	TypeDatabase = "db"
	// TypeNetworkDevice is a discovered network asset (switch, router, …).
	TypeNetworkDevice = "network.device"
)

// Phase-1 relation types. See ADR 0004.
const (
	RelRunsOn       = "runs_on"
	RelHasInterface = "has_interface"
	RelBoundTo      = "bound_to"
	RelNextHopVia   = "next_hop_via"
	RelListensOn    = "listens_on"
)

// Producer-vocabulary relation types (senhub-agent integration). The From/To
// entity types are the canonical pairing and are advisory — they are not
// enforced at runtime, so a relation may legitimately connect other registered
// types: `monitors`' target may be a host, db, or network.device, and
// `routes_via`/`adjacent_to` may be sourced from a `host` (Lot 4: a host's own
// routing/ARP tables link it to discovered network.devices).
const (
	RelMonitors   = "monitors"    // a service.instance monitors a target entity
	RelRoutesVia  = "routes_via"  // a network.device routes traffic via another
	RelForwardsTo = "forwards_to" // a network.device forwards traffic to another
	RelAdjacentTo = "adjacent_to" // two network.devices are link-layer adjacent
)

// RelationTypeDef describes a known relation type and its constraints.
type RelationTypeDef struct {
	Type string
	// From and To are the entity types the relation connects.
	From string
	To   string
	// Structural marks whether the relation's appearance/disappearance is
	// significant (suitable for alerting). See ADR 0006.
	Structural bool
}

var entityTypes = map[string]struct{}{
	TypeHost:             {},
	TypeProcess:          {},
	TypeNetworkInterface: {},
	TypeNetworkAddress:   {},
	TypeNetworkRoute:     {},
	TypeServiceListener:  {},
	// producer vocabulary
	TypeServiceInstance: {},
	TypeDatabase:        {},
	TypeNetworkDevice:   {},
}

var relationTypes = map[string]RelationTypeDef{
	RelRunsOn:       {Type: RelRunsOn, From: TypeProcess, To: TypeHost, Structural: true},
	RelHasInterface: {Type: RelHasInterface, From: TypeHost, To: TypeNetworkInterface, Structural: true},
	RelBoundTo:      {Type: RelBoundTo, From: TypeNetworkAddress, To: TypeNetworkInterface, Structural: true},
	RelNextHopVia:   {Type: RelNextHopVia, From: TypeNetworkRoute, To: TypeNetworkAddress, Structural: true},
	RelListensOn:    {Type: RelListensOn, From: TypeServiceListener, To: TypeNetworkInterface, Structural: true},
	// producer vocabulary (From/To advisory, not runtime-enforced)
	RelMonitors:   {Type: RelMonitors, From: TypeServiceInstance, To: TypeHost, Structural: true},
	RelRoutesVia:  {Type: RelRoutesVia, From: TypeNetworkDevice, To: TypeNetworkDevice, Structural: true},
	RelForwardsTo: {Type: RelForwardsTo, From: TypeNetworkDevice, To: TypeNetworkDevice, Structural: true},
	RelAdjacentTo: {Type: RelAdjacentTo, From: TypeNetworkDevice, To: TypeNetworkDevice, Structural: true},
}

// IsKnownEntityType reports whether t is a registered entity type.
func IsKnownEntityType(t string) bool {
	_, ok := entityTypes[t]
	return ok
}

// RelationDef returns the definition of relation type t, if registered.
func RelationDef(t string) (RelationTypeDef, bool) {
	d, ok := relationTypes[t]
	return d, ok
}

// EntityTypes returns the registered entity types (unordered).
func EntityTypes() []string {
	out := make([]string, 0, len(entityTypes))
	for t := range entityTypes {
		out = append(out, t)
	}
	return out
}

// RelationTypes returns the registered relation type definitions (unordered).
func RelationTypes() []RelationTypeDef {
	out := make([]RelationTypeDef, 0, len(relationTypes))
	for _, d := range relationTypes {
		out = append(out, d)
	}
	return out
}
