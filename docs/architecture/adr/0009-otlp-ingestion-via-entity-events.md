# 9. OTLP ingestion via entity events

- Status: Accepted
- Date: 2026-05-29

## Context

Toise ingests infrastructure facts as OpenTelemetry **entity events**, carried
as OTLP **`LogRecord`s** annotated with the entity semantic conventions
(ADR 0015, `docs/data-model/otel-mapping.md`). Toise implements no collectors of
its own. Producers push to it over OTLP/gRPC: a synthetic OpenTelemetry SDK Go
client in tests, and senhub-agent or an OpenTelemetry Collector in production.

The OTel entity-events specification is **experimental** and still evolving. To
avoid letting upstream churn reach the internal model, Toise pins a concrete
`LogRecord` convention at its ingest boundary and absorbs spec changes there
(ADR 0015).

## Decision

A gRPC server in `internal/ingest` implements the **OTLP logs service** using the
collector `pdata` types (`go.opentelemetry.io/collector/pdata`, packages `plog`
and `plogotlp`). It accepts `ExportLogs` requests, iterates
ResourceLogs → ScopeLogs → LogRecords, and converts each **entity-event**
`LogRecord` into a change-engine observation. Non-entity `LogRecord`s are ignored.

The phase-1 `LogRecord` convention — a pragmatic reading of the entity-events
spec — is:

- Attribute `otel.entity.event.type` selects the kind: `entity_state`,
  `entity_delete`, and the Toise relation extensions `relation_state` and
  `relation_delete`.
- **Entity events** carry `otel.entity.type` (string), `otel.entity.id` (a
  key-value map of identifying attributes), and `otel.entity.attributes` (a
  key-value map of descriptive attributes). The `LogRecord` timestamp is
  `event_time`.
- **Relation events** are a Toise extension, since the spec does not yet
  standardize relationships. They carry `toise.relation.type`,
  `toise.relation.from.type` / `toise.relation.from.id`,
  `toise.relation.to.type` / `toise.relation.to.id`, and
  `toise.relation.attributes`.
- Only OTel **scalar** attribute values map to Toise's typed `Value`
  (`string` / `int64` / `double` / `bool`, per ADR 0004). Nested `kvlist` is
  used only for the id and attributes maps.

Converted observations are handed to the change-detection engine
(ADR 0006 / ADR 0008), which classifies, persists, and projects them.
`recorded_at` is stamped by Toise at ingestion and is never taken from the
producer (ADR 0005).

## Consequences

- Toise speaks the OTLP standard rather than a proprietary protocol; any OTel
  producer can feed it.
- The relation convention is Toise-specific until the spec catches up. It is
  isolated to the ingest boundary, so the internal model is unaffected
  (ADR 0015).
- The collector `pdata` dependency is pinned and bumped deliberately
  (ADR 0003 / ADR 0015).
- Milestone 4's integration test drives the receiver with a synthetic OTLP logs
  client. senhub-agent integration follows later
  ([senhub-io/senhub-agent#185](https://github.com/senhub-io/senhub-agent/issues/185)).

See also: ADR 0004 (typed `Value`), ADR 0005 (`recorded_at`),
ADR 0006 (change taxonomy), ADR 0008 (change engine target),
ADR 0015 (spec tracking).
