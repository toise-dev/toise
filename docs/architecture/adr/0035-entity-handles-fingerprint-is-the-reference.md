# 35. Entity handles: the identity fingerprint is the consumer's reference

- Status: Proposed
- Date: 2026-09-19
- Amends: [ADR 0017](0017-entity-identity-and-stability.md) (which reference
  consumers hold), within [ADR 0029](0029-resilience-and-high-availability.md)
  (nodes never talk to each other)

## Context

On 2026-09-16, during a routine before/after check across the production
cluster, the same host answered under two different entity ids depending on
which replica served the call. A call chained on an id returned by the previous
call failed outright with `no entity found with id ...`. Nothing was broken: the
load balancer had simply sent the second call to the other node.

This is the documented consequence of the read-HA mode we run. ADR 0029 point 3
has N replicas independently re-ingest the same OTLP fan-out and rebuild their
own projection — the "run two Prometheus" pattern, chosen precisely so that
nodes need no coordination. Each replica therefore runs its own change engine,
and `model.NewEntityID()` is `ulid.Make()`: a fresh random ULID per node, per
first sight.

The divergence is not the defect. The defect is that the API hands the consumer
an opaque string, in the position where every API puts a durable reference, and
that string is durable in neither of the two senses a consumer assumes:

- It is **node-local**. Two replicas name the same entity differently, and
  nothing in an answer says which node produced it.
- It is **incarnation-scoped**. Beyond `resurrection_grace`, an entity that
  comes back is minted a new id on purpose, so the same real machine has
  different ids before and after a long outage — on a single node.

ADR 0017 split the two concepts correctly and assigned the roles: the logical
id is "what consumers reference", the identity hash is for internal lookup and
idempotent ingest. Its justification for that assignment was that the logical id
survives an identity change, where a content fingerprint cannot. **That
justification no longer holds.** ADR 0018 removed tolerant matching: the engine
never emits `entity.identity_changed`, and an observation whose identifying
attributes differ is a different entity with a new id. The logical id no longer
survives an identity change, because nothing does. What remains exclusively its
own is distinguishing incarnations past the resurrection window.

Meanwhile the deterministic name already exists and is already load-bearing:

- `Entity.IdentityHash()` is a SHA-256 over the entity type and its
  canonicalized identifying attributes. Two nodes seeing the same entity compute
  the same value, by construction, without exchanging anything.
- It is already on the durable wire as `identity_hash` in `proto/toise/v1`.
- Operator annotations are already keyed by it, precisely because they must
  survive a re-mint — and they already converge across nodes through the shared
  object store (#348), the meeting point ADR 0029 sanctions.

It is exposed by neither read surface. No read surface accepts it as an address.
The gap is paid for in prose instead: the MCP server instructions carry a
paragraph telling every consumer never to carry an id between investigations and
to re-resolve by identity. A contract that must be re-explained to each
consumer, forever, has moved its cost onto them — and Toise's consumers are LLM
agents, which chain calls by nature. The failure this produces is exactly the
one the product exists to prevent: a confident answer about the wrong thing, or
a "not found" read as a disappearance.

## Decision

1. **The identity fingerprint is the consumer-facing handle.** Every entity
   returned by GraphQL and MCP carries it, under a name that says what it is.
   Every read surface that accepts an entity id today also accepts a fingerprint.

2. **The logical id stays, demoted to what it is.** It remains the event log's
   key and is still returned. It is documented as node-local and
   incarnation-scoped — not as "the reference". ADR 0017's dual-concept design
   stands; only the assignment of the consumer-facing role moves.

3. **Read surfaces resolve an entity by its identity in one call.** A consumer
   holding a real-world identity (`host.id`, `container.id`,
   `service.instance.id`) reaches the entity without a search-then-fetch round
   trip. Two round trips are what teaches consumers to cache ids in the first
   place.

4. **Reject node-to-node coordination of id assignment.** ADR 0029 point 1 keeps
   consensus off the data path, and the annotation pattern — periodic
   reconciliation through the object store — cannot serve an allocation that
   happens once per ingested event. Determinism, not communication, is how two
   nodes agree on a name; the fingerprint already provides it at zero cost.

5. **Note, but do not require, the tier-2 convergence.** Under the
   object-store-backed log (ADR 0029 point 4), ids stop diverging as a free side
   effect: the id is minted inside the change engine and embedded in the
   committed event, so a single writer fixes it once and every replay inherits
   it. That removes the node-local half of the problem for tier-2 deployments.
   It does not remove the incarnation half, and it does not reach tier-0/1
   deployments at all, so it is not a substitute for points 1 to 3.

## Consequences

- **Failures become honest.** A fingerprint that no longer resolves means the
  identifying attributes really changed — a genuine event, worth surfacing.
  Today's "not found" can mean that, or a re-mint, or merely a different
  replica, and the consumer cannot tell which.
- **Two names on every entity is a real cost.** It is mitigated by naming them
  for their jobs rather than by their encodings, and by the read surfaces
  accepting either wherever an entity is addressed.
- **The prose warning shrinks to a statement of fact.** The MCP instructions
  stop instructing consumers to work around the contract and start describing
  it.
- **Storage and reads finally agree.** Annotations are already keyed by
  fingerprint; after this, the surface a consumer uses to annotate an entity and
  the surface it uses to read one name that entity the same way.
- **This does not fix #365.** Annotation sync runs inside the log-shipping loop,
  so it is gated on a configured object store. A fan-out deployment with local
  Pebble — our production cluster today — has no meeting point at all, and
  annotations stay node-local regardless of which key they use.
- **Nothing changes in the event log.** The logical id keeps its place in the
  durable record, so no migration, no rewrite, no compatibility break.

See also: ADR 0017 (identity and stability), ADR 0018 (exact identity matching),
ADR 0019 (per-producer reference counting), ADR 0029 (resilience and HA),
ADR 0011 (MCP server design).
