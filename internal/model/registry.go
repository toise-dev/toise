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
)

// Phase-1 relation types. See ADR 0004.
const (
	RelRunsOn       = wire.RelTypeRunsOn
	RelHasInterface = wire.RelTypeHasInterface
	RelBoundTo      = wire.RelTypeBoundTo
	RelNextHopVia   = wire.RelTypeNextHopVia
	RelListensOn    = wire.RelTypeListensOn
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
	// failure impact (ImpactNone); the canonical collapse over high-confidence
	// same_as edges is a deferred read-time overlay (ADR 0020, Lot B). The
	// producer states evidence it can justify; it never pre-merges (ADR 0018/0020).
	RelSameAs = wire.RelTypeSameAs

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
	RelRoutesVia  = wire.RelTypeRoutesVia  // legacy: use network.route + has_route + next_hop_via
	RelForwardsTo = wire.RelTypeForwardsTo // legacy: use connected_to to the learned port
	RelAdjacentTo = wire.RelTypeAdjacentTo // legacy: use port-to-port connected_to
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
	TypeNetworkEndpoint: {},
	TypeComputeVM:       {},
	TypeContainer:       {},
	TypePod:             {},
	TypeNetworkSegment:  {},
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
	RelMonitors: {Type: RelMonitors, From: TypeServiceInstance, To: TypeHost, Structural: true, Impact: ImpactToFrom},
	// "X has_route Y": the device failing drops its routes.
	RelHasRoute: {Type: RelHasRoute, From: TypeNetworkDevice, To: TypeNetworkRoute, Structural: true, Impact: ImpactFromTo},
	// connectivity is symmetric: either side failing breaks the link.
	RelConnectedTo: {Type: RelConnectedTo, From: TypeNetworkInterface, To: TypeNetworkInterface, Structural: true, Impact: ImpactBoth},
	// "X depends_on Y": the dependency target failing affects the dependent.
	RelDependsOn: {Type: RelDependsOn, From: TypeServiceInstance, To: TypeNetworkEndpoint, Structural: true, Impact: ImpactToFrom},
	// "X same_as Y": identity belief, any entity type either side (From/To advisory).
	// Non-structural (its appearance is not an alert) and ImpactNone (not a failure
	// path); the canonical collapse is a deferred read-time overlay (ADR 0020, Lot B).
	RelSameAs: {Type: RelSameAs, From: "", To: "", Structural: false, Impact: ImpactNone},
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
