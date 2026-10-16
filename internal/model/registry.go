package model

import "github.com/toise-dev/toise/pkg/emit/wire"

// Phase-1 entity types. See ADR 0004. New types are added here without breaking
// existing ones.
const (
	TypeHost             = wire.TypeHost
	TypeProcess          = wire.TypeProcess
	TypeNetworkInterface = wire.TypeNetworkInterface
	TypeNetworkAddress   = wire.TypeNetworkAddress
	TypeNetworkRoute     = wire.TypeNetworkRoute
	TypeServiceListener  = wire.TypeServiceListener
)

// Producer-vocabulary entity types, agreed with the senhub-agent producer for
// the first real-producer integration. See docs/data-model/senhub-agent-contract.md.
// Adding them is non-breaking (the phase-1 types are unchanged).
const (
	// TypeServiceInstance is an OTel service instance (e.g. the agent itself, or
	// a monitored service). Identity: a single service.instance.id.
	TypeServiceInstance = wire.TypeServiceInstance
	// TypeDatabase is a database instance. Identity SHOULD be a single composite
	// immutable key (e.g. db.instance.id). See the contract doc and ADR 0018
	// (exact identity matching).
	TypeDatabase = wire.TypeDatabase
	// TypeNetworkDevice is a discovered network asset (switch, router, …).
	TypeNetworkDevice = wire.TypeNetworkDevice
	// TypeNetworkEndpoint is a remote network endpoint observed by a producer
	// (e.g. the foreign end of an outbound TCP connection) but not canonically
	// identifiable by the observer. Identity is what the observer can see:
	// server.address + server.port + network.transport. The consumer resolves it
	// to the canonical host/service.listener at read time (#184), per the OTel
	// data-model rule "emit a different entity type keyed on what you can
	// reliably obtain" — see docs/design/netstat-connection-topology.md.
	TypeNetworkEndpoint = wire.TypeNetworkEndpoint
	// TypeComputeVM is a virtual machine as seen FROM its hypervisor, where only
	// the hypervisor's vmid is available, not the guest's machine-id. Identity:
	// {host.id (the hypervisor node), vmid}. It is deliberately NOT a `host`: a
	// host is keyed by machine-id, so a vmid in host.id would be a wrong,
	// permanent identity and would duplicate the in-guest host. The in-guest view
	// (an agent inside the VM) is the `host` {machine-id}; the two are distinct
	// facets reconciled later by a same_as overlay, never merged.
	TypeComputeVM = wire.TypeComputeVM
	// TypeContainer is an OCI/Docker container: a compute resource, not a service
	// instance (it may run a service, but the container is the thing). Identity: a
	// single container.id.
	TypeContainer = wire.TypeContainer
	// TypePod is a Kubernetes pod, keyed by the UID Kubernetes assigns it. It sits
	// between its containers and the node — a container runs_on its pod, the pod
	// runs_on its host — so failure propagates transitively without a new relation
	// type. It carries the pod-scoped telemetry no single container can own: the
	// network namespace is shared per pod.
	TypePod = wire.TypePod
	// TypeNetworkSegment is a broadcast and reachability domain bearing an
	// assigned identifier — the entity that lets "why can't A reach B" be a graph
	// question (ADR 0034). Identity is a subtype-prefixed value by precedence, of
	// which only `swarm:` is frozen; `k8s:` and `vlan:` are deliberately open
	// because neither has a scope that can be spelled correctly yet.
	//
	// Membership is NECESSARY AND NOT SUFFICIENT for reachability: policies
	// restrict on top of it, so the graph says two workloads COULD address each
	// other, never that they do.
	TypeNetworkSegment = wire.TypeNetworkSegment
	// TypeNetworkNeighbor is a link-layer neighbor an observer saw on one of its
	// own interfaces and could not resolve to an entity it already holds (ARP/NDP,
	// an FDB line, an LLDP remote known only by its chassis MAC). Identity is the
	// observer plus the MAC: a MAC is not globally unique, and the broadcast domain
	// it would otherwise be scoped to is exactly what carries no assigned
	// identifier (ADR 0034). Two observers on one segment hold two neighbors for
	// one device on purpose — a wrong merge is permanent, a split is reconciled by
	// a same_as belief once something identifies the far end (ADR 0020).
	TypeNetworkNeighbor = wire.TypeNetworkNeighbor

	// TypeK8sNode is the cluster's view of a machine, keyed {k8s.node.uid}. NOT a
	// second host: it carries the machine's ROLE IN THE CLUSTER — taints, labels,
	// allocatable capacity, conditions — which are not facts about the machine. A
	// node cordoned while its host is perfectly healthy is what makes the two
	// distinct. Joined to its host by a same_as belief, because each identity is
	// complete on its own and the collapse would lose no identifying attribute.
	TypeK8sNode = wire.TypeK8sNode

	// TypeHostService is a unit the system's supervisor manages — a systemd unit,
	// a Windows service — keyed {host.id, host.service.name} with the FULL name
	// the supervisor gives, since nginx.service and nginx.socket are two units.
	// Our extension of the upstream host.* namespace, on the entity.host.cpu
	// precedent; see wire for the gap it proposes to fill.
	TypeHostService = wire.TypeHostService
)

