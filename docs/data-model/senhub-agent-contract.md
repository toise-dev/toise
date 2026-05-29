# senhub-agent ↔ Toise contract (Milestone 0 check)

Status as of 2026-05-29. This documents whether senhub-agent can currently emit
the OpenTelemetry **entity events** Toise ingests, per Milestone 0 of the phase
1 brief.

## What senhub-agent is

`senhub-io/senhub-agent` (private) is a Go monitoring/observability agent that
collects metrics and events from many sources and ships them to multiple
destinations. A public documentation mirror exists at `senhub-io/docs`. A
commercial edition exists at `senhub-io/senhub-agent-enterprise` (private).

## Current OTel capability (observed)

Assessment by code search over `senhub-io/senhub-agent` (not a deep review):

| Signal | Evidence | Reading |
|--------|----------|---------|
| OTLP export | ~69 matches for `OTLP` | Yes — agent speaks OTLP |
| OpenTelemetry plumbing | ~59 matches for `opentelemetry` | Yes |
| Log records | ~24 matches for `LogRecord` | Yes — emits OTLP logs |
| **Entity events** | **0** matches for `entity event` / `EmitEntity` | **No** |

**Conclusion: "not yet."** senhub-agent already exports OTLP and emits
LogRecords, but it does **not** emit OpenTelemetry **entity events** (LogRecords
carrying the entity data-model semantic conventions Toise consumes).

## Gap to close (in senhub-agent, later phase)

To become a Toise producer, senhub-agent needs an entity-event emitter that
maps its collected inventory/topology into OTLP LogRecords following the OTel
Entity Data Model: per entity a `type`, an identifying attribute set, a
descriptive attribute set, a schema URL, and an `event_time`; plus relation
events. The exact wire mapping Toise expects is defined in
`docs/data-model/otel-mapping.md` (Milestone 1).

## Impact on Toise (none, blocking-wise)

This is **non-blocking** for Toise phase 1. Per the brief, Milestone 4's
ingestion integration test uses a **synthetic OpenTelemetry SDK Go client** as
the producer, not senhub-agent. senhub-agent integration catches up in a later
phase.

## Follow-up

A GitHub issue describing the entity-event emitter work should be opened in
`senhub-io/senhub-agent` once the Toise OTel mapping (Milestone 1,
`otel-mapping.md`) is finalized, so the issue can reference the exact contract.
Drafted at Checkpoint 0; creation pending maintainer go-ahead (separate repo).
