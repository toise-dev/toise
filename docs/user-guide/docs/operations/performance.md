# Performance

Toise is a single Go binary with an embedded event log
([Pebble](https://github.com/cockroachdb/pebble)) and an in-memory projection.
This page summarises what to expect and how to measure it for yourself.

!!! note "Numbers are directional"
    The figures below were measured on Apple Silicon (arm64) developer hardware,
    where `fsync` is slower and more variable than the Linux x86_64 reference
    profile (8 cores / 16 GB). Treat them as directional and re-measure on your
    own hardware. Run the suite with `make bench`.

## The shape of it

- **Reads are O(1)/O(n) over an in-memory snapshot**, independent of total graph
  size. GraphQL, MCP, and the debug UI all sit on this one projection, so they
  share the same latency picture. With ~1.3k entities and ~1.3k relations in the
  projection, a full host detail page (identity + attributes + neighbours +
  history) renders in **under 1 ms**.
- **Ingestion is bounded by the durable append.** The OTLP boundary batches a
  whole export's records into **one Sync'd batch append**, rather than one fsync
  per record. That is the single biggest ingestion lever.

## Measured highlights

| What | Measurement |
| --- | --- |
| OTLP LogRecord convert + dispatch | ~90 ns/op (excludes gRPC transport) |
| Event-log append (batch of 100, Sync) | ~4.1 ms per batch |
| `find_entities` filtering 200 hosts out of 10k **entities** (~270 real hosts at the measured 37-entities-per-host ratio) | ~1.86 ms/op |
| `get_neighbors` depth 2 | ~330 ns/op |
| Projection rebuild (replay) | ~440 ns/event (~0.44 s extrapolated for 1M events) |

### Measured at fleet scale (#351)

A production-shaped estate — 37 entities per host, the ratio the real fleet
exhibits — loaded to 10,000 hosts (311,750 entities, 300,000 relations) on
arm64 developer hardware:

| What | Measurement |
| --- | --- |
| Resident memory (settled, after GC) | ~150 MB (~0.4 KB per entity over a ~21 MB baseline) |
| Sustained ingest during load | ~39,000 entities/s over chunked exports |
| `find_entities` 200 of 10,000 real hosts | ~25 ms |
| `get_neighbors` depth 2 | ~1 ms — indifferent to graph size |
| `recent_changes`, 1h window of 380k heartbeats | **~20 ms** (the classified time index skips what a filter excludes) |
| `graph_diff`, same window | **~17 ms** |
| A window of 611k *real* changes | seconds — the answer itself is large |

The last row is the intended shape: **a question costs its answer, never the
noise it had to discard**. Do not size memory from a reading taken during or
right after a bulk load — the GC high-water mark can read several times the
settled figure.

### Batched append in practice

Ingesting a generated fabric over real OTLP/gRPC with `toise-probe`:

| Fabric | Entities | Relations | Per-record fsync | Batched append (1 fsync/export) |
| --- | --- | --- | --- | --- |
| 120 hosts | 633 | 639 | ~5.4 s | **~0.40 s** (~13×) |
| 250 hosts | 1319 | 1333 | ~8 s | **~0.03 s** (re-assert) |

On the durable-append path alone, batching 100 entities into one Sync'd append
is roughly **84×** faster than 100 individual Sync'd appends.

## Measure it yourself

```bash
make bench                                  # the microbenchmark suite
./bin/toise-server --data-dir ./live-data &
./bin/toise-probe --hosts 250 --interval 60s --heartbeat 6s
```

Then watch ingest latency and query a host detail via
[GraphQL](../querying/graphql.md) or [MCP](../querying/mcp.md).

## Keeping it fast

- **Batch your exports** on the producer side — one larger export beats many tiny
  ones, because durability cost is per-append, not per-record.
- **Page your queries** — keep GraphQL `first:` modest and use cursors; stay
  under the complexity and timeout [guardrails](../querying/graphql.md#guardrails-and-limits).
- **Set retention** — cap log growth with `retention_max_age`; see
  [Storage sizing](storage-sizing.md).
