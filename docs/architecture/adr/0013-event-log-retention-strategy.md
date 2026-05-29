# 13. Event log retention strategy

- Status: Accepted
- Date: 2026-05-29

## Context

Toise's event log is append-only and, by default, retained forever (see ADR
0002). The log is the system of record, so the baseline expectation is that
nothing is ever dropped.

That baseline collides with one event category. `entity.unchanged` heartbeats
(see ADR 0006) signal "the entity is alive but nothing changed." An entity
observed every few seconds with no change produces a continuous flood of
identical heartbeats. Retaining each one individually consumes storage without
adding information: the thousandth heartbeat in a run says nothing the first
one did not.

We therefore need a retention strategy that keeps the log faithful for
everything that carries information while bounding the cost of redundant
heartbeats.

## Decision

For phase 1 we adopt the following retention philosophy.

**Kept forever.** Every meaningful event is retained without coalescing or
dropping: `entity.created`, `entity.deleted`, `entity.identity_changed`,
`entity.attribute_updated`, `entity.state_changed`, and all relation events
(`relation.added`, `relation.removed`, `relation.attribute_changed`). These are
never touched by compaction in phase 1.

**Heartbeat coalescing.** A run of consecutive `entity.unchanged` events for the
same entity is collapsed to keep the **first** and the **last** of the run
(optionally a sparse periodic sample), discarding the redundant middle. This
preserves the fact "alive from T1 to T2 with no change" while removing the
thousands of identical records in between.

**Configuration surface.** Two operator knobs:

- `--retention-max-age` — default unlimited; an operator may set, e.g., `30d`
  to cap how far back the log is kept.
- `--retention-compaction-interval` — default `1h`; how often compaction runs.

Phase 1 implements both knobs and the heartbeat-coalescing compaction itself.

**Out of scope for phase 1 (phase 2).** Full snapshot generation and archival of
old events to cold storage are deferred. The store architecture must
accommodate them without rework: the sequence-keyed log (see ADR 0007) and a
snapshot stub interface leave room for both.

Storage growth is estimated in `docs/operations/storage-sizing.md`.

## Consequences

- Heartbeat-dominated workloads stay bounded: the cost of an idle but
  frequently-observed entity is two retained events per run, not thousands.
- The log becomes lossy **only** for redundant heartbeats. This is a deliberate,
  documented exception to the "retain everything" principle of ADR 0002, scoped
  narrowly to the one category (ADR 0006) that carries no information when
  repeated.
- `--retention-max-age` lets operators cap otherwise-unbounded growth when
  forever-retention is not desired.
- Snapshots and cold-storage archival remain future work, but the store is
  designed so they can be added in phase 2 without restructuring the log.
