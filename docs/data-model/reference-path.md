# The reference path, hop by hop

Status: **normative for the 0.6.4 end-to-end workstream.** Written as the
cartography reference for `sa` (producer), `lab` (bench), `quart` and `martin`
(rendering), with `otel` holding compatibility with the upstream convention and
`sop` holding redeployment in other contexts.

It answers one question: *what is the path from an application to its database,
expressed only in things a producer asserts?*

## 1. The rule that governs everything below

**A path is made of edges a producer asserted. Nothing else is a hop.**

Four kinds of link can appear in a drawing of an information system, and they are
not interchangeable. The whole value of this document is that they stay apart:

| kind | what it is | how it must be rendered |
|---|---|---|
| **observed** | an edge a producer emitted, from something it saw | solid |
| **belief** | a `same_as` or a `resolves_to`, with a basis and a confidence (ADR 0020) | dashed, and the basis must be reachable |
| **classification** | an answer Toise computes about an entity, not an edge | a badge on the station, never a segment |
| **deduction** | a consumer joining two entities on a matching value | dotted, and labelled as the consumer's own |

A rendering that draws all four the same way is **wrong even when every hop is
right**, because the reader cannot tell what would have to be true for the
drawing to be false. This is the same rule the read surfaces have been converging
on all month: an answer says which kind of thing it is.

Two corollaries, both load-bearing:

- **No hop may be silent.** A path that cannot cross a gap says so at that
  position. It does not skip the gap and present a shorter path, and it does not
  return empty. `quart` renders the gap hatched; the answer must give it a
  position so there is something to hatch.
- **A path that crosses a belief is a believed path.** One dashed segment makes
  the whole path conditional, and the answer says which segment it was. A
  believed path is useful — it is usually the only thing there is — but it may
  never be reported as observed.

## 2. Terminal entities: why the path does not simply flow

