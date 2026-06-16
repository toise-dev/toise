# Connection topology — "who talks to whom"

> **Status (2026-06): Track 1 consumer side shipped in v0.7.0** (toise-dev/toise#184),
> proven end-to-end on a live deployment. Producer side is tracked in
> senhub-io/senhub-agent#457 (outbound dependency edges) and #458 (host interface
> addresses for peer resolution). Track 2 (live per-socket flow) is out of scope —
> it is telemetry, not topology.

Toise models the relational context an LLM needs to reason about a system:
not just *what runs where*, but *what depends on what*. This note records how
"service A depends on B:443" reaches the graph and how the consumer presents it,
within the constraints of the OpenTelemetry entity-events model.

## The problem

A host's socket table is the cheapest, most universal source of dependency
edges: every established outbound TCP connection is "this service depends on
that endpoint". The temptation is to write `A --depends_on--> B's listener`
directly. The OTel entity spec forbids it, for a good reason:

- The emitter observes only the **foreign end** of the connection it opened:
  `{ server.address, server.port, network.transport }`. It **cannot** obtain the
  peer's `host.id` or the peer's `service.listener` identity.
- The data-model MUST-NOT rule: do not emit an entity whose identifying
  attributes you cannot reliably populate. Naming the remote listener by a
  `host.id` you had to guess would mint a wrong, permanent identity.

So the truth the producer can assert and the canonical entity an operator wants
to see are **two different things**. Toise keeps them separate.

## Two tracks

- **Track 1 — durable dependency edges (this note).** Derived from the socket
  table, debounced to "durable", emitted as structural graph edges. This is
  topology: it changes slowly and belongs in the event-sourced graph.
- **Track 2 — live per-socket flow.** Connection counts, bytes, churn. This is
  telemetry; it does not belong in the truth store and is explicitly deferred.

## The model (Track 1)

**Stored fact (producer-asserted, verbatim).** The dependent carries an embedded
`depends_on` relationship to an **observable network endpoint** entity:

```
service.instance  --depends_on-->  network.endpoint{ server.address,
                                                      server.port,
                                                      network.transport }
```

- The edge is **client-side**: the embedded-relationship source *is* the
  carrier, so only the dependent asserts it. Inbound connections produce no edge
  here (they are answered consumer-side by traversal — see below).
- The target is a `network.endpoint` keyed on exactly what the observer can see.
  It is **not** a `service.listener` and carries no `host.id`.
- **The endpoint must be observed as its own `entity.state` record**, in addition
  to the embedded edge. Toise resolves a relation's endpoints by identity and
  **parks then drops** an edge whose target was never observed — it does *not*
  materialise a target entity from the edge alone. A producer therefore emits the
  `network.endpoint` entity and the `depends_on` edge together (same batch is
  fine). (Verified against the engine; see senhub-agent#457.)
- Debounce to durable, aggregate per distinct peer endpoint, no edge attributes
  (those are Track-2 telemetry, deferred in the spec), carry
  `entity.report.interval` for liveness with the usual slack.

**Consumer-side resolution (read-time overlay, never stored).** At read time the
consumer binds the observed endpoint to the canonical entity it denotes. This is
a derived projection (ADR 0020 surcouche; ADR 0018 exact identity preserved) — the
log keeps only "A depends_on <endpoint>". Resolution
(`internal/mcp/resolve.go`) tries, in order:

1. a `service.listener` that binds this `address:port` directly
   (`listen.address`);
2. the **host that owns the address** via the interface model
   (`network.address` `bound_to` `network.interface`, `host` `has_interface`),
   then that host's listener on the port — this covers wildcard (`0.0.0.0`)
   binds, the common case.

When it resolves, `get_neighbors` (and `impact_of` / `find_path`) returns, for a
`depends_on` edge, both the raw endpoint and the resolved entity. An endpoint
that resolves to nothing is an external / off-fleet peer — itself a useful
signal, returned as-is. Path 2 depends on host interface IPs being in the graph
(senhub-agent#458); without them the endpoint stays unresolved.

**"Who connects to this service?"** is the incoming `depends_on` traversal of the
endpoint (and, by resolution, of the listener it denotes).

## What is stored vs derived

| | Stored in the log | Derived at read time |
| --- | --- | --- |
| `A depends_on endpoint{addr:port/tcp}` | yes (verbatim) | — |
| endpoint -> remote listener/host | no | yes (resolution overlay) |
| "who connects to me" | — | yes (incoming traversal) |
| connection counts / bytes | no (Track 2) | — |

## `relationship.type` indirection

`depends_on` is a sanctioned example type in the merged OTel entity spec but has
no normative semantics yet. Treat it as transitional: keep the literal in one
mapping point (`model.RelDependsOn`) so it can be remapped when semconv
standardises a dependency type, without touching every consumer.

## References

- Consumer implementation: toise-dev/toise#184 (shipped v0.7.0).
- Producer: senhub-io/senhub-agent#457 (dependency edges), #458 (host interface
  addresses).
- [ADR 0018](../architecture/adr/0018-exact-identity-matching.md) — exact
  identity; [ADR 0020](../architecture/adr/0020-weighted-multi-source-identity.md)
  — read-time surcouche; [ADR 0022](../architecture/adr/0022-engine-stores-facts-only.md)
  — facts-only engine, embedded relationships.
- [Data-model OTel mapping](../data-model/otel-mapping.md).
