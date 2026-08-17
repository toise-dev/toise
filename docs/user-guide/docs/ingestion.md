# Ingesting data (OTLP)

Toise is fed entirely by **OpenTelemetry entity events** over OTLP. **It runs no
collectors and polls no devices** — emitting entity events from hosts, network
gear, or cloud APIs is the *producer's* job. Any OpenTelemetry producer can feed
Toise: [senhub-agent](https://agent.senhub.io), an OpenTelemetry Collector, or
your own instrumentation.

This page describes the wire contract a producer must satisfy. The full,
authoritative mapping is in
[`docs/data-model/otel-mapping.md`](https://github.com/toise-dev/toise/blob/main/docs/data-model/otel-mapping.md).

## Transport

OTLP entity events are carried as OTLP **`LogRecord`s** over the logs service.

| | |
| --- | --- |
| Protocol | OTLP/**gRPC** (logs service) |
| Default address | `127.0.0.1:4317` (set via `otlp_listen`) |
| Compression | uncompressed **and gzip** accepted — gzip is the OTel SDK default, so it works out of the box |

The ingest boundary is the single place where the OTel wire shape is translated
into Toise's internal event model; everything downstream is Toise's own model.

## Entity events

Toise classifies each `LogRecord` by its **`EventName`**: a record whose
EventName is `entity.state` or `entity.delete` is an entity event; any other
record is ignored. This follows the **merged** OpenTelemetry entity-events spec
(`specification/entities/entity-events.md`, merged 2026-06-04).

| Carrier | Type | Required | Meaning |
| --- | --- | --- | --- |
| `EventName` (LogRecord) | string | yes | `entity.state` (upsert) or `entity.delete` (soft delete) |
| `entity.type` | string | yes | the entity type — must be in Toise's type registry |
| `entity.id` | **map** | yes | identifying attributes (the entity's identity), `map<string,string>` |
| `entity.description` | **map** (`AnyValue`) | no | descriptive, non-identifying attributes — full AnyValue (scalars, arrays, nested maps) |
| `entity.report.interval` | int | no | heartbeat cadence in **seconds**; arms the liveness backstop. `0` or absent = **no cadence** (removed only by an explicit `entity.delete`) |
| `LogRecord.Timestamp` | — | yes | becomes `event_time` (falls back to `ObservedTimestamp`, then ingest time) |

Set the OTLP **Resource** `service.instance.id` to identify the producing agent
on every export — it keys per-producer liveness reference counting so multiple
producers can assert the same entity without one's silence deleting it.

### Identity is scalar; description is full AnyValue

`entity.id` and `entity.description` are genuine OTLP maps, but they are read by
**different rules**:

- **`entity.description` carries the full `AnyValue`** — scalars (`string`,
  `int64`, `double`, `bool`), **arrays**, and **nested maps**, recursively. Composite
  values are ingested faithfully and render on read as compact JSON tagged `array` /
  `kvlist` (since 0.9.0). Only unsupported leaves (e.g. `bytes`) are dropped, and
  never silently — the boundary logs a `Warn` naming the key.
- **`entity.id` (identity) must be flat scalars.** Exact-match identity is over
  scalar strings (ADR 0018), so a nested value in an identity map is **dropped and
  surfaced** (a `Warn` naming the key), never hashed. Pre-flatten identity structure
  with dotted keys:

```text
{ "server.address": "10.0.0.1", "server.port": "5432" }   # correct — flat scalars
{ "server": { "address": "10.0.0.1", "port": "5432" } }   # wrong — nested map is dropped from identity
```

## Relationships are embedded

Relationships are **not** separate records. They ride **embedded** on an
`entity.state` event as an `entity.relationships` array on the source entity; each
descriptor names the **target** (the source is the emitting entity):

| `entity.relationships[]` field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `relationship.type` | string | yes | the relation type — must be in Toise's registry |
| `entity.type` | string | yes | the **target** endpoint entity type |
| `entity.id` | **map** | yes | the **target** endpoint identity |

The boundary translates each descriptor into a first-class relation event
(`from` = the emitting entity, `to` = the target) and **reconciles per source**:
a descriptor the source stops listing is **removed by absence** — there is no
explicit relation-delete on the wire.

**No edge attributes.** A descriptor carries only `relationship.type` + target.
Anything that wants to describe *how* two things relate becomes an **entity** (a
port is a `network.interface`, a route is a `network.route`), never an attribute
on the edge.

### Ordering is not required

Endpoints resolve by **exact identity** against a live entity. Producers *should*
emit endpoint `entity.state` events before the entity that embeds an edge to
them, but ordering is **not required**: with the reconciliation buffer enabled
(`relation_buffer_ttl`, on by default), an edge whose endpoint hasn't arrived yet
is **parked** and retried, and dropped with a `Warn` only if its endpoints never
appear within the hold — the greater of that TTL and the source's own re-emit
interval, so a parked edge always gets at least one full cycle. OTLP guarantees
no inter-batch order, so this keeps out-of-order delivery from silently losing
edges.

An edge whose endpoint will *never* arrive — a `same_as` naming an identity that
no longer exists, for instance — costs one `Warn` per edge once the hold expires
and nothing else: no event, no ingest error, nothing returned to the producer.

## Liveness — explicit delete, interval backstop

Liveness uses two mechanisms:

1. **Explicit `entity.delete` is the primary signal.** When a producer knows an
   entity is gone, it emits `entity.delete` and Toise soft-deletes it (history
   retained). A heartbeat is just a re-emitted `entity.state`. A delete may carry
   an optional **`entity.delete.reason`** — an open enum, never validated against a
   closed set — captured, persisted, and surfaced on MCP `recent_changes` /
   `graph_diff` and GraphQL `ChangeEvent.deleteReason`.

    Recommended values: `terminated`, `evicted`, `scaled_down`, `user_requested`,
    `expired`, `parent_removed` — and **`unmonitored`**, which is the one that
    matters. The first six say the *resource* ended; `unmonitored` says the
    *observation* did, and the resource may well still be running. Never use a
    lifecycle value when a probe was simply removed or a target left the scope: an
    entity that disappears because someone edited a configuration must not read as
    "the thing is gone". See the
    [contract](https://github.com/toise-dev/toise/blob/main/docs/data-model/senhub-agent-contract.md)
    for the full table and how each value pairs with `delete_source`.
2. **Interval backstop.** If a producer set `entity.report.interval` (> 0) and then
   goes silent past that interval, the liveness sweep expires the entity — so a
   producer that crashes without sending a delete doesn't leave stale entities
   forever. An entity with **`entity.report.interval == 0` (or absent) has no
   cadence**: the sweep never expires it, so it is removed only by an explicit
   `entity.delete`. Use `0` for entities whose absence is only ever asserted, not
   inferred from silence.

Whichever mechanism removed something, the change feed says **who authored the
disappearance**: every `entity.deleted` / `relation.removed` carries a
`delete_source` (`producer` — explicit delete or removal-by-absence;
`liveness_expiry` — the interval backstop; `cascade` — an endpoint died and took
the edge), exposed on MCP `recent_changes` / `entity_history` / `graph_diff` and
GraphQL `ChangeEvent.deleteSource`. It is consumer-authored provenance, distinct
from the producer's `delete.reason`; events recorded before 0.10.0 read back with
an unknown source.

**Sizing the interval:** apply the ×3 slack to the **effective re-emission
cadence** — the longest gap between two `entity.state` events the consumer can
see (for an agent that suppresses unchanged state, the suppression cadence) —
not the internal heartbeat tick. A tick of 60s with suppression at 120s sized as
`3 × 60 = 180s` has a real slack of ×1.5 and expires mechanically on the first
missed re-emission; size it `3 × 120 = 360s`.

!!! warning "Heartbeat faster than your interval"
    A producer must re-assert its entities **more often** than the
    `entity.report.interval` it declares, or the sweeper will expire them between
    heartbeats. Pick a heartbeat comfortably below the declared interval.

## Try it without writing a producer

The bundled `toise-probe` is a real OTLP/gRPC producer — use it to exercise the
whole path end to end:

```bash
./bin/toise-server --data-dir ./live-data &
./bin/toise-probe --hosts 60 --interval 60s --heartbeat 6s
```

See [Installation](installation.md#a-live-run-over-the-real-otlp-path) for more
producer scenarios, and the
[data model](data-model.md) for what entities and relations Toise tracks.


## The toise-emit SDK and conformance kit

Hand-rolling the wire contract is how producers drift. Two tools replace it:

- **`github.com/toise-dev/toise/pkg/emit`** — a small Go SDK: declare entities
  (type, identity map, attributes, heartbeat interval, embedded relationships)
  and call `State` / `Delete`; the SDK builds the spec-correct OTLP payload
  (deterministically — sorted keys, stable bytes) and exports it over gRPC
  with your auth headers and tenant. When Toise accepts the export but rejects
  some records (OTLP partial success), `State`/`Delete` return a typed
  `emit.PartialError` carrying the rejected count and the server's first
  rejection reason — do not retry it; fix the producer.
- **`pkg/emit/conformance`** — contract validation without a running Toise:
  `conformance.Check(logs)` returns every violation (missing identity, empty
  attribute keys, mis-typed interval, incomplete relationship descriptor,
  non-scalar values) with its location. Run it in your producer's CI; output
  that passes is never rejected per-record by Toise **for shape reasons**.
  Type-registry membership is enforced separately: under the default strict
  vocabulary an `entity.type` outside the registry is still rejected per
  record, unless the deployment sets `accept_unknown_types`. `Check` also
  returns *advisory* problems (`Problem.Advisory`, not rejections) for
  misconfigurations such as a missing `service.instance.id` resource
  attribute, which collapses multi-producer liveness reference counting.

The checked-in fixture (`pkg/emit/testdata/fixture_v1.bin`) is the published
contract v1: the SDK reproduces it byte for byte and Toise's own ingest tests
accept it with zero rejections — one artifact pins both sides.

The SDK is its own Go module
([ADR 0027](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0027-sdk-module-and-versioning.md)),
versioned independently of the server and dependency-light: importing it pulls
in the OTel pdata types and gRPC, none of the server's storage or query stack.
It is installable at a tagged version once the first SDK tag
(`pkg/emit/v0.1.0`) is cut — Go resolves the nested module path from the tag
automatically:

```bash
go get github.com/toise-dev/toise/pkg/emit@v0.1.0
```

Until then, `go get github.com/toise-dev/toise/pkg/emit@main` resolves a
pseudo-version of the latest main.
