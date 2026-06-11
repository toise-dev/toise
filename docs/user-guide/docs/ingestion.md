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
| `entity.description` | **map** | no | descriptive, non-identifying attributes |
| `entity.report.interval` | int | no | heartbeat cadence in **seconds**; arms the liveness backstop |
| `LogRecord.Timestamp` | — | yes | becomes `event_time` (falls back to `ObservedTimestamp`, then ingest time) |

Set the OTLP **Resource** `service.instance.id` to identify the producing agent
on every export — it keys per-producer liveness reference counting so multiple
producers can assert the same entity without one's silence deleting it.

### Identity values must be flat scalars

`entity.id` and `entity.description` are genuine OTLP maps, read **structurally**
but **not recursively**: each entry is kept only if its leaf value is one of four
scalar kinds — `string`, `int64`, `double`, `bool`. Pre-flatten any structure
with dotted keys:

```text
{ "server.address": "10.0.0.1", "server.port": 5432 }   # correct — flat scalars
{ "server": { "address": "10.0.0.1", "port": 5432 } }   # wrong — nested map is dropped
```

A dropped nested value is **not silent** — the boundary logs a `Warn` naming the
key, so the loss is observable. Keep identity values as strings.

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
appear within the TTL. OTLP guarantees no inter-batch order, so this keeps
out-of-order delivery from silently losing edges.

## Liveness — explicit delete, interval backstop

Liveness uses two mechanisms:

1. **Explicit `entity.delete` is the primary signal.** When a producer knows an
   entity is gone, it emits `entity.delete` and Toise soft-deletes it (history
   retained). A heartbeat is just a re-emitted `entity.state`.
2. **Interval backstop.** If a producer set `entity.report.interval` and then
   goes silent past that interval, the liveness sweep expires the entity — so a
   producer that crashes without sending a delete doesn't leave stale entities
   forever.

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
