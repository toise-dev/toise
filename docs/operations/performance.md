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
| GraphQL getNeighbors(depth=2) | ≤ 100 ms p99 | — (MCP, M6) | M6 |
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

When a target is missed, an issue is opened and the architectural cause is
investigated before proceeding (brief, patch 6).
