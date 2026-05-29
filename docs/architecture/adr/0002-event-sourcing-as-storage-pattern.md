# 2. Event sourcing as the storage pattern

- Status: Accepted
- Date: 2026-05-29

## Context

Toise maintains a live graph of an organization's infrastructure: devices,
hosts, services, network links, routing state, and the relationships between
them. This graph is assembled by reconciling data from many heterogeneous
sources (SNMP, gNMI, vSphere, Active Directory, host agents, cloud APIs), each
with its own cadence, reliability, and notion of truth.

Several requirements shape how we store this graph:

- **Time travel.** Operators need to ask "what did the topology look like at
  time *T*?" — for incident investigation, change analysis, and comparison.
- **Audit.** Every change to the graph should be attributable: which source
  reported it, when, and what it changed.
- **Replay.** As the projection logic and the data model evolve, we must be
  able to rebuild the graph from history without re-polling every source.
- **Multiple projections.** The same underlying facts may need to be served as
  different views (a graph for queries, denormalized indexes, exports to other
  systems).

A store that only keeps current state (the typical CRUD-over-a-database
approach) cannot satisfy time travel, audit, or replay without bolting on a
separate, lossy change-log after the fact.

## Decision

We will use **event sourcing** as the reference storage pattern.

- The system of record is an **append-only event log**. Receivers translate
  observations from their source into immutable, timestamped events
  (entity observed, attribute changed, relationship established or removed,
  entity no longer seen, …).
- The queryable **graph is a projection** built by folding the event log
  through projection logic. It is derived state, not the source of truth, and
  can be discarded and rebuilt at any time.
- Projections are rebuildable by replaying the log from the beginning (or from
  a snapshot).

## Consequences

### Positive

- **Time travel, audit, and replay fall out of the model** rather than being
  retrofitted. Querying historical state means projecting the log up to a
  chosen offset.
- **We can change the projection engine without losing history.** If we replace
  or restructure the graph projection — a different graph store, a new
  denormalization, a corrected reconciliation rule — we replay the existing log
  to rebuild it. The history is independent of how we currently choose to view
  it.
- **Multiple projections** of the same facts are natural: each is just another
  fold over the same log.
- Reconciliation conflicts between sources become explicit, ordered events
  rather than hidden last-writer-wins overwrites.

### Negative / costs

- Event sourcing carries well-known complexity: schema/versioning of events,
  snapshotting to bound replay time, and eventual consistency between the log
  and its projections. These must be designed deliberately.
- Queries that need current state always go through a projection; the raw log
  is not directly queryable in a useful way.

### Inspirations

This decision is informed by prior art in graph and infrastructure modeling,
notably Netflix's graph abstraction work and the conceptual approach of
[Infrahub](https://www.opsmill.com/infrahub/) to versioned, source-of-truth
infrastructure data. We adapt the ideas rather than adopt any implementation.
