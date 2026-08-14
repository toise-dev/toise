# 34. The network segment is an entity, and only the subtype with an assigned identifier is frozen

- Status: Proposed
- Date: 2026-08-13
- Relates to: ADR 0004 (data model aligned with OTel entities), ADR 0018 (exact
  identity matching), ADR 0022 (engine stores facts only), ADR 0030 (deployment
  tiers — additive and opt-in), ADR 0032 (connection-derived edges; the
  loopback scoping precedent)

## Context

"Why can't A reach B?" is the first question asked during a deployment
incident, and the graph cannot answer it. Reachability has a shape the model
does not name: a **segment** — a virtual L2 domain that two workloads either
share, in which case they resolve each other by name and talk, or do not, in
which case they cannot, whatever the firewall says.

A Docker Swarm probe made this concrete. An overlay network is exactly such a
segment, spanning every node of the cluster. None of the registered network
types names it: `network.device` is a box, `.interface` a port, `.address` an
IP, `.route` a forwarding entry, `.endpoint` a socket. The producer did not
invent a type — an unregistered type is rejected at the boundary and dropped —
so segments ride today as **metric labels**: queryable, not traversable.

The same shape recurs beyond Swarm. Kubernetes has it when a cluster runs
additional networks; SNMP discovery has it as the VLAN. Framing one generic
type is better than framing three variants badly, and framing it *once* is only
possible before the first producer ships.

The difficulty is not the type. It is **the scope of the identity**. A Swarm
network id is assigned by the cluster and unique within it. A VLAN id is not:
VLAN 10 exists at every site, and using the bare number as identity would merge
unrelated segments across the estate — the `172.17.0.1` anti-pattern this
project already rejected for `network.address`, and the same failure ADR 0032
fixed for loopback endpoints by adding a fourth identity key. Scoping a VLAN by
device does not save it either: a VLAN trunked across five switches is **one**
segment, not five, so device scoping would fragment what it is meant to unify.
The broadcast domain coincides with neither a device nor a site, and no clean
answer exists today.

Kubernetes turns out to have the mirror-image problem, raised by the reference
producer's maintainer after this ADR's first draft assumed otherwise: in the
default case there is **no object to identify at all**. Every pod shares one
flat CNI network, which is not a first-class object; a `NetworkPolicy` is a
restriction that composes with others over the same population, not a segment;
a `Namespace` is an administrative boundary the network ignores. Freezing a
`k8s:` identity now would engrave an identifier for a thing that does not
exist — the very mistake being avoided for VLANs.

## Decision

**Register `network.segment` as an entity type, and freeze only the subtype
whose identifier is genuinely assigned.**

### The type, defined independently of its first subtype

A `network.segment` is a **broadcast and reachability domain bearing an
assigned identifier**. Docker Swarm's overlay is an instance of that
definition, not the definition.

Writing the definition before the first subtype is deliberate. A type born
knowing a single subtype comes to *mean* that subtype, and the second one
arrives arguing against a connotation instead of against a definition.

It is an entity rather than an attribute for the reason the `pod` decision
settled (see 0.12.0): **an attribute you have to string-match on is not a
join.** Answering "what else is on this segment" from a label means scanning
every service and comparing strings; as a node it is one hop.

### Identity — subtype-prefixed, by precedence

A single identity key holding a subtype-prefixed value, on the model of
`network.device.id`:

| Subtype | Status | Reason |
| --- | --- | --- |
| `swarm:<network-id>` | **frozen** | the cluster assigns an id, unique within it and not derived from an address |
| `k8s:` | **open** | no corresponding object in the default flat-network case |
| `vlan:` | **open** | the id is not globally unique and the broadcast domain matches neither a device nor a site |

The open subtypes are written into the contract **as open, with their
reasons**. A silence would read as an oversight; the reason is the useful part,
because it says what would have to be true for the subtype to close.

If a Kubernetes cluster does run `NetworkAttachmentDefinition` (Multus), that
object is the only defensible candidate — and it would be **its own subtype**,
not "the cluster network". It waits for a cluster that actually has one.

### The relation

`attached_to`, **from the entity that holds the network namespace the attachment
belongs to**, to the segment. Concretely a `container` on Docker and Swarm, and
a `pod` on Kubernetes — the namespace is shared per pod, which is the argument
that made `pod` a type in the first place. Not the workload: a Swarm service is
not an entity Toise models, so an edge from one would have no source.

The first draft said "from the workload", which had no producer able to emit it.
The correction came from the reference producer's maintainer, whose Swarm probe
emits the cluster as a `service.instance` and nothing per service; the real
attachment lives on the container, where the runtime actually reports it.

Subnet, internal flag and ingress marker are **descriptive**, never identity.

### Segments and attachments come from different producers

A cluster manager sees the segments; the nodes see which containers are attached
to them. So a complete picture needs **both** probes running, and Toise's
per-producer reference counting (ADR 0019) already handles the convergence: each
producer asserts what it observes, and the entity survives as long as any of them
does.

