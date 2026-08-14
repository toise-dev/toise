// Package wire is the single in-repo spelling of the Toise entity-events wire
// vocabulary (the merged OTel entity-events convention; see ADR 0009, ADR 0022,
// and docs/data-model/otel-mapping.md). The emit SDK, the conformance kit, and
// Toise's own ingest boundary all import these constants, so producer and
// consumer can no longer drift apart one literal at a time. The frozen fixture
// (testdata/fixture_v1.bin) still pins the contract against the outside world;
// this package only pins the repo's code against itself.
//
// It is deliberately stdlib-only: importing the vocabulary must never pull a
// protocol stack into a producer's module graph.
package wire

// LogRecord EventName values. Entity lifecycle events are identified by the
// OTel LogRecord EventName, not by an attribute.
const (
	// EventEntityState asserts an entity's current state (create or update).
	EventEntityState = "entity.state"
	// EventEntityDelete releases the emitting producer's reference to the
	// entity; the entity is deleted when the last producer releases it
	// (ADR 0019).
	EventEntityDelete = "entity.delete"
)

// LogRecord attribute keys on entity events.
const (
	// AttrEntityType is the entity type, e.g. "host" or "service.listener".
	AttrEntityType = "entity.type"
	// AttrEntityID is the identifying attribute map. Exact-match identity:
	// every key and value counts (ADR 0018).
	AttrEntityID = "entity.id"
	// AttrEntityDescription is the descriptive (non-identifying) attribute map.
	AttrEntityDescription = "entity.description"
	// AttrEntityReportInterval is the producer's heartbeat cadence in SECONDS
	// (int). It arms the consumer's liveness backstop: an entity not
	// re-asserted within its interval is expired. It is a backstop, not a
	// primary delete signal.
	AttrEntityReportInterval = "entity.report.interval"
	// AttrEntityDeleteReason is the optional motive a producer attaches to an
	// entity.delete event (e.g. "terminated", "expired", "evicted",
	// "user_requested", "scaled_down"). It is an OPEN enum: those values are
	// illustrative, not exhaustive, and the consumer must never reject an
	// unrecognized one. Only meaningful on entity.delete events.
	AttrEntityDeleteReason = "entity.delete.reason"
	// AttrEntityRelationships is the embedded-relationship array an
	// entity.state event MAY carry: each element is a descriptor map (see the
	// Rel* keys). This is the sole on-wire relationship form (ADR 0022): the
	// source is the entity carrying the array, and removal is by absence on
	// re-emit.
	AttrEntityRelationships = "entity.relationships"
)

// Keys inside one entity.relationships descriptor map.
const (
	// RelType is the relationship type, e.g. "runs_on".
	RelType = "relationship.type"
	// RelTargetType is the target entity's type — spelled exactly like the
	// record-level key on purpose: a descriptor is a miniature entity
	// reference.
	RelTargetType = AttrEntityType
	// RelTargetID is the target entity's identity map.
	RelTargetID = AttrEntityID
	// RelConfidence and RelBasis are the belief attributes of an identity-alias
	// (same_as) relationship, the one exception to embedded edges being
	// attribute-free: Confidence is a scalar in [0,1] that the two entities are
	// the same real thing, Basis names the evidence (e.g. "ifPhysAddress",
	// "lldp_chassis"). The read-time canonical overlay collapses same_as edges at
	// or above a confidence threshold (ADR 0020); a same_as edge without a valid
	// confidence never collapses anything, so it is inert. Ignored on any other
	// relationship type.
	RelConfidence = "confidence"
	RelBasis      = "basis"
)

