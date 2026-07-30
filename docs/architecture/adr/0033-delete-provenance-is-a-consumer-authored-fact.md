# 33. Delete provenance is a consumer-authored fact

- Status: Accepted
- Date: 2026-07-30
- Relates to: ADR 0022 (engine stores facts only), ADR 0005 (bi-temporal event
  model), ADR 0019 (per-producer reference counting), ADR 0006 (change taxonomy)

## Context

An entity or relation can disappear from the graph three ways: the producer
says so (an explicit `entity.delete`, or an edge dropped by absence on
re-emit), Toise's liveness backstop expires it (no heartbeat within
`entity.report.interval`), or the cascade removes an incident edge because its
endpoint died. On every read surface these were indistinguishable: an expiry
carried an empty `delete_reason`, exactly like a producer delete that gave no
motive, and a `relation.removed` was byte-identical across all three origins.
The flapping incident senhub-agent #454 made the cost concrete — a producer
debugging churn cannot answer "did my agent delete this, or did Toise reap
it?" from the change feed.

Deducing the origin at read time is not possible: the only residual
discriminant (`event_time == recorded_at` on sweep events) is also produced by
any producer delete that omits its timestamp, and relations carry no
discriminant at all.

Writing the origin into `delete_reason` was rejected twice over: that field is
contractually producer-supplied verbatim, and the producer's open enum
legitimately contains `"expired"` — a consumer-written `"expired"` would
recreate the exact ambiguity being fixed, one level down.

The remaining question is one of principle: does a fact **authored by the
consumer** belong in the event log at all, given ADR 0022's "the engine stores
the standard's facts, nothing else"?

## Decision

**Yes — provenance of consumer-authored events is itself a fact, and it is
persisted.** ADR 0022 draws the line between *asserted facts* and *derived
projections*, not between producer-authored and consumer-authored facts. The
engine already forges and commits events of its own: Sweep's `entity.deleted`,
the cascade's `relation.removed`, and `recorded_at` on every event (ADR 0005)
are all consumer-authored, sanctioned facts. What the log lacked was not a new
kind of data but the **attribution of the author** of those events.

Concretely:

- `DeleteSource` (`producer` / `liveness_expiry` / `cascade`) is a distinct
  field on `EntityEvent` and `RelationEvent`, stamped at every write site,
  persisted in the log, exposed on MCP (`delete_source`) and GraphQL
  (`deleteSource`).
- `delete_reason` is untouched in both directions: Toise never writes it, and
  `DeleteSource` is never derived from it.
- The zero value means **unknown** (an event written before schema 1.1) and is
  never re-labeled `producer` — no retroactive provenance lies. Schema version
  bumps 1.0 → 1.1; the protobuf change is additive, so pre-1.1 logs and
  snapshots replay unchanged (snapshots contain no delete events).

The rule this ADR fixes for the future: **an event the consumer authors must
say so.** Any new consumer-originated event (a future retention reap, an
operator-initiated purge) carries an explicit author attribution from day one,
in a field distinct from any producer vocabulary.

## Consequences

- The change feed answers the #454 triage question directly; `graph_diff`'s
  transient/deleted buckets show who authored each disappearance.
- Resurrection (#183) becomes legible: an expired-then-returned entity reads
  `entity.deleted (liveness_expiry)` → `entity.created` in its history, instead
  of an unexplained flap.
- Not a spec gap to raise upstream: `entity.delete.reason` is producer wire
  vocabulary; consumer-side expiry provenance is out of the entity-events
  spec's scope. It belongs as prior art in the liveness experience report
  ADR 0022 already plans (any backend implementing an expiry backstop will
  need to distinguish its synthetic deletes from producer deletes).
- The `delete_source` values are Toise API surface from now on: renaming one is
  a breaking read-API change.
