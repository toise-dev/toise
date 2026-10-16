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
	// TypeHostService is a unit the SYSTEM'S SUPERVISOR manages: a systemd unit
	// on Linux, a Windows service on Windows. One type for both, because it is
	// one concept — something the operating system is meant to start by itself.
	//
	// Identity is {host.id, host.service.name}, where the name is the FULL name
	// the supervisor gives: nginx.service and nginx.socket are two different
	// units, and keying on the bare name fuses them into one entity whose
	// attributes oscillate.
	//
	// OUR EXTENSION of the upstream host.* namespace, which describes the host
	// itself. The precedent is entity.host.cpu: a property the host owns,
	// nested in its namespace. Upstream defines nothing here — no entity, no
	// attribute, no issue — and the two collector-contrib receivers that touch
	// this ground use two divergent local vocabularies, which is the gap. An
	// experience report proposing the entity upstream is the companion to this
	// name; until it lands, treat the spelling as proposed rather than settled.
	TypeHostService = "host.service"

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

	// TypeK8sNode is a Kubernetes node: the cluster's view of a machine, keyed on
	// {k8s.node.uid}. It is NOT a second host — it is the facet carrying the
	// machine's ROLE IN THE CLUSTER, which are not facts about the machine: its
	// taints, its labels, its allocatable capacity, its conditions. A node
	// cordoned while its host is perfectly healthy is the case that makes the two
	// distinct, and the same shape as compute.vm beside host.
	//
	// It is linked to its host by a same_as belief, which the DaemonSet agent can
	// assert with the cleanest evidence there is: it reads node.UID and
	// node.Status.NodeInfo.MachineID from the SAME cluster-API object, so it holds
	// both identities itself. Each is a complete identity of one machine, so the
	// canonical collapse would lose no identifying attribute — which is the test
	// that makes this a same_as rather than a dedicated relation.
	//
	// The cluster-role attributes (k8s.node.name, k8s.kubelet.version) belong
	// here; host.uuid stays on the host, being a fact about the machine. So
	// "which hosts are cluster nodes" stops being a filter on an attribute and
	// becomes a walk over an observed same_as — a better answer, not just a moved
	// one.
	//
	// NAMING, stated because it is inconsistent on purpose and for one release
	// only: this is k8s.node while the pod above is still "pod". Renaming the pod
	// type re-keys every pod entity — the entity type is part of the identity
	// fingerprint — so it is an identity break, and this project groups identity
	// breaks into one release with one note rather than spending several. pod
	// becomes k8s.pod in that release, with the four bare identity keys and the
	// Kubernetes container key.
	TypeK8sNode = "k8s.node"
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
	// TypeNetworkNeighbor is a link-layer neighbor an observer saw answering on
	// one of its own interfaces, and could not resolve to anything it already
	// holds: an ARP or NDP entry, an FDB line, an LLDP remote known by its chassis
	// MAC alone. Identity is the OBSERVER plus the MAC — {host.id, mac} — because
	// a MAC is not a globally unique identity (it repeats under virtualization and
	// rotates under randomization) and the broadcast domain it would otherwise be
	// scoped to is precisely what has no assigned identifier (ADR 0034). Two
	// observers on one segment therefore hold two neighbors for one device, which
	// is the intended trade: a wrong merge can never be undone, while a split is
	// reconciled by a same_as belief the day a producer identifies the far end.
	// It is NOT a network.interface: fabricating a port for an unresolved MAC is
	// what the mapping document forbids as a phantom port, and this type exists so
	// the observation has a home that claims nothing it did not see.
	TypeNetworkNeighbor = "network.neighbor"
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
	// RelTypeNeighborOf attaches an unresolved link-layer neighbor to the
	// interface that OBSERVED it. Read actively: "this neighbor was seen on that
	// interface".
	//
	// It carries NO impact in either direction, which this comment got wrong
	// until it was measured. The first reading argued by analogy with monitors —
	// losing the interface loses the observation — but monitors points at a
	// target from OUR monitor, so coverage loss lands on something we own, while
	// here the dependent side is a third party's record. With impact flowing
	// interface to neighbor, impact_of on a host walked has_interface and then
	// every neighbor_of, reporting each ARP neighbor as impacted: on a host with
	// a few hundred cache entries, one host failure claimed a few hundred
	// third-party devices.
	RelTypeNeighborOf = "neighbor_of"
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
	// RelTypeResolvesTo links an unresolved link-layer neighbor to the interface
	// that OWNS its MAC — not to the place the address was heard, which is what
	// RelTypeNeighborOf already says. A host's ARP neighbor resolves to the
	// router's interface; a MAC learned on a switch port resolves to the host's
	// interface, never to the port.
	//
	// It is a BELIEF, with RelConfidence and RelBasis, and deliberately not a
	// same_as: same_as feeds the read-time canonical collapse, and whatever it
	// links we are saying we eventually want collapsed into one entity. A
	// neighbor's identity CONTAINS its observer, so collapsing it into a port
	// would drop the observer and with it the only thing that made the
	// observation attributable. The test is whether the collapse would lose an
	// identifying attribute: same_as when it would not, this when it would.
	//
	// The basis is a closed vocabulary because the sources are not equally
	// strong: lldp (the neighbor states its own identity), arp / ndp (an L3 cache
	// maps the MAC to an address), fdb (a forwarding table heard the MAC on a
	// port — the weakest, and the one that moves).
	//
	// A producer asserts it only when it holds BOTH identities itself, never by
	// matching a MAC against what another agent reported: a value two producers
	// happen to agree on is not an observation.
	RelTypeResolvesTo = "resolves_to"
	// RelTypeAggregatedInto links a physical interface to the logical interface
	// that aggregates it — a bond or team member to its master, a switch port to
	// its port-channel.
	//
	// It exists because the addresses, the routes and therefore the egress
	// interface sit on the LOGICAL interface while LLDP and the forwarding table
	// see the PHYSICAL members, with no edge between them: a path arrives at the
	// bond by its route and must leave by a member, so without this it stops at
	// every aggregate — which means at every network core and every hypervisor.
	//
	// It carries NO impact, and for a measured reason rather than caution: a
	// member failing does not take the aggregate down, that being the entire
	// point of a LAG, so propagating would assert the false alarm the aggregate
	// exists to prevent. The real impact is conditional on being the LAST active
	// member, and the impact walk works on edges without counting them.
	RelTypeAggregatedInto = "aggregated_into"
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
// NOT to be emitted. They stay registered so the boundary keeps accepting an
// existing emitter; the device-level views they expressed are derived at read
// time instead.
//
// All three are DEPRECATED and scheduled for removal in 1.0. They are still
// accepted today, and RelationTypes() still lists them, because the list states
// what the boundary accepts rather than what a producer should emit. A producer
// still emitting one has until 1.0 to move to the replacement named on each.
const (
	// Deprecated: superseded by network.route + has_route + next_hop_via. Still
	// accepted at the boundary, scheduled for removal in 1.0. No known producer
	// emits it.
	RelTypeRoutesVia = "routes_via"
	// Deprecated: superseded by connected_to to the learned port. Still accepted
	// at the boundary, scheduled for removal in 1.0. No known producer emits it.
	RelTypeForwardsTo = "forwards_to"
	// Deprecated: superseded by port-to-port connected_to. Still accepted at the
	// boundary, scheduled for removal in 1.0. No known producer emits it.
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
		TypePod, TypeK8sNode, TypeNetworkSegment, TypeNetworkNeighbor,
		TypeHostService,
	}
}