// Entity types — the registered vocabulary. Toise's boundary accepts these and
// refuses the rest under the default strict vocabulary, so a producer that
// spells a type by hand can only be right by luck. Deriving from these makes a
// unilaterally invented type a compile error instead of a rejected batch.
//
// Adding a type is additive and never breaks an existing one (ADR 0004).
const (
	// TypeHost is a machine, keyed by its machine-id.
	TypeHost = "host"
	// TypeProcess is a running process, keyed by {process.pid,
	// process.creation.time}: a recycled pid is a different entity.
	TypeProcess = "process"
	// TypeNetworkInterface is a network interface — a host NIC or a device port.
	TypeNetworkInterface = "network.interface"
	// TypeNetworkAddress is an IP address as a first-class node. It is the join
	// point between a host's routing view and SNMP-discovered topology, and is
	// deliberately NOT host-scoped: a LAN address is shared by construction.
	TypeNetworkAddress = "network.address"
	// TypeNetworkRoute is one routing-table entry (topology-as-entities, ADR 0022).
	TypeNetworkRoute = "network.route"
	// TypeServiceListener is a listening socket, keyed by service.endpoint.
	TypeServiceListener = "service.listener"
	// TypeServiceInstance is an OTel service instance: the producer itself, or a
	// service it observes. Keyed by a single service.instance.id.
	TypeServiceInstance = "service.instance"
	// TypeDatabase is a database instance. Its identity SHOULD be an id the
	// technology reports and persists across restarts, never a network address
	// (ADR 0018): an address names the path to the thing, not the thing.
	TypeDatabase = "db"
	// TypeNetworkDevice is a discovered network asset — switch, router, and so on.
	TypeNetworkDevice = "network.device"
	// TypeNetworkEndpoint is the observable remote-endpoint entity type — the
	// target of a depends_on edge, keyed on what a producer can actually see from
	// a socket ({server.address, server.port, network.transport}), never the
	// peer's host.id (the data-model MUST-NOT rule). A host-local address adds
	// host.id as a fourth key (ADR 0032). The consumer resolves it to the
	// canonical listener or host at read time (#184).
	TypeNetworkEndpoint = "network.endpoint"
	// TypeComputeVM is a virtual machine seen FROM its hypervisor, keyed by
	// {host.id of the hypervisor, vmid}. It is deliberately not a host: the
	// in-guest view is a separate entity, reconciled by same_as, never merged.
	TypeComputeVM = "compute.vm"
	// TypeContainer is an OCI/Docker container, keyed by a single container.id.
	TypeContainer = "container"
	// TypePod is a Kubernetes pod: the unit of scheduling and of failure, keyed
	// by the UID Kubernetes assigns it — never namespace/name, which is mutable
	// and reused. It sits between its containers and the node: a container
	// runs_on its pod, the pod runs_on its host, so a node failing takes the
	// pods, which take the containers. It owns the telemetry its containers
	// cannot: the network namespace is shared per pod, so pod-scoped network
	// measurements belong to it and to nothing else.
	TypePod = "pod"
	// TypeNetworkSegment is a broadcast and reachability domain bearing an
	// assigned identifier: two workloads on the same segment can address each
	// other, two on different segments cannot, whatever the firewall says.
	// Docker Swarm's overlay is an instance of that definition, not the
	// definition (ADR 0034).
	//
	// Identity is a single subtype-prefixed value by precedence, like
	// network.device.id. Only `swarm:<network-id>` is frozen, because the cluster
	// assigns it. `k8s:` and `vlan:` are deliberately NOT frozen: Kubernetes has
	// no object to identify in the default flat-network case, and a VLAN id is
	// not globally unique while a VLAN trunked across five switches is one
	// segment rather than five. Emitting either would engrave a wrong identity.
	//
	// Membership is a NECESSARY AND NOT SUFFICIENT condition for reachability:
	// network policies restrict on top of it, and on a flat cluster network
	// shared membership says almost nothing.
	TypeNetworkSegment = "network.segment"
)

// Relation types. The From/To pairings are canonical but advisory: they are not
// enforced at the boundary, so a relation may legitimately connect other
// registered types.
const (
	// RelTypeRunsOn attaches a process, service.instance, compute.vm or container
	// to the host it runs on.
	RelTypeRunsOn = "runs_on"
	// RelTypeHasInterface attaches an interface to the host or device that has it.
	RelTypeHasInterface = "has_interface"
	// RelTypeBoundTo attaches an address to the interface it is bound to.
	RelTypeBoundTo = "bound_to"
	// RelTypeNextHopVia links a route onward to its next-hop address.
	RelTypeNextHopVia = "next_hop_via"
	// RelTypeListensOn attaches a listener to the interface it listens on.
	RelTypeListensOn = "listens_on"
	// RelTypeMonitors records that a service.instance observes a target entity.
	// It is an observation, not ownership: nothing about the observer describes
	// the observed.
	RelTypeMonitors = "monitors"
	// RelTypeHasRoute attaches a routing-table entry to the device that holds it.
	RelTypeHasRoute = "has_route"
	// RelTypeConnectedTo is bare port-to-port link-layer adjacency (ADR 0022).
	// The edge carries no attributes; the ports do.
	RelTypeConnectedTo = "connected_to"
	// RelTypeDependsOn is the durable dependency relationship a producer asserts
	// from one of its own entities toward a remote network endpoint it observed
	// itself connecting to — the outbound, client-side edge of the
	// connection-topology model (ADR 0032). Open-enum, no belief attributes.
	RelTypeDependsOn = "depends_on"
	// RelTypeSameAs is the identity-belief relationship type ("these two entities
	// are the same real thing"), the sole edge that carries
	// RelConfidence/RelBasis. It does NOT merge the entities: the canonical
	// collapse is a read-time overlay (ADR 0020).
	RelTypeSameAs = "same_as"
	// RelTypeAttachedTo attaches a workload to the network segment it joined,
	// FROM the entity holding the network namespace the attachment belongs to: a
	// container on Docker and Swarm, a pod on Kubernetes (the namespace is shared
	// per pod). Not from the workload or the service — a Swarm service is not an
	// entity, so an edge from one would have no source (ADR 0034).
	RelTypeAttachedTo = "attached_to"
	// RelTypeHasSegment attaches a network segment to the cluster that declares
	// it, following the ownership family already in this vocabulary:
	// has_interface for a host's ports, has_route for a device's routes — a
	// logical network object declared by its owner, which is the shape here.
	//
	// A segment is emitted with this edge or not at all: a segment nobody can say
	// whose it is has no business in the graph.
	RelTypeHasSegment = "has_segment"
)

