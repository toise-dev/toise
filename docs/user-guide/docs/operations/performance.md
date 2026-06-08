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
| `find_entities` filtering 200 of 10k hosts | ~1.86 ms/op |
| `get_neighbors` depth 2 | ~330 ns/op |
| Projection rebuild (replay) | ~440 ns/event (~0.44 s extrapolated for 1M events) |

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
