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
	RelMonitors = "monitors" // a service.instance monitors a target entity
	// RelHasRoute attaches a routing-table entry to the device that holds it,
	// mirroring has_interface (device -> port). The route's metric/protocol ride on
	// the network.route entity (topology-as-entities, ADR 0022); next_hop_via links
	// it onward.
	RelHasRoute = "has_route"
	// RelConnectedTo is the bare, port-to-port link-layer adjacency in the
	// topology-as-entities model (ADR 0022): ports are network.interface entities,
	// so the edge carries no attributes (the ports do). It is the standard, spec-
	// embeddable form that supersedes adjacent_to + port attributes; device-level
	// adjacency is derived from it at read time, not stored.
	RelConnectedTo = "connected_to"

	// Legacy device-level edges — superseded under topology-as-entities (ADR 0022)
	// and NOT to be emitted by producers: routes_via is replaced by network.route +
	// has_route + next_hop_via; adjacent_to by port-to-port connected_to; forwards_to
	// (FDB) by connected_to to the learned port. They remain registered (so the
	// boundary still accepts them) but the contract derives the device-level views at
	// read time. See docs/data-model/otel-mapping.md.
	RelRoutesVia  = "routes_via"  // legacy: use network.route + has_route + next_hop_via
	RelForwardsTo = "forwards_to" // legacy: use connected_to to the learned port
	RelAdjacentTo = "adjacent_to" // legacy: use port-to-port connected_to
)

// ImpactFlow says in which direction a failure propagates across a relation
// of this type: when one endpoint fails, which other endpoint is affected.
// "X runs_on Y" means Y failing takes X down (impact flows To->From), while
// "X has_interface Y" means X failing takes Y down (From->To); connectivity
// edges propagate both ways. Unregistered types default to both — over-
// reporting impact is safer than silently missing it (#136).
type ImpactFlow int

const (
	// ImpactBoth: a failure at either endpoint affects the other.
	ImpactBoth ImpactFlow = iota
	// ImpactToFrom: a failure at To affects From (dependency edges: runs_on).
	ImpactToFrom
	// ImpactFromTo: a failure at From affects To (containment edges: has_interface).
	ImpactFromTo
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
	// Impact is the failure-propagation direction across this relation.
	Impact ImpactFlow
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
	// "X runs_on Y": the host failing takes the process down, not the reverse.
	RelRunsOn: {Type: RelRunsOn, From: TypeProcess, To: TypeHost, Structural: true, Impact: ImpactToFrom},
	// "X has_interface Y": the host/device failing takes its interface down.
	RelHasInterface: {Type: RelHasInterface, From: TypeHost, To: TypeNetworkInterface, Structural: true, Impact: ImpactFromTo},
	// "X bound_to Y": the interface failing takes the address down.
	RelBoundTo: {Type: RelBoundTo, From: TypeNetworkAddress, To: TypeNetworkInterface, Structural: true, Impact: ImpactToFrom},
	// "X next_hop_via Y": the next-hop address failing breaks the route.
	RelNextHopVia: {Type: RelNextHopVia, From: TypeNetworkRoute, To: TypeNetworkAddress, Structural: true, Impact: ImpactToFrom},
	// "X listens_on Y": the interface failing takes the listener down.
	RelListensOn: {Type: RelListensOn, From: TypeServiceListener, To: TypeNetworkInterface, Structural: true, Impact: ImpactToFrom},
	// producer vocabulary (From/To advisory, not runtime-enforced)
	// monitoring is observation, not dependency: a monitor failing does not
	// take the host down, nor the reverse — but losing the host DOES lose the
	// monitoring coverage, so model it as a dependency of the monitor.
	RelMonitors: {Type: RelMonitors, From: TypeServiceInstance, To: TypeHost, Structural: true, Impact: ImpactToFrom},
	// "X has_route Y": the device failing drops its routes.
	RelHasRoute: {Type: RelHasRoute, From: TypeNetworkDevice, To: TypeNetworkRoute, Structural: true, Impact: ImpactFromTo},
	// connectivity is symmetric: either side failing breaks the link.
	RelConnectedTo: {Type: RelConnectedTo, From: TypeNetworkInterface, To: TypeNetworkInterface, Structural: true, Impact: ImpactBoth},
	// legacy device-level edges (superseded; not emitted — see the const block)
	RelRoutesVia:  {Type: RelRoutesVia, From: TypeNetworkDevice, To: TypeNetworkDevice, Structural: true, Impact: ImpactToFrom},
	RelForwardsTo: {Type: RelForwardsTo, From: TypeNetworkDevice, To: TypeNetworkDevice, Structural: true, Impact: ImpactBoth},
	RelAdjacentTo: {Type: RelAdjacentTo, From: TypeNetworkDevice, To: TypeNetworkDevice, Structural: true, Impact: ImpactBoth},
}

// ImpactFlowOf returns the failure-propagation direction for a relation type.
// Unregistered types propagate both ways: over-reporting impact is safer than
// silently missing it.
func ImpactFlowOf(relType string) ImpactFlow {
	if def, ok := relationTypes[relType]; ok {
		return def.Impact
	}
	return ImpactBoth
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
