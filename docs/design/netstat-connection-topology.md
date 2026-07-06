# Design note: connection topology from `netstat` — dependency edges that enrich LLM analysis

Status: draft for review (2026-06-15)
Scope: how Toise ingests host-local connection data (`netstat -an` / equivalent)
to answer "who is connected to this host's services?" and "what does this
service depend on?" without polluting the stable entity model or re-introducing
churn.

## Goal and constraint

The product goal is to make an LLM's analysis of observability data materially
more relevant. Relations between entities are the highest-leverage piece of that
context: an LLM that knows `service A depends on B:443` reasons about blast
radius, root cause, and impact far better than one that sees isolated hosts.

The constraint is to stay on the OpenTelemetry standards path. The merged
entity-events convention is explicit on two points that shape this design:

1. Entities are **stable** objects — identifying attributes MUST NOT change over
   an entity's lifetime. A live TCP connection has no durable identity.
2. Relationship mutation is **expensive**: there are no edge deletes on the
   wire; an edge is retired by re-emitting the source entity's full state
   without it. The spec's own placement guidance ("put the edge on the most
   volatile endpoint, because every edge change forces a re-emit") is an
   admission that the relationship model is meant for **durable structural
   links**, not for flow.
3. The spec states that *deriving topology from telemetry signals is out of
   scope for the current entity model* — i.e. the consumer/model does not derive
   it. It does **not** forbid a **producer** from asserting a dependency it
   observed; that is the normal producer-asserts-facts model.

Putting per-socket `ESTABLISHED` connections into `entity.relationships` is
therefore the anti-pattern the spec is built to avoid: maximal edge churn,
cascading state re-emissions, and exactly the flapping addressed in #183.

## Decision

Two tracks. Track 1 ships the LLM value now by reusing machinery that already
works; Track 2 is the clean long-term overlay.

### Track 1 (now): producer-derived durable `depends_on` relationships

senhub-agent derives, from its periodic `netstat` snapshot, **only durable
dependency edges**, and asserts them through the **existing** `entity.relationships`
mechanism it already emits. No new Toise storage.

Rules (the emission direction and the edge target are forced by the spec — see
the OTel alignment section below for the rationale):

- **Debounce to durable.** A connection becomes an edge only after it has
  persisted across N consecutive scrapes (e.g. N=3). One-off / short-lived
  sockets never become edges. This is what makes the edge "structural" rather
  than "flow", satisfying the spec's stability intent.
- **Aggregate per distinct peer**, never per socket — bounded volume,
  topological meaning.
- **Client-side emission** (the dependent carries the edge). The host that
  *initiated* the connection (its local port is ephemeral, not in its own LISTEN
  set) emits `<local service.instance> --depends_on--> <observed remote
  endpoint>`. This is forced by the embedded-relationship model: the carrier of
  an embedded relationship *is* its source, so an `A depends_on B` edge can only
  be emitted by A. "Who connects to my services?" is then answered by Toise
  traversing the **incoming** `depends_on` edges of a listener.
- **Edge target = an observable network-endpoint entity**, keyed on what the
  emitter can actually see: `{ server.address, server.port, network.transport }`.
  The emitter does NOT name the remote `service.listener` by its `host.id`-based
  identity, because it cannot reliably obtain the peer's `host.id` (OTel
  data-model MUST-NOT rule). Binding that endpoint to the canonical remote
  listener/host is a consumer-side concern (next bullet).
- **No edge attributes.** The merged spec defers relationship attributes, so the
  edge carries existence only; per-peer `connection.count` / bytes are
  **telemetry** (the backbone's per-entity correlation), never edge fields.
- **Liveness + grace.** The edge carries `entity.report.interval` like every
  other observation; residual churn is absorbed by the #183 tombstone grace
  window. Pick an interval with the same x3 slack the agent uses elsewhere.

Peer resolution (observed endpoint → canonical entity) is a **consumer-side
overlay in Toise**, never persisted into the truth store. Toise maps the
endpoint's `server.address` to a known host (via host interface addresses it has
ingested) and presents the edge as `A depends_on <B's listener on :port>` to the
LLM. The engine stores only the asserted fact (`A depends_on <endpoint>`); the
resolution is a derived projection (the ADR 0020 weighted-multi-source posture:
a surcouche, not truth). When the endpoint resolves to no known host (external,
off-fleet) it stays an unresolved network endpoint — still a useful "depends on
something outside" signal.

### Track 2 (later): live connections as a telemetry overlay

