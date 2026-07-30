# 32. Connection-derived edges — endpoint as terminal entity, and proxy vs NAT discontinuity

- Status: Accepted
- Date: 2026-07-06
- Relates to: ADR 0018 (exact identity), ADR 0019 (per-producer reference
  counting), ADR 0020 (weighted multi-source identity / `same_as` belief overlay),
  ADR 0022 (engine stores facts only), ADR 0015 (tracking the OTel entity-events
  spec), and the design note
  [`docs/design/netstat-connection-topology.md`](../../design/netstat-connection-topology.md)

## Context

Toise derives durable `depends_on` edges from host-local connection data (the
netstat design note): a producer that *initiates* a connection asserts
`<local process/host> --depends_on--> <observed remote endpoint>`, keyed on
`{ server.address, server.port, network.transport }`. That note settled the
emission direction (client-side, forced by the embedded-relationship model) and the
target type (an observable network-endpoint, not a remote `host.id`, forced by the
data-model MUST-NOT rule).

A design pass on the reverse-proxy / load-balancer case (an LB fronting backends,
HTTP reverse-proxy on one port and L4 passthrough on another) surfaced three
questions the note did not yet answer normatively, and which are load-bearing enough
to ratify:

1. **What must an edge connect to** for the graph to stay coherent — and is a
   destination with no resolvable owning host an error or information?
2. **Where does the edge sit** (which local and remote entities are its endpoints)?
3. **How is an address/port change across a hop modelled** when a NAT rewrites a
   flow versus when a proxy terminates and re-originates it?

These are identity and provenance decisions. Getting them wrong produces either
dangling edges (a proxy soldered to a phantom) or silent lies (two genuinely
different endpoints merged into one, erasing a real hop).

## Decision

1. **The observed network-endpoint is a first-class terminal entity, persisted
   unconditionally.** Continuity means *an edge always lands on a materialized node*,
   not *every endpoint resolves to a host*. A destination `{server.address,
   server.port, network.transport}` is stored even when no producer ever attaches a
   host behind it. Dropping a destination because "it has no host" discards crucial
   information and is prohibited.

2. **A host-less endpoint is a classifiable node, never an error by default.** It is
   one of three things, told apart by enrichment, not collapsed:
   - **external / out-of-scope** (SaaS, public API, upstream DNS, partner) — the
     egress dependency map; a feature;
   - **internal, not yet supervised** — a discovery / onboarding signal;
   - **key divergence (NAT / VIP)** — the far host *is* monitored but the dialed
     address differs from the advertised one, so the join failed; the only true
     defect.
   Persist first, classify as a queryable dimension (internal CIDR/RFC1918 vs public
   range, reverse DNS / known-service catalogue, observed NAT mapping), never drop.

3. **Edge granularity is `process --depends_on--> network.endpoint`.** The source is
   the socket-owning **process** (`process.pid` + `process.creation.time`), falling
   back to **host** (`host.id`) when the PID is unresolvable — **never the network
   interface** (an interface conducts, it does not depend; the churn rule places the
   edge on the volatile side). The target is the **observable endpoint, never the
   remote host**. The interface / address / route wiring layer is *separate* and is
   joined *through* the endpoint; two hosts connect transitively at the shared
   endpoint, never via a direct host↔host edge.

4. **An address/port change across a hop splits into two cases, modelled
   oppositely:**
   - **Translation only** (kernel rewrites one conntrack flow: DNAT / SNAT / VIP):
     the two addresses are the *same logical endpoint*. Model as **identity aliasing
     via a `same_as` belief** (ADR 0020 overlay), **only when the mapping is
     observable** (an nftables/conntrack rule read by the gateway's agent). Merge.
   - **Termination + re-origination** (a proxy / LB opens a *new* socket to the
     backend): the frontend and backend endpoints are *genuinely different*. Model as
     an **explicit hop** — the proxy is a node bridging two distinct, both-persisted
     endpoints. **Never `same_as`** (merging would erase the hop and lie about the
     topology). The discriminator is: *does an entity re-open a socket* (proxy → hop)
     or *does the kernel rewrite a tuple* (NAT → alias)?

5. **Only the entity that sees both sides may assert the bridge.** The proxy emits the
   frontend↔backend bridge; the NAT gateway's agent (conntrack/nftables) emits the
   `same_as` alias. No third party sees both halves, so none may assert the mapping.
   When nobody sees both sides, the join is **not invented**: the two endpoints stay
   separate (one likely host-less, a decision-2 signal) and are flagged "probable
   unbridged translation." An honest gap beats a fabricated seam. The proxy bridge is
   modelled at **durable routing granularity** (frontend listener → proxy → backend
   endpoints), never per live connection.

5a. **Backend identity must be a resolved concrete endpoint, whatever its source.**
   The invariant is not "socket over config" — it is that the emitted key is a
   **resolved endpoint that matches the address the backend advertises for itself**.
   Two sources are equally valid:
   - **(a) the resolved peer of the proxy's outbound socket** — resolved by
     construction, uniform across providers, the simplest and the fallback / cross-check;
   - **(b) a proxy-specific producer that interprets the config format *and resolves
     it* to that same concrete endpoint at emit time** (name→address, provider
     semantics).
   What is prohibited is emitting an **unresolved config token** (a raw name / URL) as
   identity — that is the key-divergence trap of decision 2/4. A resolving producer is
   preferred where available because it additionally captures **configured-but-idle
   backends** (no live socket yet) and the **routing intent** (which frontend rule maps
   to which backend) that the socket alone cannot show. Resolution MUST be done at
   runtime (names→addresses change) and land in the backend's own address space. A
   proxy's backend configuration format is *not guaranteed* (URL / IP / hostname /
   docker / k8s / consul / file provider), which is why interpretation belongs to a
   proxy-aware producer, never to a generic consumer parsing the raw config.

