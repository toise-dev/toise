# OpenTelemetry entity-events mapping

This document records the mapping between the
[OpenTelemetry Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
/ entity-events specification and the Toise schema. It is maintained per
[ADR 0015](../architecture/adr/0015-tracking-otel-entity-events-spec.md) and kept
up to date as either side changes.

## How OTel entity events reach Toise

OTel entity events are carried as **OTLP `LogRecord`s** annotated with the entity
semantic conventions. Toise ingests them at the **OTLP boundary** in
**Milestone 4** (`internal/ingest`): the boundary is the single place where the
OTel wire shape is translated into the internal Toise event model.

## Wire shape: the exact LogRecord attributes Toise reads

The ingest boundary classifies each `LogRecord` by **which lifecycle key is
present**: `otel.entity.event.type` marks an **entity event** (standard OTel),
`entity.relation.event.type` marks a **relation event** (the extension). A record
with neither is ignored (treated as an ordinary log). The scope is **not**
inspected — Toise does not require the experimental `otel.entity.entity_event=true`
instrumentation-scope flag; a producer may set it for spec fidelity, but Toise
neither reads nor requires it.

### Entity events (standard OTel convention)

| LogRecord attribute        | Type    | Required | Meaning                                                |
| -------------------------- | ------- | -------- | ------------------------------------------------------ |
| `otel.entity.event.type`   | string  | yes      | `entity_state` (upsert) or `entity_delete` (soft delete) |
| `otel.entity.type`         | string  | yes      | the entity type — **must be in Toise's type registry** |
| `otel.entity.id`           | **map** | yes      | identifying attributes (the entity's identity)         |
| `otel.entity.attributes`   | **map** | no       | descriptive, non-identifying attributes                |
| `otel.entity.interval`     | int     | no       | heartbeat cadence in **milliseconds**; arms the liveness backstop (see *Entity liveness*) |
| `LogRecord.Timestamp`      | —       | yes      | becomes `event_time` (falls back to `ObservedTimestamp`, then ingest time) |

Notes:

- `otel.entity.id` and `otel.entity.attributes` are genuine OTLP **maps**
  (`AnyValue` kvlist), parsed structurally — see *AnyValue restriction* below.
- A `schema_url` is part of the model but is **not currently read** at the OTLP
  boundary; entities ingested via OTLP carry an empty schema URL for now.

### Relation events — the `entity.relation.*` extension (non-standard)

The OTel Entity Data Model does **not yet model entity-to-entity relationships**;
[OTEP 0256](https://github.com/open-telemetry/oteps/blob/main/text/entities/0256-entities-data-model.md)
lists relationships as explicit *Future Work* (citing exactly cases like
"Process runs on Host"). Toise needs a temporal **graph**, so it ingests edges
today via a **vendor-neutral, non-standard extension** that never pretends to be
standard OTel. It rides the same LogRecord convention:

| LogRecord attribute            | Type    | Required | Meaning                                              |
| ------------------------------ | ------- | -------- | ---------------------------------------------------- |
| `entity.relation.event.type`   | string  | yes      | `state` (upsert) or `delete`                         |
| `entity.relation.interval`     | int     | no       | heartbeat cadence in **milliseconds**; arms the edge liveness backstop |
| `entity.relation.type`         | string  | yes      | the relation type — **must be in Toise's registry**  |
| `entity.relation.from.type`    | string  | yes      | source endpoint entity type                          |
| `entity.relation.from.id`      | **map** | yes      | source endpoint identity                             |
| `entity.relation.to.type`      | string  | yes      | target endpoint entity type                          |
| `entity.relation.to.id`        | **map** | yes      | target endpoint identity                             |
| `entity.relation.attributes`   | **map** | no       | descriptive edge attributes                          |

**Why `entity.relation.*` (vendor-neutral), not `toise.*` or `senhub.*`:** the
extension carries neither a consumer prefix (`toise.*`) nor a producer prefix
(`senhub.*`). A neutral name lets any producer and any consumer speak it, and is
shaped to map **1:1 onto the eventual OTel relationships standard**, so migration
is trivial for everyone. We deliberately avoid `otel.entity.relationship.*` too —
that would squat the reserved OTel namespace before the spec exists. The extension
is explicitly **transitional** and both sides commit to migrating to the standard
once it lands.

**Strict purity:** a relation record carries **no `otel.entity.*` attribute at
all** — its lifecycle is the neutral `entity.relation.event.type` (`state` /
`delete`), never `otel.entity.event.type`. This matters for interop: a record with
`otel.entity.event.type=relation_state` but no `otel.entity.type`/`id` would look
like a *malformed* entity event to a standard OTel entity-events consumer; carrying
no `otel.entity.*`, the relation record is instead cleanly ignored by such a
consumer. The boundary routes by which lifecycle key is present:
`otel.entity.event.type` → entity event, `entity.relation.event.type` → relation.

**Endpoint resolution is by exact identity**, against a **live** entity by
`(type, identity)`. Endpoint identities must be the entity's current identity.
Producers should still emit the endpoint `entity_state` events **before** the
edge, but ordering is **not required**: with the **reconciliation buffer** enabled
(`--relation-buffer-ttl`, on by default in `toise-server`), an edge whose endpoint
is not yet present is **parked** and retried as later entities arrive, and dropped
with a `Warn` only if its endpoints have not appeared within the TTL — so
out-of-order delivery (OTLP guarantees no inter-batch order) never silently loses
edges. With the buffer disabled, a missing endpoint is a retriable ingest error
instead. See *Robustness backstops* below.

## Mapping table

| OTel concept                              | Toise field                                                                 |
| ----------------------------------------- | --------------------------------------------------------------------------- |
| Entity type                               | `Entity.type`                                                               |
| Entity identifying attributes             | `Entity.identity`                                                           |
| Entity descriptive attributes            | `Entity.attributes`                                                          |
| Entity `schema_url`                       | `Entity.schema_url`                                                          |
| `LogRecord` timestamp / observed timestamp | `event_time` (producer/reality)                                            |
| (ingestion)                               | `recorded_at` — set by Toise at ingest, never taken from the producer       |
| OTel `AnyValue`                           | Toise typed `Value` (`string` / `int64` / `double` / `bool`)                |

### AnyValue restriction

Toise's typed `Value` is a deliberate subset of OTel `AnyValue`. Only the four
scalar kinds — `string`, `int64`, `double`, `bool` — are supported in phase 1.

The `id` / `attributes` (and relation-endpoint id) attributes are themselves
**maps**, and Toise reads them structurally: it iterates the **top-level**
map and keeps each entry whose **leaf value is one of the four scalars**. A
non-scalar leaf (a nested `kvlist`/`array`/`bytes`) is dropped — the boundary does
**not** recurse into nested structures.

The practical contract for producers: the `id` and `attributes` maps must be
**flat maps of scalars**. Pre-flatten any structure with dotted keys before
emitting — send `{"server.address": "10.0.0.1", "server.port": 5432}`, never
`{"server": {"address": "10.0.0.1", "port": 5432}}` (the nested `server` value
would be discarded).

**No silent loss:** dropping a nested value is **surfaced**, not silent — the
boundary logs a `Warn` naming the dropped key(s) (e.g.
`otel.entity.attributes.foo`) so the loss is observable. Pre-flattening remains
the producer's contract; the consumer never drops data quietly.

### Entity liveness — explicit delete primary, interval backstop

Liveness uses **two mechanisms, not one**:

1. **Explicit `entity_delete` is the primary signal.** When a producer knows an
   entity is gone, it emits `entity_delete` and Toise soft-deletes it. A heartbeat
   is a re-emitted `entity_state` (unchanged → `entity.unchanged`, coalesced under
   retention, ADR 0013).
2. **`otel.entity.interval` is a backstop.** A producer often *misses* the clean
   delete — `kill -9`, crash, host powered off, network partition — so relying on
   the explicit delete alone accumulates zombie entities. OTel's `Interval` exists
   exactly for this ("resilient to event losses"). An entity observed with an
   `otel.entity.interval` (heartbeat cadence, in ms) is armed with a deadline; a
   periodic sweeper (`toise-server --liveness-sweep-interval`, default 30s) expires
   it with `entity.deleted` if it is not re-asserted within the interval. The
   producer should **size the interval to include slack** (e.g. a few heartbeats)
   so a single late heartbeat does not expire a live entity.

Producers emit **both**: the explicit delete *and* the interval. Only entities that
carry an interval are ever expired, so the sweeper is safe to leave on.

The **same backstop applies to edges**: a relation observed with an
`entity.relation.interval` (ms) is armed with its own deadline, and the sweeper
expires it with `relation.removed` if it is not re-asserted in time. `relation_delete`
remains the primary signal; only edges that carry an interval are ever expired.

### Identity matching — exact (immutable Id)

Entity identity is matched **exactly** against the producer-declared Id, in line
with the OTel model where an entity's Id is **immutable** (ADR 0018, superseding
the tolerant matching of ADR 0017).

Background: phase-1 Toise matched *tolerantly* (an observation differing in at
most one identifying value was treated as the same entity that "changed identity",
ADR 0017). That conflates two cases the data cannot distinguish — "same entity,
attribute changed" vs "a different, similar entity" — and causes **silent
over-merges** (two databases on a host differing only by port; two NICs; two jobs
collapse into one). For an infrastructure source of truth that is the wrong
trade-off.

Under exact matching:

- An entity keeps its Id; descriptive attributes change → `entity.attribute_updated`
  / `state_changed` (already exact-Id matched).
- The Id changes → a *different* entity (delete-and-create from the consumer's
  view). Fuzzy "identity changed" detection is dropped; if continuity across an Id
  change is ever needed, the producer signals it explicitly.
- A single-key identity (`host.id`, the agent key) is **valid again** — no need for
  the "≥2 values / composite key" rule.

The corollary for producers: **Ids must be immutable** — never put a mutable value
(a pid, a leased IP, a **network address**) in the identity; those are descriptive
attributes. In particular a `db` identity must be a **stable source identifier**
(PostgreSQL `system_identifier`, MySQL `server_uuid`, or an operator-configured
logical instance name), **not** a network-derived composite like
`host:port` — the address moves under DHCP/failover/VIP, which would make the
instance look like a brand-new entity and orphan its edges. `server.address` /
`server.port` / `db.system.name` stay descriptive attributes.

*(Implemented: ADR 0017 is superseded by ADR 0018; the change engine matches
exactly and no longer emits `entity.identity_changed`, though the type is retained
in the taxonomy for replay/wire compatibility.)*

### Type registry — types must be known

`otel.entity.type` and `entity.relation.type` are validated against Toise's
**type registry**; an unregistered type is **rejected** at the boundary. The
registry is:

- **entities:** `host`, `process`, `network.interface`, `network.address`,
  `network.route`, `service.listener`, and the producer vocabulary
  `service.instance`, `db`, `network.device`;
- **relations:** `runs_on`, `has_interface`, `bound_to`, `next_hop_via`,
  `listens_on`, and the producer vocabulary `monitors`, `routes_via`,
  `forwards_to`, `adjacent_to` (each with declared endpoint types and a
  structural flag).

`runs_on` is the foundational producer edge — `service.instance --runs_on--> host`
as well as the existing `process --runs_on--> host`. Endpoint-type pairings are
**advisory** (not runtime-enforced), so `monitors` may target a host, db, or
network.device. Further monitored-system types (e.g. those discovered by later
collection lots) are added to the registry when introduced — the vocabulary is the
explicit coordination point with a producer (see `senhub-agent-contract.md`).

### Robustness backstops (the "no silent loss" principle)

A graph that aims to be an infrastructure source of truth must not lose or merge
data **silently**, and must not assume fragile invariants (event ordering, a
reliably-delivered delete). The agreed backstops, some implemented and some
planned:

| Concern | Decision | Status |
| ------- | -------- | ------ |
| Entity collisions | exact-Id matching (no fuzzy merge) | **done** (ADR 0018) |
| Missed deletes | explicit `entity_delete`/`relation_delete` + `interval` TTL backstop | **done** (entity + edge expiry) |
| Out-of-order edges | reconciliation buffer (park & flush, opt-in) | **done** |
| Nested values | explicit `Warn` on drop (never silent) | **done** |
| Scope flag `otel.entity.entity_event=true` | accepted and ignored (never rejected) — interop fast-path for other OTel producers | done |

### Timestamps

OTel supplies the producer-side time via the `LogRecord` timestamp (or observed
timestamp), which becomes Toise's **`event_time`**. Toise always stamps
**`recorded_at`** itself at ingestion — it is never derived from the producer.
The two-timestamp model is described in
[ADR 0005](../architecture/adr/0005-bi-temporal-event-model.md).

## Schema versioning and spec churn

The Toise **`schema_version` (`"1.0"`) is independent of the OTel spec version.**
When the OTel entity-events spec changes, the change is absorbed by a
**migration layer at the ingest boundary** rather than by mutating the internal
model. This concentrates all spec-churn handling in one place and keeps stored
history and the projections built on it insulated from upstream version changes.

The OTel entity-events specification is **experimental** and still evolving. Its
shape can change between releases in ways that affect the wire contract Toise
consumes. Toise tracks the spec actively and pins its OTel dependencies; see
[ADR 0015](../architecture/adr/0015-tracking-otel-entity-events-spec.md) for the
tracking policy.

## Producers and the reference producer

A producer-side issue tracking entity-event emission in **senhub-agent** is open
at [senhub-io/senhub-agent#185](https://github.com/senhub-io/senhub-agent/issues/185).
Until that lands, **Milestone 4 uses a synthetic OpenTelemetry SDK Go client as
the reference producer**, exercising the OTLP ingest boundary against
spec-conformant entity events.
