# Storage sizing

Use this to plan disk for the Toise event log and to choose a
[`retention_max_age`](../configuration.md#settings).

!!! warning "These are estimates, not measurements"
    The figures below are conservative planning estimates, not benchmarks.
    Re-validate against your own hardware and realistic event payloads before
    treating any number as a commitment.

## Planning figure

A proof of concept measured **~176 bytes/event on disk** for a highly
compressible synthetic payload — an optimistic floor, because real events are
larger and less compressible, and the production store also writes **secondary
indexes** for every event (by entity, by change type, by event time; relation
events are indexed under both endpoints).

To absorb both effects, this guide uses a conservative planning figure of
**~400 bytes/event, including secondary indexes**.

## Formula

```text
storage ≈ event_rate (events/s) × duration (s) × bytes_per_event
```

with `bytes_per_event ≈ 400`.

Crucially, this counts **meaningful, persisted events** — not raw emissions.
[Heartbeat coalescing](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0013-retention-and-compaction.md)
collapses repeated "still here, unchanged" heartbeats, so heartbeat-dominated
workloads write far fewer events than they emit.

## Worked table (per 1,000 entities, ~400 B/event)

| Meaningful event rate | Persisted events/day | Storage/day | Storage/month |
| --- | --- | --- | --- |
| 1 evt / entity / min | ~1.44 M | ~0.58 GB | ~17 GB |
| 10 evt / entity / min | ~14.4 M | ~5.8 GB | ~173 GB |

Scale linearly with entity count: 10,000 entities at 1 evt/entity/min ≈
5.8 GB/day ≈ 170 GB/month.

## Keeping growth bounded

Long-term growth is **bounded**, not unbounded:

- **Retention** — `retention_max_age` caps how far back the log is kept; beyond
  that horizon, storage reaches a steady state rather than growing forever.
- **Heartbeat coalescing** — heartbeat-dominated workloads write far fewer events
  than they emit, so real on-disk growth is typically well below the raw-rate
  worst case.
- **Compaction** — runs on `retention_compaction_interval` (default `1h`).

For the full derivation see
[`docs/operations/storage-sizing.md`](https://github.com/toise-dev/toise/blob/main/docs/operations/storage-sizing.md)
in the repository.