// Legacy relation types, superseded under topology-as-entities (ADR 0022) and
// NOT to be emitted by new producers. They stay registered so the boundary keeps
// accepting existing emitters; the device-level views they expressed are derived
// at read time instead.
const (
	// RelTypeRoutesVia is superseded by network.route + has_route + next_hop_via.
	RelTypeRoutesVia = "routes_via"
	// RelTypeForwardsTo is superseded by connected_to to the learned port.
	RelTypeForwardsTo = "forwards_to"
	// RelTypeAdjacentTo is superseded by port-to-port connected_to.
	RelTypeAdjacentTo = "adjacent_to"
)

// EntityTypes returns every registered entity type. A producer can use it to
// validate a type before emitting; Toise's own cross-check test uses it to prove
// this list and the engine registry are the same set, so neither can gain a type
// the other does not know.
func EntityTypes() []string {
	return []string{
		TypeHost, TypeProcess, TypeNetworkInterface, TypeNetworkAddress,
		TypeNetworkRoute, TypeServiceListener, TypeServiceInstance, TypeDatabase,
		TypeNetworkDevice, TypeNetworkEndpoint, TypeComputeVM, TypeContainer,
		TypePod, TypeNetworkSegment,
	}
}

// RelationTypes returns every registered relation type, legacy ones included:
// the boundary still accepts them, so a vocabulary that omitted them would be
// incomplete.
func RelationTypes() []string {
	return []string{
		RelTypeRunsOn, RelTypeHasInterface, RelTypeBoundTo, RelTypeNextHopVia,
		RelTypeListensOn, RelTypeMonitors, RelTypeHasRoute, RelTypeConnectedTo,
		RelTypeDependsOn, RelTypeSameAs, RelTypeAttachedTo, RelTypeHasSegment,
		RelTypeRoutesVia, RelTypeForwardsTo, RelTypeAdjacentTo,
	}
}

// Identity keys of a network.endpoint entity. A producer that keys an endpoint by
// hand MUST use exactly these spellings so that both ends of a hop derive the same
// identity — the continuity invariant that lets an outbound edge join the remote
// side's own emission (ADR 0032). The port is written in its string form, matching
// exact-match identity (a port is "443", not the int 443; see emit.Entity.ID).
const (
	EndpointServerAddress    = "server.address"
	EndpointServerPort       = "server.port"
	EndpointNetworkTransport = "network.transport"
)

// IdentityHostID is the identity key naming a machine. It appears on a host, on
// a compute.vm (the hypervisor's), and as the fourth key of a host-local
// network.endpoint, so its spelling must be identical everywhere: identity is
// byte-exact (ADR 0018), and the same machine written two ways is two entities.
//
// The contract pins the rendering to a lowercase hyphenated UUID (8-4-4-4-12).
// The raw 32-hex contents of /etc/machine-id are the same bytes and a different
// string, so a producer reading the file directly and one going through a
// library that formats it produce a silent, permanent duplicate of every host.
const IdentityHostID = "host.id"

// OTLP Resource attribute keys on a ResourceLogs carrying entity events.
const (
	// ResServiceName names the producing service (informational).
	ResServiceName = "service.name"
	// ResServiceInstanceID identifies the producing agent instance. It is the
	// producer identity liveness is reference-counted on (ADR 0019): set it
	// stable per producer instance, or every producer collapses into one
	// anonymous reference.
	ResServiceInstanceID = "service.instance.id"
)
