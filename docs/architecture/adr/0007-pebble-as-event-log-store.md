# 7. Pebble as the event log store

- Status: Accepted
- Date: 2026-05-29

## Context

Toise is event-sourced (ADR 0002): the append-only event log is the system of
record and the queryable graph is a projection derived from it. The store that
holds this log therefore has to be durable, embeddable in a single Go binary,
and pure Go (no CGO) to keep cross-compilation and operations simple.

[Pebble](https://github.com/cockroachdb/pebble) — the pure-Go LSM-tree key/value
store used in production by CockroachDB — was validated against the phase 1
targets in a Milestone 0 proof of concept (ADR 0016). With that validation in
hand, we now record the production store design.

## Decision

We will use **Pebble** (`github.com/cockroachdb/pebble`, pinned **v1.1.5**) as
the event log store, in package `internal/store`.

- The log is keyed by a monotonic 8-byte big-endian **sequence** reflecting
  append/ingestion order. The primary record is keyed `evt/` + `seq` and holds
  the proto-serialized event (`proto/toise/v1.Event`). A meta key `meta/seq`
  persists the next sequence to assign.
- **Secondary indexes** map back to the primary sequence (the index values are
  empty) and support the required read patterns:
  - by entity — `ent/<entityID>/<seq>`
  - by change type — `typ/<changeType>/<seq>`
  - by event time — `tim/<eventTimeUnixNano>/<seq>`

  Relation events are indexed under **both** endpoint entity IDs, so a query for
  "events touching entity X" includes the relations attached to it.
- `Append(events ...Event)` writes the primary record and all index entries for
  the batch in a **single Pebble batch committed with `Sync`**. A batch is
  therefore atomic and durable as a unit.
- **Reads** the store exposes: full scan in append order; by entity ID; by
  change type; and by event-time range. The store offers raw ordered access
  only — bi-temporal query semantics are imposed by the higher projection layers
  (ADR 0005), not by the store.
- **Crash recovery** relies on Pebble's WAL; the next sequence is recovered at
  open from `meta/seq`.

## Consequences

- Scans ordered by ingestion are cheap, since the primary key *is* the append
  sequence.
- The secondary indexes add write amplification and storage overhead, quantified
  in `docs/operations/storage-sizing.md`.
- The design is pure Go with no CGO, preserving simple cross-compilation.
- **Out of scope for phase 1.** Snapshots and archival are deliberately deferred:
  phase 1 ships only a stub interface for them. They arrive in phase 2 alongside
  retention and compaction — including heartbeat coalescing — recorded in
  ADR 0013.
- **Durability trade-off.** Committing every batch with `Sync` (one fsync per
  batch) costs latency; the measured impact and the alternative of a periodic
  group-sync are noted in ADR 0016 and will be revisited under load.

See also: ADR 0002 (event sourcing), ADR 0016 (Pebble validation),
ADR 0005 (bi-temporal semantics imposed by the query layers),
ADR 0013 (retention & compaction).
</content>
</invoke>
