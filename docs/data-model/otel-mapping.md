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

The ingest boundary classifies each `LogRecord` **solely by the presence of the
`otel.entity.event.type` attribute**. A record without it is ignored (it is
treated as an ordinary log). The scope is **not** inspected — Toise does not
require the experimental `otel.entity.entity_event=true` instrumentation-scope
flag; a producer may set it for spec fidelity, but Toise neither reads nor
requires it.

### Entity events (standard OTel convention)

| LogRecord attribute        | Type    | Required | Meaning                                                |
| -------------------------- | ------- | -------- | ------------------------------------------------------ |
| `otel.entity.event.type`   | string  | yes      | `entity_state` (upsert) or `entity_delete` (soft delete) |
| `otel.entity.type`         | string  | yes      | the entity type — **must be in Toise's type registry** |
| `otel.entity.id`           | **map** | yes      | identifying attributes (the entity's identity)         |
| `otel.entity.attributes`   | **map** | no       | descriptive, non-identifying attributes                |
| `LogRecord.Timestamp`      | —       | yes      | becomes `event_time` (falls back to `ObservedTimestamp`, then ingest time) |

Notes:

- `otel.entity.id` and `otel.entity.attributes` are genuine OTLP **maps**
  (`AnyValue` kvlist), parsed structurally — see *AnyValue restriction* below.
- `otel.entity.interval` is **not consumed** in phase 1 (see *Entity liveness*).
- A `schema_url` is part of the model but is **not currently read** at the OTLP
  boundary; entities ingested via OTLP carry an empty schema URL for now.

### Relation events — the `toise.relation.*` extension (non-standard)

The OTel Entity Data Model does **not yet model entity-to-entity relationships**;
[OTEP 0256](https://github.com/open-telemetry/oteps/blob/main/text/entities/0256-entities-data-model.md)
lists relationships as explicit *Future Work* (citing exactly cases like
"Process runs on Host"). Toise needs a temporal **graph**, so it ingests edges
today via a clearly-namespaced, **non-standard extension** that never pretends to
be standard OTel. It rides the same LogRecord convention:

| LogRecord attribute          | Type    | Required | Meaning                                              |
| ---------------------------- | ------- | -------- | ---------------------------------------------------- |
| `otel.entity.event.type`     | string  | yes      | `relation_state` (upsert) or `relation_delete`       |
| `toise.relation.type`        | string  | yes      | the relation type — **must be in Toise's registry**  |
| `toise.relation.from.type`   | string  | yes      | source endpoint entity type                          |
| `toise.relation.from.id`     | **map** | yes      | source endpoint identity                             |
| `toise.relation.to.type`     | string  | yes      | target endpoint entity type                          |
| `toise.relation.to.id`       | **map** | yes      | target endpoint identity                             |
| `toise.relation.attributes`  | **map** | no       | descriptive edge attributes                          |

**Why `toise.relation.*` and not a producer namespace (e.g. `senhub.*`):** the
relation wire format is the **consumer's** contract. senhub-agent is one producer
among potentially many; every producer feeds the *same* boundary, so the keys are
namespaced to the format Toise parses, not to any one producer. The extension is
designed to be **retired in favour of the OTel standard** once relationships land
in the spec — both sides migrate together at that point.

**Endpoint resolution is by exact identity.** Each endpoint is matched to a
**live** entity by `(type, identity)` using an **exact** match (no tolerance). So:

- both endpoint entities must already exist when the relation event is processed —
  emit the `entity_state` events for the endpoints **before** the
  `relation_state` that connects them (a relation to an unknown endpoint is a
  retriable ingest error, failing that export batch);
- endpoint identities must be the entity's **current** identity (after an
  identity change, reference the new identifying values).

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
non-scalar leaf (a nested `kvlist`/`array`/`bytes`) is **silently dropped — not
flattened**. The boundary does **not** recurse into nested structures.

The practical contract for producers: the `id` and `attributes` maps must be
**flat maps of scalars**. Pre-flatten any structure with dotted keys before
emitting — send `{"server.address": "10.0.0.1", "server.port": 5432}`, never
`{"server": {"address": "10.0.0.1", "port": 5432}}` (the nested `server` value
would be discarded). This is revisited only if a later phase needs richer values.

### Entity liveness — no interval-based expiry

Toise tracks liveness by **events, not by a TTL**. An entity stays live until an
explicit `entity_delete` (a soft delete) arrives; the OTel `otel.entity.interval`
is **not** consumed and does **not** expire entities in phase 1. A heartbeat is a
re-emitted `entity_state` with unchanged identity/attributes, which Toise
classifies as `entity.unchanged` and coalesces under retention (ADR 0013).
**Producers must emit an explicit `entity_delete` when an entity disappears**;
they cannot rely on letting an interval lapse. (Interval-driven expiry is a
candidate for a later phase.)

### Identity and the matching constraint (read this before choosing identities)

Toise assigns a stable logical id on first sight and matches later observations
to it **tolerantly**: an observation may differ from a live entity in **at most
one** identifying value (same key set) and still be treated as the *same* entity
that changed identity (ADR 0017). This is what lets a process keep its logical id
across a restart that changes its pid.

The consequence for producers: **two genuinely distinct entities must differ in
at least two identifying values, or use a single composite identity key.** If two
distinct instances differ in exactly one identifying value, Toise will mistake the
second for an identity-change of the first and merge them. For example, an identity
of `{db.system.name, server.address, server.port}` makes two databases on the same
host that differ only by port collapse into one. Prefer a single composite key
(e.g. a synthesised `db.instance.id`) or guarantee ≥2 distinguishing values.

### Type registry — types must be known

`otel.entity.type` and `toise.relation.type` are validated against Toise's
**type registry**; an unregistered type is **rejected** at the boundary. The
phase-1 registry is:

- **entities:** `host`, `process`, `network.interface`, `network.address`,
  `network.route`, `service.listener`;
- **relations:** `runs_on`, `has_interface`, `bound_to`, `next_hop_via`,
  `listens_on` (each with declared endpoint types and a structural flag).

A producer that introduces new types (e.g. `service.instance`, `db`,
`network.device`, or relations like `monitors`, `adjacent_to`, `routes_via`,
`forwards_to`) requires a corresponding **registry extension in Toise** before
those events are accepted. New types are added to the registry without breaking
existing ones; this vocabulary is the explicit coordination point between Toise
and a producer (see `senhub-agent-contract.md`).

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