// Phase-1 relation types. See ADR 0004.
const (
	RelRunsOn       = wire.RelTypeRunsOn
	RelHasInterface = wire.RelTypeHasInterface
	RelBoundTo      = wire.RelTypeBoundTo
	// RelNeighborOf attaches an unresolved link-layer neighbor to the interface
	// that observed it.
	RelNeighborOf = wire.RelTypeNeighborOf
	RelNextHopVia = wire.RelTypeNextHopVia
	RelListensOn  = wire.RelTypeListensOn
)

// Producer-vocabulary relation types (senhub-agent integration). The From/To
// entity types are the canonical pairing and are advisory — they are not
// enforced at runtime, so a relation may legitimately connect other registered
// types: `monitors`' target may be a host, db, network.device, compute.vm, or
// container; `runs_on`'s source may be a process, service.instance, compute.vm,
// container or pod, and a container's `runs_on` target is its pod when one exists; and `routes_via`/`adjacent_to` may be
// sourced from a `host` (Lot 4: a host's own routing/ARP tables link it to
// discovered network.devices).
const (
	RelMonitors = wire.RelTypeMonitors // a service.instance monitors a target entity
	// RelHasRoute attaches a routing-table entry to the device that holds it,
	// mirroring has_interface (device -> port). The route's metric/protocol ride on
	// the network.route entity (topology-as-entities, ADR 0022); next_hop_via links
	// it onward.
	RelHasRoute = wire.RelTypeHasRoute
	// RelConnectedTo is the bare, port-to-port link-layer adjacency in the
	// topology-as-entities model (ADR 0022): ports are network.interface entities,
	// so the edge carries no attributes (the ports do). It is the standard, spec-
	// embeddable form that supersedes adjacent_to + port attributes; device-level
	// adjacency is derived from it at read time, not stored.
	RelConnectedTo = wire.RelTypeConnectedTo

	// RelDependsOn is a durable dependency a producer asserts from one of its own
	// entities to a remote endpoint it depends on (the foreign end of a
	// persistent outbound connection). Source-carried per the embedded-relationship
	// model; the target is a network.endpoint the consumer resolves. "depends_on"
	// is a sanctioned example type in the merged OTel entity spec but has no
	// normative semantics yet — treat as transitional (#184).
	RelDependsOn = wire.RelTypeDependsOn

	// RelSameAs is a producer-asserted identity belief: "these two entities are
	// the same real thing", with edge attributes confidence (0-1) and basis (e.g.
	// hyperv-kvp, serial_match). It does NOT merge the entities and carries no
	// failure impact (ImpactNone): the canonical collapse over high-confidence
	// same_as edges is a read-time overlay, SHIPPED and read by both surfaces
	// (internal/canonical, ADR 0020). It is never written back — the exact
	// entities and any low-confidence or conflicting evidence stay intact, and
	// only the grouping is derived. The producer states evidence it can justify;
	// it never pre-merges (ADR 0018/0020).
	RelSameAs = wire.RelTypeSameAs
	// RelResolvesTo is a resolution belief: the MAC an unresolved
	// network.neighbor was seen by is OWNED by this interface. Not where the
	// address was heard — RelNeighborOf already says that.
	//
	// Deliberately not a same_as: same_as feeds the read-time canonical collapse,
	// so whatever it links we are saying we eventually want collapsed into one
	// entity. A neighbor's identity CONTAINS its observer, and collapsing it into
	// a port would drop the observer and with it the only thing making the
	// observation attributable. The test: same_as when the collapse would lose no
	// identifying attribute, this when it would.
	//
	// It carries confidence and basis, the second being a closed vocabulary
	// because the sources are not equally strong: lldp states its own identity,
	// arp/ndp map a MAC to an address, fdb heard it on a port.
	RelResolvesTo = wire.RelTypeResolvesTo
	// RelAggregatedInto links a physical interface to the logical one that
	// aggregates it — a bond or team member to its master, a switch port to its
	// port-channel.
	//
	// Without it a path stops at every aggregate, which means at every network
	// core and every hypervisor: the addresses, the routes and therefore the
	// egress interface sit on the LOGICAL interface while LLDP and the forwarding
	// table see the PHYSICAL members.
	RelAggregatedInto = wire.RelTypeAggregatedInto

	// RelAttachedTo joins a workload to a network segment, FROM the entity that
	// holds the network namespace the attachment belongs to: a container on
	// Docker and Swarm, a pod on Kubernetes. Not from the workload or the service
	// — a Swarm service is not an entity, so an edge from one would have no
	// source (ADR 0034).
	RelAttachedTo = wire.RelTypeAttachedTo
	// RelHasSegment attaches a network segment to the cluster that declares it,
	// following the ownership family already here: has_interface for a host's
	// ports, has_route for a device's routes — a logical network object declared
	// by its owner. A segment is emitted with this edge or not at all.
	RelHasSegment = wire.RelTypeHasSegment

	// Legacy device-level edges — superseded under topology-as-entities (ADR 0022)
	// and NOT to be emitted by producers: routes_via is replaced by network.route +
	// has_route + next_hop_via; adjacent_to by port-to-port connected_to; forwards_to
	// (FDB) by connected_to to the learned port. They remain registered (so the
	// boundary still accepts them) but the contract derives the device-level views at
	// read time. See docs/data-model/otel-mapping.md.
	// The deprecation markers are aimed at producers, and the engine is the one
	// legitimate caller: the boundary must keep accepting these until 1.0, and
	// spelling the strings by hand here instead would break the cross-check that
	// proves the wire vocabulary and this registry are the same set.
	RelRoutesVia  = wire.RelTypeRoutesVia  //nolint:staticcheck // legacy, still accepted at the boundary: use network.route + has_route + next_hop_via
	RelForwardsTo = wire.RelTypeForwardsTo //nolint:staticcheck // legacy, still accepted at the boundary: use connected_to to the learned port
	RelAdjacentTo = wire.RelTypeAdjacentTo //nolint:staticcheck // legacy, still accepted at the boundary: use port-to-port connected_to
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
	// ImpactNone: a failure crosses this edge in neither direction — it is not a
	// dependency. Used by identity-belief edges (same_as): an alias assertion is
	// not a failure path, and a low-confidence one must not inflate blast radius.
	ImpactNone
)

