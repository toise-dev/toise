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

`attached_to`, from the workload to the segment. **Structural**: a service
leaving a segment is topology worth alerting on. Impact flows **target to
source** — the segment failing takes what is attached to it — the same
direction as `runs_on`.

Subnet, internal flag and ingress marker are **descriptive**, never identity.

### What the edge does not say

A shared segment says two workloads **could** reach each other, not that they
do. Network policies restrict on top of it; the producer measures attachment,
not traffic. This is a **necessary and not sufficient** condition, and the
contract states it as a limit rather than leaving a reader to discover it —
otherwise someone will use segment membership as an answer during an incident.

The Kubernetes case sharpens the point: on a flat cluster network, shared
membership says almost nothing about reachability.

## Consequences

- A fourteenth entity type and a fourteenth relation type. Both additive:
  existing types, relations and stored events are untouched (ADR 0030).
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