6. **`same_as` is reserved for endpoint identity aliasing (decision 4), never for HA
   peers.** A VIP shared between two proxy peers is identity-sharing between redundant
   hosts and belongs to a failover model; modelling it with `same_as` would collapse
   two distinct hosts into one canonical node and is prohibited.

7. **The egocentric-outbound rule is scoped, not universal.** "Emit from the
   observer, about its own outbound socket view" governs *socket-derived dependency
   edges only*. Other topology sources invert the perspective (an SNMP/LLDP device
   emits its own port/neighbour view). Network topology is not yet worked out enough
   to freeze a single direction rule.

## Consequences

- **No engine schema change.** Toise stores arbitrary entity and relation types
  (ADR 0022); this ratifies contract/convention and consumer-side overlay behaviour,
  not new engine code. The network-endpoint type, the endpoint→host resolution
  overlay, and the endpoint classification dimension are convention + projection work.
- **New capabilities fall out of decision 2:** an egress dependency map, an
  inventory-gap / shadow-IT detector, and an outbound security surface — all from
  data Toise already ingests.
- **The `same_as` overlay (ADR 0020) gains a second, precisely-bounded use** (NAT
  aliasing) while being explicitly *denied* two tempting misuses (HA peers, proxy
  hops).
- **Downstream, unblocked but not decided here:** the producer contract for the side
  that emits the proxy bridge, the transitional `proxies` relation literal, and
  whether the deterministic (config-derived) track keys backends by name or by
  IP:port. These depend on the concrete proxy's configuration shape and do not block
  ratifying the principles above.
- **SIG prior-art carried** (hold until relationship modelling reopens): representing
  a translating hop (bridge vs alias) and blessing a host-less observed endpoint as a
  first-class classifiable node — both are holes the current spec leaves open.
- **No silent behaviour.** Every branch either lands on a real node, resolves through
  an observable mapping, or is flagged as a gap; nothing is merged or dropped without
  an observable basis. Consistent with ADR 0018 (exact identity, no fuzzy merge) and
  ADR 0022 (only asserted facts in the engine; everything else is a projection).

## Addendum (2026-07-30): host-scoped identity for host-local and link-local endpoints

Agreed with the senhub-agent maintainer (senhub-agent's loopback/Docker
anti-collapse follow-up; replaces its producer-side guard).

The 3-key endpoint identity `{server.address, server.port, network.transport}`
encodes the continuity invariant: two observers dialing the same routable
address land on the same node. That presumes the address's **scope** is at
least as wide as the observation domain. Two families break that presumption
structurally — loopback (`127.0.0.0/8`, `::1`, host scope) and link-local
(`169.254.0.0/16`, `fe80::/10`, link scope) — so today every host's
`127.0.0.1:5432` collapses into one node: a silent lie of the decision-2 kind.

**Decision: for exactly those four ranges, the endpoint identity gains a
fourth identity key** — `{server.address, server.port, network.transport,
host.id}` — where `host.id` is the *observing* host's id, byte-identical to
the `host` entity identity (ADR 0018). Exact-match identity keeps the scoped
and routable forms disjoint by construction. Encoding `host:port` inside
`server.address` was rejected: it puts a non-address value in a
semconv-shaped key and destroys the join with the host entity.

Rules:

1. **The range list is exhaustive and scope-based, not privacy-based.**
   RFC1918 and CGNAT (`100.64.0.0/10`) keep the 3-key form: they are routable
   within an observation domain, so two observers can legitimately denote the
   same endpoint, and scoping them would break real joins. The criterion is
   *address scope narrower than the observation domain*.
2. **IPv6 zone indices are kept verbatim, lowercased, never fabricated.** No
   cross-OS canonicalization (`%eth0` vs `%12`): host-scoping removes the need
   for two producers to join on a link-local address, so the only consistency
   that matters is a producer with itself. All IPv6 identity values follow
   RFC 5952 text form (lowercase, single `::` compression).
3. **Accepted trade-off on link-local:** a link-local address has *link*
   scope, not host scope — two agents on one segment seeing the same `fe80::`
   peer now produce two nodes (honest fragmentation) instead of one false
   merge. Consistent with decision 5: an honest gap beats a fabricated seam.
   A link/segment identity would be the real fix if a concrete case warrants
   it.
4. **Consumer first.** The read-time endpoint resolution must honor the
   scope — an endpoint carrying `host.id` resolves only against that host's
   listeners (or the host itself), never through a fleet-wide bind search —
   and must ship BEFORE producers adopt the 4-key form, or the read overlay
   re-merges exactly what the identity made distinct.
5. **Producer rollout is an identity break** for existing loopback endpoints
   (old deleted, new created): group it with a producer version bump and
   release-note the churn.
