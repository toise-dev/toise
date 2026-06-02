# Performance

Phase-1 performance targets and the latest measured numbers. Targets are from
the phase-1 brief. Each milestone checkpoint records a benchmark run here.

Run the suite with `make bench`.

## Reference profile vs. measured hardware

The brief's reference profile is a Linux x86_64 machine, 8 cores / 16 GB RAM.
**The numbers below were measured on Apple Silicon (arm64) dev hardware**, where
`fsync` is slower and more variable. Targets must be re-validated on the
reference profile before being treated as pass/fail; they are recorded here as
directional signals.

## Targets and current status

| Metric | Target | Latest | Milestone |
|--------|--------|--------|-----------|
| Ingestion rate (OTLP/gRPC) | ≥ 1,000 evts/s | conv ~90 ns/op | M4 ✅* |
| Event log append (batch of 100, Sync) | ≤ 10 ms p99 | ~4.1 ms/batch (mean) | M2 ✅ |
| Pebble write throughput (PoC) | — | ~20,900 evts/s | M0 |
| Pebble full-scan read (PoC) | — | ~8.6M evts/s | M0 |
| Identity hash | — | ~284 ns/op | M1 |
| Entity → proto | — | ~486 ns/op | M2 (model) |
| Projection rebuild (1M events) | ≤ 30 s | ~0.44 s (extrapolated) | M3 ✅ |
| GraphQL entity(id) | ≤ 10 ms p99 | functional, bench TBD | M5 |
| getNeighbors(depth=2) (GraphQL/MCP) | ≤ 100 ms p99 | MCP ~330 ns/op | M6 ✅ |
| MCP find_entities (10k hosts) | ≤ 10 ms p99 | ~1.86 ms/op | M6 ✅ |
| GraphQL entityHistory(1h) | ≤ 200 ms p99 | functional, bench TBD | M5 |
| Memory footprint (100k entities) | ≤ 1 GB RSS | — | M3 |
| Cold start to ready (100k events) | ≤ 10 s | — | M3 |

## Notes by milestone

- **M0** (ADR 0016): Pebble PoC — write ~20.9k evts/s, scan ~8.6M evts/s,
  ~176 B/event (compressible synthetic payload). See `storage-sizing.md`.
- **M1**: `model.IdentityHash` ~284 ns/op (13 allocs); `Entity.ToProto`
  ~486 ns/op.
- **M2**: `BenchmarkAppendBatch100` ~4.1 ms to append+index+Sync a batch of 100
  events (~109 KB, ~1,900 allocs). Comfortably under the 10 ms target on dev
  hardware; re-measure p99 under sustained load on the reference profile.

- **M3**: `BenchmarkReplay` rebuilds the projection from 50k entity-creation
  events in ~22 ms (~440 ns/event), i.e. ~0.44 s extrapolated for 1M events —
  well under the 30 s target on dev hardware. Re-measure with a realistic event
  mix (relations, updates) and from the on-disk log.

- **M4**: `BenchmarkRouteEntityState` converts+dispatches a LogRecord in
  ~90 ns/op (2 allocs), **excluding gRPC transport**. End-to-end OTLP ingestion
  is bounded by the store's Sync'd append (~4.1 ms per 100-event batch, M2), so
  the ≥ 1,000 evts/s target is comfortably met with batched appends. (*) A full
  end-to-end gRPC throughput benchmark is still to be run on the reference
  profile.

- **M5**: GraphQL `entity`/`entities`/`relations` read the in-memory projection
  (O(1)/O(n) over a snapshot); `entityHistory`/`recentChanges` read the log.
  All are functionally validated; a formal p99 latency benchmark on the
  reference profile is still to be run.

- **M6**: `BenchmarkFindEntities` filters and renders 200 of 10k host entities
  in ~1.86 ms/op (~806 allocs) — the MCP layer adds only struct conversion over
  the projection snapshot. `BenchmarkGetNeighbors` traverses depth 2 and renders
  the result in ~330 ns/op (8 allocs). Both share the same read model as GraphQL,
  so the latency picture matches M5; re-measure p99 on the reference profile.

- **M7**: the debug UI is server-rendered HTML over the *same* read model as
  GraphQL/MCP (projection + log) plus `html/template` rendering. It is a
  read-only debug surface, not a hot path, so it carries no separate target or
  benchmark; its read cost is bounded by the M5/M6 numbers above and the entity
  list is capped at 500 rows to keep page rendering bounded on large graphs.

## Scale characterization — real OTLP, multi-machine fabric (`toise-probe`)

Measured end-to-end over OTLP/gRPC with `toise-probe --hosts N` (a generated
fabric: hosts + interfaces/addresses/routes/listeners, ~1 db per 8 hosts, a ring
of network switches, all monitored by one agent), on Apple Silicon dev hardware:

| Fabric | Entities | Relations | Initial ingest (1 fsync/record) | After **batched append** (1 fsync/export) |
|--------|----------|-----------|---------------------------------|-------------------------------------------|
| 120 hosts | 633 | 639 | ~5.4 s (≈ 235 records/s) | **~0.40 s** (~13× faster) |
| 250 hosts | 1319 | 1333 | ~8 s (≈ 330 records/s) | **~0.03 s** (re-assert) |

Two clear signals:

- **The read model scales effortlessly.** With 1.3k entities / 1.3k relations in
  the projection, a host detail page (identity + attributes + neighbours +
  history) renders in **< 1 ms** — reads are O(1)/O(n) over the in-memory snapshot
  regardless of graph size. GraphQL, MCP, and the debug UI all sit on this.
  Microbenchmark (regression guard, `internal/change`, real Sync'ing store):
  `BenchmarkIngestBatch100` ingests 100 entities in **~4.5 ms** (one Sync'd batch
  append) versus `BenchmarkIngestPerEvent100` at **~374 ms** (100 Sync'd appends) —
  ~**84×** on the durable-append path alone.

- **Batched append lifts the ingestion ceiling.** The OTLP boundary used to commit
  **one durable (Sync'd) Pebble append per record**, so a large export (≈ 1.3k
  records) was fsync-limited to ~250–330 records/s. The engine's `Batch` now
  ingests a whole export's records under one lock and flushes their qualified
  events in **one Sync'd batch append** (`Engine.Batch` → `store.Append(...)`),
  cutting a 633-record export from ~5.4 s to **~0.40 s**. Durability is preserved:
  events are applied to the projection as they are classified and made durable in
  one batch at the end of the export; a crash before the flush is covered by OTLP
  at-least-once retry and idempotent classification (verified: a data dir written
  this way reopens and rebuilds the projection intact).

When a target is missed, an issue is opened and the architectural cause is
investigated before proceeding (brief, patch 6).
