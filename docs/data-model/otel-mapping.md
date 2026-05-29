# OpenTelemetry entity-events mapping

This document records the mapping between the
[OpenTelemetry Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
/ entity-events specification and the Toise schema. It is maintained per
[ADR 0015](../architecture/adr/0015-tracking-otel-entity-events-spec.md) and kept
up to date as either side changes.

## How OTel entity events reach Toise

OTel entity events are carried as **OTLP `LogRecord`s** annotated with the entity
semantic conventions. Toise ingests them at the **OTLP boundary** in
**Milestone 4**: the boundary is the single place where the OTel wire shape is
translated into the internal Toise event model.

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
Nested **`kvlist`**, **`array`**, and **`bytes`** values are **not supported**;
the ingest boundary rejects or flattens them rather than carrying them into the
internal model. This is revisited only if a later phase needs richer values.

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
