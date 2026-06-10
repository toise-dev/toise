# 26. Reconciling Resource-borne entities with entity events

- Status: Proposed (implementation gated on upstream spec feedback and pdata API)
- Date: 2026-06-10

## Context

OTel spec PR [#5147](https://github.com/open-telemetry/opentelemetry-specification/pull/5147)
makes resource detection entity-aware: SDK detectors populate **entities on the
Resource** of ordinary telemetry, not just flat attributes. The same entity can
therefore reach a consumer through two channels:

1. **Entity events** (`entity.state` / `entity.delete` LogRecords) — the rail
   Toise consumes today, carrying lifecycle, timestamps, heartbeat intervals,
   and embedded relationships (ADR 0009, 0019, 0022).
2. **Resource-borne entity refs** attached to metrics/logs/traces — carrying
   identity (and some descriptive attributes) but **no lifecycle and no
   timestamps**.

Producer-side merging is specified; **consumer-side reconciliation is not** —
we asked for guidance on the PR (issue #105 carries the link). pdata v1.59.0
ships `EntityRef` only in its internal generated code: there is **no public
API** to read entity refs off a `pcommon.Resource` yet, so an implementation
cannot land today even if we wanted it to.

## Decision

When both channels speak about the same entity identity (same type + exact
identity attributes, ADR 0018):

1. **Entity events are authoritative for lifecycle and state.** Only the event
   channel creates deletions, arms liveness backstops (ADR 0019), and drives
   change classification. A Resource-borne sighting never deletes, never
   expires, and never resurrects anything.
2. **Resource-borne refs primarily ASSOCIATE.** Their value is identity
   association: tying a telemetry stream to a graph entity (the
   `telemetry_keys` direction, in reverse). Association never mutates the
   graph.
3. **Presence bootstrap is opt-in.** A deployment may choose to let a
   first-seen Resource-borne entity create a graph entity ("exists, details
   pending"). When enabled, the bootstrap arms the standard liveness backstop
   with a configurable TTL — the resource channel has no delete signal, so an
   unbounded bootstrap would mint immortal entities. Off by default until the
   spec answers.
4. **On divergent descriptive attributes, the event channel wins.** A
   Resource-borne attribute may only fill keys the event channel has never
   set; it never overwrites. Authority beats recency — resource attributes are
   detector snapshots, not observations.
5. **Timestamps for bootstrapped presence**: EventTime = the observed
   timestamp of the carrying telemetry batch; RecordedAt = Toise ingest time —
   the same bi-temporal contract as events (ADR 0005).
6. **One internal representation.** Whatever the channel, entities exist
   internally only in the merged entity-events model (embedded relationships,
   ADR 0022); the resource channel is translated at the ingest boundary,
   exactly like the wire contract is today.

## Consequences

- The graph never silently degrades to "whatever the last Resource said":
  lifecycle integrity stays with the channel designed to carry it.
- Implementation is **blocked on two externals**, tracked in #105/#66:
  the spec's consumer-guidance answer on PR #5147, and a public
  `EntityRefs` accessor in pdata. When both land, the ingest boundary gains a
  resource-entity translator alongside `routeRecord`, with tests for the
  both-channels and divergence scenarios.
- If the spec lands a different authority model, this ADR is revised before
  any code ships — the Proposed status is the contract.