// RelationTypes returns every registered relation type, legacy ones included:
// the boundary still accepts them, so a vocabulary that omitted them would be
// incomplete.
func RelationTypes() []string {
	return []string{
		RelTypeRunsOn, RelTypeHasInterface, RelTypeBoundTo, RelTypeNextHopVia,
		RelTypeListensOn, RelTypeMonitors, RelTypeHasRoute, RelTypeConnectedTo,
		RelTypeDependsOn, RelTypeSameAs, RelTypeResolvesTo, RelTypeAggregatedInto,
		RelTypeAttachedTo, RelTypeHasSegment,
		RelTypeNeighborOf,
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

// IdentityMAC is the key carrying a hardware address: identifying on a
// network.neighbor, descriptive on a network.interface, and the same six bytes
// either way.
//
// The contract pins the rendering to IEEE RA hexadecimal — uppercase,
// hyphen-separated octets, most significant first (AA-BB-CC-DD-EE-FF) — which is
// the form semconv requires for host.mac. A MAC becomes an identity, and it is
// the value a same_as belief joins on when SNMP later names the device a
// neighbor was first seen as: two renderings of the same address are two
// entities under exact identity, the join silently finds nothing, and the
// duplicate surfaces months later.
const IdentityMAC = "mac"

// IdentityNetworkDeviceID is the single subtype-prefixed identity of a polled
// network device. Its "mac:" rung carries an LLDP chassis-id, rendered per
// IdentityMAC.
const IdentityNetworkDeviceID = "network.device.id"

// IdentityNetworkInterfaceName names an interface within its owner — a host by
// host.id, or a polled device by network.device.id. It is the semconv spelling
// (release candidate since 1.44) and it is the ONLY one that may identify.
//
// The earlier spelling `interface.name` remains valid as a descriptive attribute
// and as a telemetry join key, because a graph still holds interfaces observed
// before the migration and dropping it would blind the pivot on them. It must
// never appear in an identity: identities are hashed over the keys as sent, with
// no normalization, so one port named two ways is two entities that nothing
// joins. The conformance kit refuses it in an identity and accepts it
// everywhere else.
const IdentityNetworkInterfaceName = "network.interface.name"

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
