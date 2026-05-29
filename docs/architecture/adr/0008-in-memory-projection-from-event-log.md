# 8. In-memory projection from the event log

- Status: Accepted
- Date: 2026-05-29

## Context

Toise is event-sourced (ADR 0002): the append-only event log, stored in Pebble
(ADR 0007), is the system of record. The queryable graph is a projection derived
from that log. Consumers — the GraphQL API and the MCP server — need fast access
to *current state* and to topology traversals (neighbours, paths) over that graph.

At the phase-1 target scale (10^5–10^6 entities), the live graph fits comfortably
in RAM. Serving current-state and traversal queries directly from the log, or
from a disk-backed graph store, would pay disk and decode costs on every read
for state that is small enough to hold resident.

## Decision

We will maintain the live graph as an **in-memory projection** of the event log,
in package `internal/projection`. The projection is derived from, and fully
reconstructible from, the log; it holds no authority of its own.

- **Data structures.** Entities are held in a `map[EntityID]Entity` plus a
  soft-deleted set; relations in a `map[RelationID]Relation`. Traversal is served
  by outgoing and incoming adjacency maps (`map[EntityID]set[RelationID]`).
  Auxiliary indexes support reconciliation: identity-hash → logical entity ID,
  and entity type → ids (for change-time identity matching).
- **Lifecycle.** The projection is **rebuilt at startup** by replaying the log in
  append order (`store.Scan`), and **updated live** as new qualified events are
  applied. Because it is pure derived state, it can be discarded and replaying
  the log rebuilds it.
- **Concurrency.** A `sync.RWMutex` guards the maps. Reads take the read lock;
  applies take the write lock.
- **Change detection** lives in a separate package, `internal/change`. It diffs
  an incoming observation against the current projection, classifies it per the
  change taxonomy (ADR 0006), assigns and maintains the logical entity ID with
  tolerant identity matching (ADR 0017), appends the resulting qualified event to
  the store, applies it to the projection, and notifies subscribers. Structural
  relation add/remove (ADR 0004/0006) are flagged as high-priority signals.
- **Query routing.** Current-state queries read the projection. The bi-temporal
  `event_time` / `asKnownAt` history queries (ADR 0005) are served from the log,
  not the projection.

## Consequences

- Current-state reads are O(1) for entity/relation lookup and O(neighbours) for
  traversal, and no current-state query touches disk.
- Rebuild cost is bounded by log size. Phase-1 targets: replay 1M events in
  ≤ 30 s; cold start of 100k events in ≤ 10 s; 100k entities resident in ≤ 1 GB
  RSS.
- The projection can lag the log only transiently: applies are synchronous, after
  the append.
- Changing the projection's shape requires no data migration — replaying the log
  rebuilds it under the new shape (ADR 0002).

See also: ADR 0002 (event sourcing), ADR 0005 (history served from the log),
ADR 0006 (change taxonomy), ADR 0007 (the event log it replays),
ADR 0017 (entity identity matching).