`network.endpoint` is **terminal** (ADR 0032, enforced in
`internal/model/registry.go` and in the traversal since #442). It may be reached,
it may be the start, and it may only be left by an edge it is the *source* of —
and it is the source of none. Two services dialling the same object store
converge on one endpoint; reading that convergence as a route between them
asserts a link neither producer observed. On a fleet it makes a large fraction of
host pairs falsely reachable, which is what #442 measured and removed.

**So the reference path is not one path. It is two, plus a junction that is not
an edge.** Anyone who draws it as a single continuous line is drawing a deduction
at the junction, and must say so.

**One thing to know before reading the tables.** The `From` / `To` in the
relation registry are **advisory**: they name the canonical pair and they set the
direction impact propagates in. They are not enforced at ingest, and only
`ImpactFlowOf` and `RelationDef` consult them. So `has_route` is registered
`network.device → network.route` and a `host` asserting it is correct and
accepted — a host and a router both have routes. Read the registry for the
direction of *consequence*, not for a permitted-endpoints list.

## 3. Segment A — the client, outbound

| # | hop | relation and direction | status today |
|---|---|---|---|
| A0 | the application | **not an entity.** A label: `senhub.label.application` on the entities that belong to it | emitted |
| A1 | `service.instance` → `host` | `runs_on`, From service.instance | **observed** |
| A2 | `process` → `host` | `runs_on`, From process | **observed** |
| A3 | `host` → `network.interface` | `has_interface`, From host | **observed** |
| A4 | `network.address` → `network.interface` | `bound_to`, From address | **observed** |
| A5 | `host` → `network.route` | `has_route`, From host | **observed** |
| A6 | `network.route` → `network.address` (the gateway) | `next_hop_via`, From route | **observed** |
| A7 | gateway `network.address` → the router's `network.interface` | `bound_to`, From address | **observed** — see below |
| A8 | router `network.device` → that `network.interface` | `has_interface`, From device | **observed** |
| A9 | `network.interface` → `network.interface` | `connected_to`, symmetric | **observed** (LLDP) |

**A7 is the join that already works and nobody should re-derive.** The gateway
address the host reports in `next_hop_via` and the address SNMP reports bound to
the router's interface are **the same `network.address` entity** — same identity,
one node. Host and router therefore meet with no deduction at all. `sa` has a
test for it; `lab` should assert it as *one* entity with two incident edges, not
as two entities that look alike.

A1/A2 do not yet connect the process to the outbound call: nothing says *which*
process dialled. That is `quart`'s request 3, on the producer side, and until it
exists the first hop of the call is a deduction.

## 4. Segment B — the server, inbound

| # | hop | relation and direction | status today |
|---|---|---|---|
| B1 | `db` or `service.instance` → `host` | `runs_on` | **observed** |
| B2 | `service.listener` → `network.interface` | `listens_on`, From listener | **observed** |
| B3 | `host` → `network.interface` | `has_interface` | **observed** |
| B4 | `network.address` → `network.interface` | `bound_to` | **observed** |
| B5 | up into the fabric | `connected_to` / the L2 descent of §6 | **partly missing** |

## 5. The junction, and why it is not an edge

Segment A ends at `service.instance --depends_on--> network.endpoint`
(`<ip>:<port>`). Segment B holds a `network.address` with that IP and a
`service.listener` on that port. **Nothing joins them**, and that is deliberate
(#443).

Measured on production, 12/10: **97 endpoints, 34 whose IP matches a known
`network.address`.** The endpoint `141.94.207.32:5432` seen from one host and the
`network.address 141.94.207.32` held on another are two entities joined by
nothing but the equality of a string.

**Synthesising that edge is forbidden, and the reason is not aesthetic.** A bare
IP is not a globally unique identity: RFC 1918 ranges repeat across sites, so the
same `10.x` in two segments is two machines, and the fourth identity key on
host-local addresses exists precisely because a narrower scope once merged
distinct endpoints. A silent false positive here invents a path between two
customers' machines.

**What the product owes instead is a classification** (ADR 0032 §2, unbuilt):
per endpoint, is the far side *legitimately external* (draw it as external — the
egress map is a feature), *internal and unsupervised* (draw it as a coverage gap;
it is an onboarding signal), or a *key divergence* — the far host **is** monitored
under another key, which is the only true defect and today is invisible.

Until that exists: `quart` draws the junction **dotted and labelled as its own
deduction**, which is exactly what it already does. Do not change that to solid
when the classification lands; change it to a badge on the station.

## 6. The L2 descent — the link that is actually missing (ent#624)

Segment A9 works between two devices that both speak LLDP. It does **not** reach
a host whose access port has no L3 address and whose switch runs no LLDP toward
it. That is the missing rung, and `sa` is right that it is *the* one.

**Ruling, revised after `otel`'s objection: a dedicated relation,
`resolves_to`. Not `bound_to`, and not `same_as`.**

My first ruling was `same_as`, on the precedent that our model already links two
*different types* with it — a `compute.vm` seen from the hypervisor and the
in-guest `host` are the same machine, reconciled and never merged. `otel`
objected that two types are never the same entity. That objection as stated does
not hold against our own precedent, but it is right, and the reason is one step
further on:

**`same_as` is the input to the read-time canonical collapse (ADR 0020, Lot B).**
Whatever we link with it, we are saying we eventually want collapsed into one
canonical entity. For a VM and its in-guest host that is exactly right — one
machine, two facets, one canonical answer. For a neighbour and a switch port it
is wrong: the `network.neighbor` is an **observation**, with an observer in its
identity (`{host.id, mac}`), a 24 h cadence and no retraction by absence, and the
`network.interface` is a port with its own lifecycle. Collapsing them would
dissolve *who saw it and when*, which is the entire value of the ARP layer. The
neighbour must survive its own resolution.

So the link is not sameness. It is resolution:

```
network.neighbor --resolves_to--> network.interface   (the interface that OWNS the MAC)
```

- `From` network.neighbor, `To` network.interface. **Structural: false** — a new
  resolution is coverage improving, not an alertable event, and SNMP discovering
  ports would otherwise flood the change feed. **Impact: `ImpactNone`**, for the
  same reason `neighbor_of` carries none: the dependent side is a third party's
  record, and measured, impact there made one host failure claim a few hundred
  devices.
- It carries **`basis`** from a fixed vocabulary and **`confidence`** from the
  producer, exactly as `same_as` does. The three bases are not equally strong:
  `lldp` (the neighbour states its own identity), `arp` / `ndp` (an L3 cache maps
  the MAC to an address), `fdb` (a forwarding table heard the MAC on a port — the
  weakest, and the one that moves). Toise never raises a confidence.
- **A path crossing a `resolves_to` is a believed path**, rendered dashed, and the
  basis must be reachable on the answer. An `fdb`-based crossing is weaker than
  an `lldp`-based one and the reader must see which.

**Two things this costs us, both mine:**

1. Belief attributes are carried **only on `same_as`** edges today
   (`internal/ingest/embedded.go`). `resolves_to` needs that allowlist widened,
   or its basis and confidence are dropped at the boundary and the relation is a
   bare assertion — which is worse than `same_as` would have been.
2. `network.neighbor` is not in `terminalEntityTypes`, and once it carries an
   outgoing edge it becomes a transit: a MAC seen on two interfaces of one host
   would make `interface A → neighbour → interface B` traversable — a convergence
   rendered as a route, #442 one layer down. **Resolved below**, and not by making
   the type terminal: a path may cross a neighbour only by entering and leaving
   through different relation types, which admits the real descent and refuses the
   convergence.

Both are release gates on Toise before the producer enables emission.

### A producer emits `resolves_to` only when it holds BOTH identities

**Confirmed, and it is the same rule as the forbidden endpoint synthesis.** A
producer may assert the resolution only when it observed both ends itself: the
neighbour it recorded, and the interface that owns the MAC, as an entity it
already holds under its own key. **Never by matching a MAC string against
something another agent reported.** That is the #443 prohibition in another
costume — a value two producers happen to agree on is not an observation, and the
false positive is silent and permanent.

`sa` worked out the consequence before I did, and it is worth stating because it
bounds the feature: the descent closes for **the host's own port** and generally
nowhere else. An agent on host H that polls the switch sees its *own* MAC learned
on a port, and can resolve `{device.id, MAC_H}` to H's own `eth0` because it holds
both. For another host's MAC on another port it holds one end only, and must stay
silent.

That is enough for this path. The path needs H to reach the port H is plugged
into, and that is exactly the edge H can legitimately assert.

### The rule that makes the descent work is the same one that blocks the fake

The gate above — that a path may cross a `network.neighbor` only by **entering and
leaving through different relation types** — is not merely a safety rail. It is
the exact discriminator between the two cases, which is why it is the rule rather
than a special case:

- **the legitimate descent** enters the neighbour by `neighbor_of` (from the port
  where the MAC was heard) and leaves by `resolves_to` (to the interface that owns
  it). Different types: allowed.
- **the fake** is a MAC seen on two interfaces of one host: the traversal enters by
  `neighbor_of` and leaves by `neighbor_of`. Same type: refused, and it is refused
  for the reason #442 established — two things that converge on one observation
  are not connected to each other.

So `network.neighbor` does not need to be terminal. It needs this rule, which
costs one comparison and states the semantics instead of forbidding the type.

### Which MAC belongs to whom — the part that is easy to get backwards

The direction of `resolves_to` follows the MAC's **owner**, never the place it was
heard. The two edges are different and both already exist:

- a host resolving its gateway holds `network.neighbor {host.id, MAC_router}`.
  `neighbor_of` points at **the host's own interface**, where it was seen.
  `resolves_to` points at **the router's interface**, which owns that MAC.
- a switch learning a MAC on a port holds
  `network.neighbor {network.device.id, MAC_host}` — scoped on the device by the
  same rule that scopes a host's neighbour on itself. `neighbor_of` points at
  **the port**, where it was heard. `resolves_to` points at **the host's
  interface**, which owns that MAC.

So an FDB entry does not resolve to the port it was learned on. Saying it did
would claim the port's identity is the host NIC's MAC, and that is the phantom
port the mapping document forbids. My first draft of this document got that
backwards; `otel` caught the same thing from the other direction.

### `next_hop_via`, not `routes_via`

`otel` reports that our upstream one-pager says `routes_via` while the agent
emits `next_hop_via`, and asks which one stands. **`next_hop_via`**, with no
hesitation: `routes_via` is in our own registry as a **legacy device-level edge,
superseded and not emitted**, alongside `forwards_to` and `adjacent_to`. They are
still accepted at the boundary so old data reads, and nothing should be built on
them. The modern form is `network.route` as an entity plus `has_route` and
`next_hop_via`, which is what shipped and what the route's third identity key was
corrected for.

## 7. VLANs (`sa`'s gap 2)

**A VLAN becomes a `network.segment` only with an assigned identifier, and only
with its `has_segment` edge** (ADR 0034). A VLAN id is not globally unique, and
one VLAN trunked across five switches is **one** segment, not five — so
`vlan:<id>` alone is not an identity and must not be emitted. The `vlan:` rung is
deliberately open precisely because nobody has yet produced an identifier that
survives that.

So, for the bench: **do not publish VLANs as entities yet.** Carry the VLAN id as
a descriptive attribute on the port's `network.interface`, where it is true and
costs nothing. Membership is *necessary and not sufficient* for reachability, so
a segment would not have carried a hop of the path anyway. If a cluster or a
controller can name the segment — the way Swarm names a network id — that is a
different conversation and I will open it then.

## 8. Telemetry per hop (`martin`'s point 2, `sa`'s question)

`telemetry_keys` is filled **by Toise**, not by a producer-side declaration. It
walks the entity's identity and attributes and returns those keys that are in a
curated allowlist of attributes telemetry backends actually carry
(`internal/mcp/telemetry.go`). The producer's job is therefore only to put the
key on the entity — as identity or as a descriptive attribute — in its standard
spelling.

**And there is a hole exactly where this workstream needs it.** The allowlist
holds `network.device.id` and `network.interface.name` (plus `interface.name`,
the pre-semconv spelling, still carried because the retention window holds it).
It does **not** hold `snmp.target` or an interface index, and it should not: those
are poll-mechanics, not identity.

`sa` reports that the SNMP interface series carry `snmp.target`, the index and
the interface name. So a switch port can be selected by interface name and
**cannot be disambiguated by device**: two switches both have `Ethernet1`. The
hop is visible and not measurable, which is precisely the failure `martin` named.

**Producer rule: the interface metric series must carry `network.device.id` —
the same canonical value, byte for byte, that the entity's identity carries.**
Not the poll target, not the `sysName`, not the management IP unless that is what
the canonical id resolved to. Keep `snmp.target` as well; it is useful for
debugging the poller and useless as a join key. I would rather fix it on the
series than widen the allowlist, because widening it would make the pivot depend
on how we happen to poll.

## 9. Rhythm (`sa`'s ent#625, ent#626)

Both must be closed before the bench, and `sa` is right about why: `graph_diff`
would report changes that did not happen, and the bench's whole purpose is to be
the witness.

ent#625 — relations retracted ~120 s after an agent restart — is very likely the
defect we fixed this week in two halves: the reconciler's diff state is now keyed
per collection method (#457) and the liveness reference key now names the
**speaker**, a producer *and* its method (#465). Before that, one method's first
cycle after a restart looked like a partial array for all of them, and one
method's silence released the whole producer's reference. **Re-measure ent#625
against a build carrying both** before spending anything on it; if it survives,
it is a different defect and I want the measurement.

ent#626 — SNMP reports arriving at 120 or 180 s for a 120 s tick — is a producer
scheduling question, but it has a Toise consequence worth stating: an entity's
`entity.report.interval` is a **promise**, and the liveness backstop is armed
from it. Declaring 120 and delivering 180 expires entities on the third late
cycle. Either declare the interval you actually achieve, or make the achieved
rhythm match the declaration. Do not ask Toise for a grace factor: that would
make every producer's declared interval mean something slightly different.

## 10. The path primitive (`quart`'s question 1)

`find_path` exists and already refuses to walk through a terminal. What `quart`
asks for beyond that is **per-hop provenance in the answer**: for each hop, its
origin (observed / belief / declared / unknown), its source, and its
`event_time`. That is the right request and it is the same invariant as #395 —
every qualifier the engine holds must be reachable on the answer — applied to a
traversal rather than to an entity.

**It is not in 0.6.4, and 0.6.4 does not need it.** The engine now records, per
edge, the asserting producers and the collection methods (#456, #465), so the
material exists; shaping it into an ordered path answer with a position for each
gap is a read-surface change with its own tests, and this release is already
long. For the bench, `quart` walks it with `get_neighbors` as it proposed, and
the gap positions come from this document rather than from the API.

I will open it as the successor to #442 and #395, with `quart`'s oracle in §11 as
the acceptance criterion, because that oracle is a better specification than
anything I would have written from the engine side.

## 11. What the bench must show (`lab`'s path-check)

`quart`'s station list is the oracle, and I adopt it with three amendments:

- **The junction is dotted, and it is the consumer's deduction**, not a hop. Count
  it as a station only if it is labelled as such.
- **The `address → route` hop is observed, not deduced.** `quart` expected
  longest-prefix matching; it is not needed here. The host emits
  `has_route → next_hop_via`, so the route and its gateway are both entities with
  real edges. Longest-prefix selection — *which* route covers a destination — is
  a separate question, and it is a deduction when a consumer does it.
- **Every station carries `asserted_by` and `collected_by`**, which since #465
  are a set across the collection methods of one producer rather than whichever
  method spoke last. On the bench the LLDP-stated link and an FDB-inferred one
  must be distinguishable *in the answer*, not only in the drawing.

Exit criteria I will hold the workstream to:

1. The station count and order match the bench cabling.
2. The gateway is **one** `network.address` entity, not two that look alike.
3. Cutting the LLDP link stops the path at the switch and the next hop becomes a
   named gap — not a shorter path, not an empty answer.
4. An `as_of` before the bench existed answers "no path at this instant" and says
   so, rather than returning an empty path.
5. A MAC appears in exactly one rendering everywhere it appears.
6. `graph_diff` over a quiet window reports **nothing**. If the rhythm defects of
   §9 are open, this fails and the bench cannot be a witness.

## 12. Display folding (`martin` and `quart`, the 8-level limit)

`quart` is right, and the argument is his: **folding would hide the hop that
breaks.** A path is an ordered sequence, not a depth, so the level limit does not
apply to it — it applies to the neighbourhood view, where folding is the right
answer and already exists.

Fold for display, never in the model, and never on a path. If a path view needs
to be shorter, the thing to drop is not a station but the *detail* on a station.

