# Event-log storage sizing

## Purpose

This document estimates the **Pebble on-disk storage** consumed by the Toise
event log for a given number of entities and event rate. Use it for capacity
planning: provisioning disk, setting `--retention-max-age`, and sizing the
reference deployment.

> **These are estimates, not measurements.** Every figure below must be
> re-validated against the brief's Linux x86_64 reference profile (8-core,
> 16 GB) with realistic event payloads before it is treated as a commitment.
> The only measured input we have so far comes from the Milestone 0 dev-laptop
> proof of concept (ADR 0016).

## Inputs from the Milestone 0 PoC (ADR 0016)

The M0 proof of concept wrote 10,000 events and measured on-disk size:

- **~176 bytes/event on disk**, for a **256-byte synthetic value**.

Two caveats make 176 B/event an optimistic floor, not a planning figure:

1. **The synthetic value was highly compressible.** It was a repeating `a–z`
   pattern, which Pebble's default Snappy compression flattens almost for free.
   Real entity events (proto-serialized `toise/v1.Event` records with IDs,
   timestamps, attribute maps) are **larger and far less compressible**.
2. **The PoC stored only the primary record.** The production store (ADR 0007)
   also writes **secondary indexes** for every event — by entity
   (`ent/<entityID>/<seq>`), by change type (`typ/<changeType>/<seq>`), and by
   event time (`tim/<eventTimeUnixNano>/<seq>`). Relation events are indexed
   under **both** endpoint entities. These index entries add write amplification
   and on-disk overhead the PoC did not capture.

### Planning figure

To absorb both effects, this document uses a **conservative planning figure of
~400 bytes/event, including secondary indexes**.

> **~400 B/event is a planning estimate, not a measurement.** It is roughly the
> 176 B/event PoC floor inflated for a larger, less-compressible real payload
> plus index overhead. Replace it with a measured value from the reference
> profile as soon as one exists.

## Formula

```
storage ≈ event_rate (events/s) × duration (s) × bytes_per_event
```

with `bytes_per_event ≈ 400` as the planning figure above.

Note that **compaction and heartbeat coalescing (ADR 0013)** reduce this
materially for heartbeat-dominated workloads: repeated "still here, unchanged"
heartbeats are coalesced rather than stored one-for-one, so the effective event
count written to disk is well below the raw emission rate. The formula above
counts *meaningful* events that actually land in the log; treat coalesced
heartbeats as not contributing.

## Worked example (per 1,000 entities)

Assume a fleet of **1,000 entities**. Each entity emits heartbeats that
**coalesce away** (ADR 0013) plus some number of *meaningful* change events that
are actually persisted. We size on the meaningful, persisted events.

Constants:

- bytes/event (planning) = **400 B**
- entities = **1,000**
- seconds/day = 86,400
- days/month = 30

### Case A — 1 meaningful event / entity / minute

Per-entity rate: 1 event / 60 s.
Fleet rate (1,000 entities): `1,000 / 60 ≈ 16.7 events/s`.

- Events/day: `16.7 × 86,400 ≈ 1,440,000` events
  (equivalently `1,000 entities × 1,440 min/day = 1,440,000`).
- **Storage/day:** `1,440,000 × 400 B ≈ 576,000,000 B ≈ 0.58 GB/day`.
- **Storage/month:** `0.58 GB × 30 ≈ 17 GB/month`.

### Case B — 10 meaningful events / entity / minute

Fleet rate (1,000 entities): `10,000 / 60 ≈ 166.7 events/s`.

- Events/day: `1,000 × 14,400 min-events/day = 14,400,000` events.
- **Storage/day:** `14,400,000 × 400 B ≈ 5,760,000,000 B ≈ 5.8 GB/day`.
- **Storage/month:** `5.8 GB × 30 ≈ 173 GB/month`.

All figures rounded and labelled as **estimates**.

## Summary table (per 1,000 entities, ~400 B/event)

| Event rate per entity | Persisted events/day | Storage/day per 1,000 entities | Storage/month per 1,000 entities |
|-----------------------|----------------------|--------------------------------|----------------------------------|
| 1 evt/entity/min      | ~1.44 M              | ~0.58 GB (~576 MB)             | ~17 GB                           |
| 10 evt/entity/min     | ~14.4 M              | ~5.8 GB                        | ~173 GB                          |

> Scale linearly with entity count: 10,000 entities at 1 evt/entity/min ≈
> 5.8 GB/day ≈ 170 GB/month, and so on.

## Closing note

Long-term growth is **bounded**, not unbounded:

- **Retention** — `--retention-max-age` caps how far back the log is kept;
  beyond that horizon, storage reaches a steady state rather than growing
  forever.
- **Heartbeat coalescing (ADR 0013)** — heartbeat-dominated workloads write far
  fewer events than they emit, so real on-disk growth is typically well below
  the raw-rate worst case used above.

**Refine these numbers** by re-running the Milestone 0 PoC (ADR 0016) with
realistic, less-compressible event payloads and the full secondary-index set
(ADR 0007), on the Linux x86_64 reference hardware. Replace the ~400 B/event
planning figure with the measured value once available.

## References

- ADR 0016 — Pebble validation (Milestone 0 proof of concept)
- ADR 0007 — Pebble as the event log store (secondary indexes)
- ADR 0013 — Retention & compaction, including heartbeat coalescing