The consequence is operational and belongs here rather than being discovered by
the first integrator: **running only the manager probe yields segments with no
attachments**, which is a legitimate and stable state on the consumer side.
Toise has no entity-level orphan rule — an entity with no edges lives as long as
a producer keeps asserting it. Only a relation whose *endpoint* is missing is
parked and eventually dropped, which is a different mechanism.

### A segment is never emitted as an isolated node

That consumer-side tolerance is not enough, because it says nothing about what a
producer will actually put on the wire. The reference producer's emitter drops an
entity before emission unless it is a `host`, carries an outgoing edge, or is
targeted by another entity — a reasonable guard against emitting things nothing
refers to. A manager-only deployment would therefore emit **no segments at all**,
not segments without attachments: the guard would take them before the wire and
the consumer would see a graph that is empty on this point rather than merely
incomplete.

Rather than ask a producer to exempt the type, the segment carries an edge that
is **true**: an overlay belongs to the cluster that declares it, and is scoped to
it.

```
service.instance --has_segment--> network.segment   (the cluster declares it)
container        --attached_to--> network.segment   (the workload joins it)
```

`has_segment` follows the **ownership family the vocabulary already has**:
`has_interface` (a host declares its ports) and, closest of all, `has_route` (a
device declares its routes — a *logical* network object, not a physical one).
Same shape, same direction, same impact: From to To, the owner failing takes the
declared object with it.

An earlier draft anchored the segment with `runs_on` instead, to avoid a second
relation type. Two arguments killed it, both from the reference producer's
maintainer. The **semantic** one: `runs_on` means *executes on / is scheduled
onto*, and every type in its From set is a compute thing. Adding a network object
would give one relation two senses, so that "what runs on this cluster" would
start returning its networks — and once merged, the senses cannot be separated
again without another ADR. The **cost** one: the saving was illusory. Neither
relation exists yet, so both ship in the same SDK tag; the extra cost is a
constant, not a cycle.

The `pod` precedent does not carry here. Composing was right for a pod because a
pod genuinely *is* scheduled onto a node — the existing sense stretched, it did
not split. A segment is not scheduled anywhere.

The guard is still satisfied, on its **third** term: the cluster targets the
segment. And the rule this fixes for producers generally: **a segment is emitted
with its cluster edge or not at all.** That is not a workaround for one
producer's guard; it is what makes a segment's provenance explicit — a segment
nobody can say whose it is has no business in the graph.

### What the impact direction promises, and what it does not

Impact flows **target to source** — the segment failing takes what is attached
to it — the same direction as `runs_on`. That direction is a statement about
dependency, and it is correct.

It is **not** a promise that a failure event will arrive. An overlay is a
control-plane construct: it does not fail on its own. What fails is a node or the
underlying transport, and **no producer can currently mark a segment down** —
attached-task counts and subnet saturation are metrics, not health, and neither
belongs in a state key.

Both readings therefore have to be written, or the semantics look richer than
they are:

- `impact_of` on a segment stays meaningful, because it is defined over a
  **hypothetically** failing entity. *If this overlay broke, what would go with
  it* is a real question during a network migration or a fabric change, and it is
  exactly what a blast radius is for.
- **Nothing will propagate from a segment at runtime.** Do not build an alert on
  segment-failure propagation; it will never fire.

The alert-worthy event exists, but it is on the **edge**, not the node: a
container losing its overlay attachment is observable, is reported by the
runtime, and is why `attached_to` is **structural**.

### What the edge does not say

A shared segment says two workloads **could** reach each other, not that they
do. Network policies restrict on top of it; the producer measures attachment,
not traffic. This is a **necessary and not sufficient** condition, and the
contract states it as a limit rather than leaving a reader to discover it —
otherwise someone will use segment membership as an answer during an incident.

The Kubernetes case sharpens the point: on a flat cluster network, shared
membership says almost nothing about reachability.

## Consequences

- A fourteenth entity type and two relation types, `attached_to` and
  `has_segment`. Both ship in the same SDK tag. All additive: existing types,
  relations and stored events are untouched (ADR 0030).
- The usual ordering applies, met twice before: the SDK is tagged first, the
  engine registers the type second, the producer emits third. The root module
  resolves `pkg/emit` by published version, so a constant cannot be registered
  before it is tagged.
- Until registration lands, producers keep segments as metric labels and
  document that as a **transitional state, not a choice** — so nobody hunts for
  an edge that is coming later.
- The type ships with one frozen subtype. Accepted: a precedence ladder is
  built to extend, and `network.device.id` carries five tiers but could have
  shipped with one.
- Cardinality is low (dozens per cluster), so none of the scale concerns that
  deferred ADR 0020's Lot C apply here.
- Toise infers no reachability. The producer asserts membership; whether two
  members can talk is a consumer's reading of the graph, not a stored fact
  (ADR 0022).

## Relationship to other decisions

**ADR 0018 (exact identity)** is why the scope question dominates this ADR: with
no fuzzy matching, an identity whose scope is narrower than the observation
domain silently merges distinct things. **ADR 0032** solved that same problem
for loopback endpoints by adding a fourth identity key rather than tolerating
the collision; the open `vlan:` subtype is the same problem without a known
answer, and it stays open rather than being guessed.

**ADR 0022** keeps the boundary: membership is a producer-asserted fact,
reachability is not stored.