// RelationTypeDef describes a known relation type and its constraints.
type RelationTypeDef struct {
	Type string
	// From and To are the entity types the relation connects, and they are
	// ADVISORY: nothing validates an observed relation against them. Only
	// Structural and Impact are read by the engine. An empty string means
	// unconstrained — several types are legitimate, so none is named.
	//
	// Keep them honest anyway. They are what a reader consults to learn the
	// model, and a constraint nothing enforces is a claim: monitors declared
	// To: host for three releases while probes were meant to watch listeners,
	// endpoints and addresses too, and no test could notice. See #494.
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
	TypeK8sNode:          {},
	TypeHostService:      {},
	// producer vocabulary
	TypeServiceInstance: {},
	TypeDatabase:        {},
	TypeNetworkDevice:   {},
	TypeNetworkEndpoint: {},
	TypeComputeVM:       {},
	TypeContainer:       {},
	TypePod:             {},
	TypeNetworkSegment:  {},
	TypeNetworkNeighbor: {},
}

var relationTypes = map[string]RelationTypeDef{
	// "X runs_on Y": the host failing takes what runs on it down, not the reverse.
	// From is the canonical process; service.instance, compute.vm, and container
	// also runs_on a host (From/To advisory). Impact To->From: the host failing
	// takes the VM/container/process down.
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
	// A probe's target is whatever it was pointed at: a host, a local listener,
	// a remote endpoint, or a bare address for an ICMP check. To is left
	// unconstrained rather than naming one of them, because naming one made
	// this entry assert something about the graph that was never true.
	//
	// ImpactNone: watching a thing is not depending on it. A monitored host
	// going down does not make the monitoring agent unhealthy — the agent is
	// fine and its reading is bad, which is a different fact. The previous
	// ImpactToFrom made every probe target a failure path into its own agent,
	// so one unreachable target inflated the blast radius by the whole fleet
	// that agent watches.
	RelMonitors: {Type: RelMonitors, From: TypeServiceInstance, To: "", Structural: true, Impact: ImpactNone},
	// "X has_route Y": the device failing drops its routes.
	RelHasRoute: {Type: RelHasRoute, From: TypeNetworkDevice, To: TypeNetworkRoute, Structural: true, Impact: ImpactFromTo},
	// "X neighbor_of Y": an observation, and ImpactNone like same_as, because an
	// observation is not a failure path in either direction.
	//
	// The first version of this read To->From, by analogy with monitors: losing the
	// interface loses the observation, so model the observed as depending on the
	// observer. Measured, that analogy is wrong, and expensively. monitors points
	// at a target from OUR monitor, so coverage loss lands on something we own.
	// Here the dependent side is a third-party device's record, and claiming it is
	// impacted says a printer stops working because the machine watching it died.
	// With To->From, impact_of on a host walked has_interface then every
	// neighbor_of and reported each ARP neighbor as impacted: on a host with a few
	// hundred ARP entries, a host failure would claim a few hundred devices. The
	// tool whose whole value is a believable blast radius would have become
	// unusable on any host that resolves addresses, which is all of them.
	//
	// Losing visibility of a segment is a coverage question, not a blast radius,
	// and it does not belong in this answer.
	//
	// Structural: a device appearing on a segment is the alertable event, and it is
	// the reason the type exists. What keeps that from flooding the change feed is
	// the lifecycle, not the flag — a long declared interval and no retraction by
	// absence, because an ARP cache evicts and an eviction is not a departure.
	RelNeighborOf: {Type: RelNeighborOf, From: TypeNetworkNeighbor, To: TypeNetworkInterface, Structural: true, Impact: ImpactNone},
	// connectivity is symmetric: either side failing breaks the link.
	RelConnectedTo: {Type: RelConnectedTo, From: TypeNetworkInterface, To: TypeNetworkInterface, Structural: true, Impact: ImpactBoth},
	// "X depends_on Y": the dependency target failing affects the dependent.
	RelDependsOn: {Type: RelDependsOn, From: TypeServiceInstance, To: TypeNetworkEndpoint, Structural: true, Impact: ImpactToFrom},
	// "X same_as Y": identity belief, any entity type either side (From/To advisory).
	// Non-structural (its appearance is not an alert) and ImpactNone (not a failure
	// path); the canonical collapse is a read-time overlay, shipped and never
	// written back (internal/canonical, ADR 0020).
	RelSameAs: {Type: RelSameAs, From: "", To: "", Structural: false, Impact: ImpactNone},
	// "X resolves_to Y": the interface Y owns the MAC the neighbor X was seen by.
	// Non-structural, because a new resolution improves coverage rather than
	// being an alertable event, and SNMP discovering ports would flood the change
	// feed. ImpactNone for the same reason as neighbor_of: the dependent side is
	// a third party's record, and measured, an impact there made impact_of on a
	// host report every ARP cache entry — a few hundred devices on one failure.
	RelResolvesTo: {Type: RelResolvesTo, From: TypeNetworkNeighbor, To: TypeNetworkInterface, Structural: false, Impact: ImpactNone},
	// "X aggregated_into Y": the physical interface X is a member of the logical
	// interface Y. Structural, because a port leaving a port-channel is a real
	// degradation operators alert on and membership does not churn.
	//
	// ImpactNone, and for a MEASURED reason rather than caution: a member failing
	// does not take the aggregate down, that being the entire point of a LAG, so
	// propagating would assert the false alarm the aggregate exists to prevent.
	// The real impact is conditional on being the LAST active member, and the
	// impact walk works on edges without counting them. Nobody should later
	// "fix" this believing it an oversight.
	RelAggregatedInto: {Type: RelAggregatedInto, From: TypeNetworkInterface, To: TypeNetworkInterface, Structural: true, Impact: ImpactNone},
	// "X attached_to Y": the segment failing takes what is attached to it. The
	// direction states a dependency; it does not promise an event will arrive —
	// an overlay is a control-plane construct and no producer can mark one down
	// today, so impact_of answers a hypothetical while nothing propagates at
	// runtime (ADR 0034). Structural because the alertable event is here, on the
	// edge: a container losing its attachment is observable.
	RelAttachedTo: {Type: RelAttachedTo, From: TypeContainer, To: TypeNetworkSegment, Structural: true, Impact: ImpactToFrom},
	// "X has_segment Y": the cluster failing drops the segments it declares —
	// same shape and direction as has_interface and has_route.
	RelHasSegment: {Type: RelHasSegment, From: TypeServiceInstance, To: TypeNetworkSegment, Structural: true, Impact: ImpactFromTo},
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

// terminalEntityTypes are the types a traversal may end on but never pass
// through. An observed network-endpoint is terminal by ADR 0032: it is the
// destination a producer dialed, not a thing two dependents are part of, and
// nothing is declared to leave it. Two entities that dial the same endpoint
// converge on it; reading that convergence as a route between them asserts a
// link neither producer observed.
var terminalEntityTypes = map[string]struct{}{
	TypeNetworkEndpoint: {},
}

// crossOnlyByChangingRelationTypes are the types a traversal may pass through
// ONLY by entering and leaving through DIFFERENT relation types.
//
// They are not terminal — a real path crosses them — but the crossing is real
// only in one shape, and the shape is what tells it from a convergence:
//
//   - a network.neighbor is entered by neighbor_of (from the interface where the
//     address was heard) and left by resolves_to (to the interface that owns it).
//     Two neighbor_of edges mean one MAC seen on two interfaces of one host,
//     which is a convergence: the two interfaces are not linked by it.
//   - a network.interface entered by aggregated_into is left by connected_to.
//     Two aggregated_into edges mean two members of one bond, and traffic does
//     not pass from one member to another — it goes up into the logical
//     interface and into the machine.
//
// Both are #442's error in a new costume, and a per-type rule is what is left
// after the general one was refuted: a switch traversed enters and leaves by
// has_interface, both times, and that IS forwarding. Structurally the two cases
// are identical — the node is the From of both edges — so only the type
// separates them. The conjecture and its refutation are recorded in
// docs/data-model/reference-path.md.
var crossOnlyByChangingRelationTypes = map[string]struct{}{
	TypeNetworkNeighbor:  {},
	TypeNetworkInterface: {},
}

// CrossingNeedsADifferentRelation reports whether a traversal passing through an
// entity of type t must enter and leave it through different relation types.
func CrossingNeedsADifferentRelation(t string) bool {
	_, ok := crossOnlyByChangingRelationTypes[t]
	return ok
}

// IsTerminalEntityType reports whether t may be an endpoint of a traversal but
// not an intermediate hop.
func IsTerminalEntityType(t string) bool {
	_, ok := terminalEntityTypes[t]
	return ok
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
