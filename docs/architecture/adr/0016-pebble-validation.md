# 16. Pebble validation (Milestone 0 proof of concept)

- Status: Accepted
- Date: 2026-05-29

## Context

The phase 1 brief locks [Pebble](https://github.com/cockroachdb/pebble) as the
event-log store (pure-Go LSM tree, embeddable, used in production by
CockroachDB). Before committing to it deeply (Milestone 2), Milestone 0
requires a throwaway proof of concept to surface any unexpected behavior:
open a database, write 10,000 realistically-sized events, read them back, and
measure throughput and on-disk size.

The PoC lives in `scratch/pebble-poc/` as a separate Go module so it does not
add Pebble to the main module's dependencies before Milestone 2.

## Decision

We confirm Pebble as the phase 1 event-log store. The PoC behaved as expected
with no surprises in the API or operational model.

### Measured results

Reference machine: Apple Silicon (arm64) laptop, local SSD — **dev hardware,
not the brief's Linux x86_64 8-core / 16 GB reference profile**. Pebble v1.1.5,
10,000 events, 256-byte values, batches of 100 committed with `pebble.Sync`
(fsync per batch).

| Metric | Result | Phase 1 target | Verdict |
|--------|--------|----------------|---------|
| Write throughput | ~20,900 evts/s | ≥ 1,000 evts/s | ✅ ~20× |
| Batch commit p50 | 4.8 ms | — | — |
| Batch commit **p99** | **15.1 ms** | ≤ 10 ms (batch of 100) | ⚠️ over on dev HW |
| Full-scan read | ~8.6M evts/s | — | ✅ |
| On-disk size | 1.68 MB (176 B/event) | — | see caveat |

## Consequences

- Pebble is validated for Milestone 2. The API (batches, `Sync`, iterators,
  `Flush`, `Metrics`) is straightforward and pure Go (no CGO).
- **Batch p99 caveat.** The 15.1 ms p99 exceeds the 10 ms target, but this was
  measured on macOS dev hardware where `fsync` is slow and variable, with a
  forced fsync on every 100-event batch. The sync strategy (per-batch `Sync`
  vs. WAL with periodic group-sync) is a Milestone 2 design knob; the target
  must be re-measured on the Linux reference profile before being treated as a
  miss. Tracked for the Milestone 2 benchmark.
- **Storage-sizing caveat.** The 176 B/event figure used a highly compressible
  synthetic value (repeating `a–z`); Pebble's default Snappy compression
  flattered it. `docs/operations/storage-sizing.md` (Milestone 2) must re-run
  with a realistic, less-compressible event payload.
- The PoC module is retained under `scratch/pebble-poc/` for reproducibility;
  it is excluded from the main module build and from `make test`/`make lint`.
- Superseded-by relationship: the production store-design decision is recorded
  separately in ADR 0007 (pebble-as-event-log-store) at Milestone 2; this ADR
  only records the validation step.
