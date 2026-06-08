# Troubleshooting

Common issues when bringing up `toise-server` and feeding it OTLP entity events.

## Entities disappear shortly after appearing

**Cause.** A producer declared an `entity.report.interval` but heartbeats *slower*
than that interval, so the liveness sweep expires the entity between heartbeats.

**Fix.** Re-assert entities comfortably **more often** than the declared
interval. With `toise-probe`, keep `--heartbeat` well below `--interval`
(e.g. `--interval 60s --heartbeat 6s`). See
[Liveness](ingestion.md#liveness-explicit-delete-interval-backstop).

## Nothing shows up at all

- **Wrong port or transport.** Ingestion is OTLP/**gRPC** on `127.0.0.1:4317` by
  default — not HTTP. Check `otlp_listen`.
- **Records aren't entity events.** Toise only ingests `LogRecord`s whose
  `EventName` is `entity.state` or `entity.delete`; everything else is ignored.
  Verify the producer sets `EventName`, not a payload attribute.
- **Unknown entity/relation type.** `entity.type` and `relationship.type` must be
  in Toise's registry; unknown types are rejected. Check the server logs.

## A relationship's endpoint isn't found

Endpoints resolve by **exact identity** against a *live* entity. If a descriptor's
target `entity.id` doesn't match a known entity's current identity, the edge can't
attach.

- With the reconciliation buffer **enabled** (default), an edge whose endpoint
  hasn't arrived yet is **parked** and retried, and dropped with a `Warn` only
  after `relation_buffer_ttl`. Out-of-order delivery is fine.
- With it **disabled** (`relation_buffer_ttl: 0`), a missing endpoint is a
  retriable ingest error.

Make sure the target's identity in the descriptor matches exactly (same keys,
same values, as strings).

## Identity attributes silently missing

`entity.id` / `entity.description` are read **structurally but not recursively**:
only flat scalar leaves (`string`, `int64`, `double`, `bool`) are kept. A nested
map is dropped — and **logged as a `Warn`** naming the key. Pre-flatten with
dotted keys: `{"server.address": "10.0.0.1"}`, never
`{"server": {"address": "10.0.0.1"}}`. See
[Identity values must be flat scalars](ingestion.md#identity-values-must-be-flat-scalars).

## Two distinct things merged into one

The flip side of exact identity: if two entities accidentally share an
identifying key, they collapse into one. Treat identity keys as a **contract**
with your producers — pick attributes stable for the entity's lifetime, and keep
volatile values (current IP, port) as descriptive attributes. See
[Two identities, not one](data-model.md#two-identities-not-one).

## GraphQL queries time out or are rejected

The server is hardened with guardrails:

- **Complexity cap** (default `1000`) rejects overly broad selections.
- **10s per-request timeout** returns HTTP `503` with an actionable message.

Request only the fields you need, keep `first:` modest and page with cursors, and
split large traversals. See
[Guardrails and limits](querying/graphql.md#guardrails-and-limits).

## Browser can't open a WebSocket subscription

Cross-origin WebSocket upgrades are refused unless the `Origin` is allow-listed;
non-browser clients (no `Origin`) are allowed. Serve your client same-origin, or
allow-list its origin.

## Can't reach the server from another machine

By design: all listeners bind to **loopback** (`127.0.0.1`) by default, and
phase 1 has **no authentication**. Binding to `0.0.0.0` is an explicit choice and
should only be done on a trusted, network-isolated segment. See
[Security (phase 1)](configuration.md#security-phase-1).

## Still stuck?

Open an issue at
[github.com/toise-dev/toise/issues](https://github.com/toise-dev/toise/issues)
with the server logs and your producer's export shape.