Fine-grained, live connections (not debounced) become a **flow telemetry
overlay**, not graph edges. They join to the stable `service.listener` entity
via the join keys Toise already exposes through the `telemetry_keys` MCP tool
(`server.address:port` = the listener). A dedicated MCP tool ("current
connections for this entity") answers high-resolution questions without ever
mutating the graph. This is the OTel-idiomatic home for ephemeral flow and
mirrors how the Service Graph connector and eBPF/OBI treat "who talks to whom".

## OTel attribute mapping (reuse, do not invent)

The `netstat` ingestion uses canonical semantic conventions only. For an
**outbound** (dependency) connection — the case Track 1 emits — the local end is
the client and the **foreign end is the server**, which is the edge target:

| netstat field (outbound row) | OTel attribute | role |
|---|---|---|
| foreign IP / port (the service A connects to) | `server.address` / `server.port` | **edge target identity** (the observed remote endpoint) |
| local IP / ephemeral port (us) | `client.address` / `client.port` | the dependent — already our own host |
| transport | `network.transport` = `tcp` (always set with a port) | target identity |
| address family | `network.type` = `ipv4` \| `ipv6` | descriptive |
| when client/server roles cannot be inferred | `source.*` / `destination.*` | fallback |
| low-level direct peer (opt-in) | `network.peer.address` / `network.peer.port`, `network.local.address` / `network.local.port` | descriptive |

The edge **target entity** is keyed on `{ server.address, server.port,
network.transport }` — the observable remote endpoint, not the remote
`host.id`-based `service.listener` (see OTel alignment). `relationship.type` =
`depends_on` (sanctioned as an *example* in the merged spec, but with no
normative semantics yet): treat it as **transitional and remappable** — keep the
literal in one place (a Toise mapping / config), never baked into downstream
consumers, so it can follow whatever semconv eventually standardizes.

## What the LLM sees

Nothing new to learn on the query side: the durable edges show up through the
existing graph tools.

- "Who connects to this service?" → `get_neighbors(<service.listener>,
  relation_type=depends_on, direction=incoming)`.
- "What does this service depend on?" → same, `direction=outgoing`.
- Blast radius / impact already compose over these edges via `impact_of` and
  `find_path`.

That is the whole point: the crucial relational context lands in the graph the
LLM already reads, immediately, with no new query surface for Track 1.

## Why this is fast

Track 1 adds **no new Toise storage or ingestion path** — it is a new
`relationship.type` plus a producer-side debounce, riding rails that already
exist (senhub-agent's `entity.relationships` encoder, Toise's relation engine,
#183 grace). The only genuinely new server work is **peer IP → entity
resolution**, which can start as a simple address index over data Toise already
holds.

## OTel alignment (spec-forced decisions)

Verified against the merged entity spec + in-flight work (SIG liaison digest,
2026-06-15). Two of these are not preferences — the spec forces them.

1. **Emission side is forced to the client.** An embedded relationship's source
   *is* the entity carrying it. So `A depends_on B` can only be emitted by A.
   Server-side emission would assert the wrong source. → Track 1 emits from the
   dependent (outbound) side; the "who depends on me" view is the incoming-edge
   traversal, not a separate emission.

2. **Target identity is forced to an observable type, not `host.id`.** The
   data-model states: *if an observer cannot reliably obtain an identifying
   attribute, it MUST NOT emit that entity type; instead emit a different entity
   type keyed on what it can observe.* A sees the peer only as IP:port and cannot
   obtain B's `host.id`. → the edge target is an **observable network-endpoint
   entity** keyed on `{server.address, server.port, network.transport}`, never a
   fabricated/partial `host.id` listener. Naming the canonical listener would
   diverge from a MUST-NOT.

3. **Endpoint → canonical entity is a consumer-side overlay, not engine truth.**
   No spec mechanism exists for late-binding / resolving a relationship target's
   identity, and consumer-derived topology is explicitly *not blessed* ("out of
   scope for the current entity model"). → Toise keeps the asserted fact
   (`A depends_on <endpoint>`) as truth and does the endpoint→host/listener
   binding as a **derived projection** (the ADR 0020 weighted-multi-source
   surcouche), never written back into the log. Consistent with Toise's founding
   principle: only asserted facts live in the engine.

4. **No multi-identity to lean on.** Entity merge is strict exact-match
   (identical type + identifying attributes), with no alias/equivalence
   mechanism in spec or in flight (PR #5067 "identity scope" refines *where* an
   id is unique, it does not add aliases). → We cannot expect the engine to
   converge "peer endpoint" and "host.id entity"; convergence stays an explicit
   overlay. Aligned with ADR 0018.

5. **`depends_on` is a sanctioned example, not a normative type.** Safe to emit,
   but keep it transitional and remappable (single source of the literal) until
   semconv standardizes a dependency type.

### Open questions to carry to the SIG (hold until "relationship modelling" reopens)

These are confirmed gaps in the spec where Toise has prior art to contribute —
do not draft proposals yet:

1. **Naming a relationship target you can only observe, not canonically
   identify** (the cross-resource identity gap, Q2 of the digest). Toise's
   answer — an observable-endpoint entity keyed on discriminants + consumer-side
   resolution — is exactly the hole the spec left open.
2. **A standard `relationship.type` for dependency/communication** (vs the
   containment types runs_on/part_of/scheduled_on). None defined today.
3. **Entity ↔ flow join substrate**: the stable entity graph as the index that
   flow telemetry (`client/server.*`) attaches to, with live→durable derivation
   kept out of the truth store.

## Risks / notes

- A new observable entity type ("network endpoint", keyed on
  `{server.address, server.port, network.transport}`) is required. Toise stores
  arbitrary entity types, so this is a contract/convention addition, not engine
  schema code — but `describe_type` / docs and the resolution overlay must know
  it.
- Direction inference relies on the agent's own LISTEN set (local port in LISTEN
  → inbound, else outbound). Rare ambiguous cases fall back to
  `source.*`/`destination.*`.
- Debounce window vs liveness interval must be chosen together so a durable edge
  does not flap at the boundary; lean on the #183 grace for the rest.
- A dependency to an off-fleet / non-agent host is seen only from the client
  side and stays an unresolved endpoint (no canonical host to bind) — still a
  useful signal.
- Privacy: peer IPs are recorded; confirm this is acceptable for the deployment
  before enabling outbound emission.

## Addendum (2026-07-06): continuity, host-less endpoints, and proxy/translation discontinuity

This addendum refines Track 1 after a design pass on the reverse-proxy / load-balancer
case (an LB fronting backends, HTTP reverse-proxy on one port and L4 passthrough on
another). It does not change the Track 1 rules above; it makes three things precise:
what the edge connects to, what a host-less endpoint means, and how to handle a
proxy that breaks address/port continuity.

### A. The continuity invariant (restated precisely)

The requirement is **not** "every endpoint must resolve to a host". It is: **an edge
must never float — it must always land on a materialized node.** The observed
network-endpoint *is* that node. A destination `{server.address, server.port,
network.transport}` is a **first-class terminal entity**, persisted
unconditionally, even when no producer ever attaches a host behind it. Dropping a
destination because "it has no host" loses crucial information (see B).

So there are two independent axes, and earlier drafts conflated them:

- *Continuity* — the edge points at a real, stored node. **Mandatory.** Satisfied by
  always materializing the endpoint.
- *Host attachment* — a producer on the far side binds that endpoint to its
  host/listener. **Optional, and informative in both presence and absence.**

### B. A host-less endpoint has three meanings — only one is a defect

An endpoint with an inbound `depends_on` but no host bound to it is not, by default,
a graph error. It is one of three things, and they must be told apart, not collapsed:

1. **External / out-of-scope** — a SaaS, public API, upstream DNS, partner service.
   Never monitored by us. The endpoint is the whole truth we will ever have, and it
   is valuable: it is our **egress dependency map**. A feature, not a gap.
2. **Internal, not yet supervised** — an on-fleet host/service we depend on but that
   runs no agent. The host-less endpoint is a **discovery / onboarding signal**:
   "something here is depended on but nobody watches it." Actionable, and a *good*
   find.
3. **Key divergence (NAT / VIP)** — the far host *is* monitored, but the address we
   dialed differs from the address it advertises, so the join failed. **This is the
   only true defect** — a join that should have happened and did not.

Classification is an enrichment, not a precondition: **persist the endpoint first,
classify as a queryable dimension** (internal CIDR / RFC1918 vs public range, reverse
DNS / known-service catalogue, observed NAT mapping), and **never drop it**. This
turns the endpoint layer into three capabilities at once: an egress dependency map, an
inventory-gap / shadow-IT detector, and an outbound security surface. Keeping the bare
endpoint is also the spec-correct move — emit the entity type you can key, not a
guessed host.

### C. Where the edge sits (granularity)

The `depends_on` edge is **neither** host↔host **nor** interface→IP:

```
process (local)  --depends_on-->  network.endpoint { server.address, server.port, transport }
```

- **Target = the observable endpoint, never the remote host** (unkeyable from a
  socket; the MUST-NOT rule). Already the Track 1 rule.
- **Source = the process** (`process.pid` + `process.creation.time`), fallback
  **host** (`host.id`) when the PID is not resolvable. **Not the interface**: an
  interface *conducts* packets, it does not *depend*; the socket owner is the process
  (`/proc`), and the churn rule puts the edge on the volatile side (the process that
  opens/closes connections), not the stable NIC.
- The **interface / address / route** live in a **separate wiring layer** (Toise's
  existing `bound_to` / `next_hop_via` / `connected_to`). A consumer that wants the
  physical path **joins through the endpoint**; the dependency itself does not sit
  there.
- Two hosts therefore connect **transitively at the shared endpoint** — my outbound
  edge lands on the endpoint, the far host's own agent attaches that same endpoint to
  its listener/host — never via a direct host↔host edge. The endpoint is the join
  point (also the reconciliation seam of C-below).

### D. Address / port discontinuity — two cases modelled oppositely

"The address/port changed across the hop" is not one problem. It is two, and merging
them is where the graph would start lying.

**D.1 Translation only (kernel rewrites, one flow).** DNAT / SNAT / VIP keepalived:
the 5-tuple changes but it is the *same* conntrack flow; no entity re-opens a socket.
The two addresses denote the **same logical endpoint** under two guises.
→ Model as **identity aliasing**: a `same_as` belief (VIP-endpoint `same_as`
bind-endpoint), **only when the mapping is observable** (an nftables/conntrack rule
read by the gateway's agent). Merge, because they really are the same thing. This is
the ADR 0020 belief overlay, never engine truth.

**D.2 Termination + re-origination (a proxy / LB).** The proxy accepts one connection
and **opens a new one** to the backend — two distinct sockets in its own process.
The frontend endpoint (what clients hit) and the backend endpoint (what it forwards
to) are **genuinely different endpoints with different roles**.
→ **Never `same_as`.** Merging them would erase the proxy and lie about the topology.
Instead, **insert the proxy as an explicit hop** between two distinct, both-persisted
endpoints. The address/port change is *real and informative* — "there is a proxy here
that translates" is a fact to show, not a seam to hide.

**Discriminator:** does an *entity re-open a socket* (proxy → two sockets in a
process) or does the *kernel rewrite a tuple* (NAT → one flow)? The former is a hop;
the latter is an alias.

**Who may emit the bridge — the hard rule:** only the entity that sees **both** sides
may assert the mapping.
- The **proxy** sees its frontend listener *and* its backend sockets → its own
  producer emits the bridge. No third party sees both halves, so no third party may
  assert it. The backend endpoint identity must be a **resolved concrete endpoint**
  (matching the address the backend advertises), from either the observed outbound
  socket peer **or** a proxy-aware producer that interprets *and resolves* the config
  format at runtime — never a raw, unresolved config token (name/URL), which would
  diverge from the backend's key. A resolving producer additionally surfaces
  configured-but-idle backends and routing intent that the socket alone cannot show.
- The **NAT gateway** reads conntrack/nftables → it emits the `same_as` alias.
- If **nobody** sees both sides → do not invent the join. Leave the two endpoints
  separate (one likely host-less = a B-signal) and flag "probable unbridged
  translation here." An honest gap beats a fabricated seam.

**Granularity of the proxy bridge:** durable **routing** level, never per-connection
(live flow is out of scope, section "Goal and constraint"):

```
listener(frontend :443)  --[owned by]-->  proxy process  --depends_on-->  endpoint(backend :PORT)
```

The proxy process is the common owner of the frontend listener and the outbound
`depends_on` edges, so the bridge already exists through that shared node. To make the
hop legible to `find_path` / `impact_of`, the proxy may additionally emit an explicit
`proxies` edge between the two endpoints (transitional/remappable literal, like
`depends_on`). `find_path(client, backend)` then traverses
`client → frontend → [proxy] → backend → backend-host`, with both endpoints kept
distinct.

**HA note:** a VIP shared between two proxy peers (e.g. keepalived across an
active/passive pair) is **identity-sharing between HA peers**, a *distinct* concern
from proxy translation — it belongs to a failover model, not to a `same_as` of
endpoints. Do not model the HA pair with `same_as` (that would collapse two redundant
hosts into one canonical node).

### E. Scope of the "egocentric outbound" rule

"Emit from the observer, about its own view" is the general principle. "**Outbound
from the socket table**" is the *specific* form for socket-derived dependencies, and
it is a **scoped working rule, not a universal law**: other topology sources invert
the perspective (an SNMP/LLDP device emits its own *port/neighbour* view, which is not
"outbound"). Network topology is not yet worked out enough to freeze a single
direction rule — keep this scoped to socket-derived dependency edges.

### Additional SIG open questions (carry when relationship modelling reopens)

6. **Representing a translating hop** — a standard way to express "a proxy re-originates
   a flow" (distinct endpoints bridged by an entity) vs "the same endpoint under two
   addresses" (aliasing). The spec has neither a bridge relation nor an alias mechanism.
7. **Host-less-endpoint semantics** — whether the model should bless an observed
   endpoint with no resolvable owning entity as a first-class, classifiable node
   (external vs to-be-onboarded vs unresolved-join), which is exactly Toise's prior art.

## References

- `docs/data-model/otel-mapping.md`, `docs/data-model/senhub-agent-contract.md`
- Toise #183 — identity-stable resurrection + tombstone grace window
- OTel entity-events (merged), entity data model, network semantic conventions,
  Service Graph connector, OBI/eBPF (URLs in the SIG digest, 2026-06-15)
