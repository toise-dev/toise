# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

<!-- Add new changes here under Added / Changed / Deprecated / Removed / Fixed / Security as the project evolves. -->

## [0.2.0] - unreleased

**A breaking wire-contract release.** 0.2.0 realigns Toise onto the **merged**
OpenTelemetry entity-events specification (`specification/entities/entity-events.md`,
merged 2026-06-04) and removes the transitional relation extension. What changes is
the **wire contract producers emit**; stored event-log data and the GraphQL/MCP
query schemas are unaffected. Toise is pre-1.0/alpha, so this is a clean break with
no compatibility shim — update producers in lockstep. See the
[0.1 → 0.2 migration guide](docs/migration/0.1-to-0.2.md).

### Changed

- **Relationships are embedded-only.** Edges now ride **embedded** on the source
  entity's state event as an `entity.relationships` array (`{ relationship.type,
  entity.type, entity.id }` naming the target); removal is by absence. The engine,
  change taxonomy, and bi-temporality are unchanged — the ingest boundary still
  translates each descriptor into a first-class relation event (ADR 0022). (#69,
  #70, #71, #72, #74)
- **Ingest realigned onto the merged OTel entity-events spec.** Entity events are
  identified by the LogRecord **`EventName`** (`entity.state` / `entity.delete`),
  not an attribute; attribute keys drop the `otel.` prefix and rename
  (`entity.type`, `entity.id`, `entity.description`); the liveness interval is
  **`entity.report.interval` in seconds** (was `otel.entity.interval` in
  milliseconds — a unit fix); the relationship descriptor field is
  `relationship.type`; `entity.id` is typed `map<string,string>`. (#80)
- **Process identity follows the OTel semantic conventions** —
  `{ process.pid, process.creation.time }` so PID reuse across a restart is a new
  process, not a mutated one. (#62)

### Added

- **Layered configuration for `toise-server`** — built-in defaults < YAML file
  (`--config` / `TOISE_CONFIG`) < environment (`TOISE_*`) < flags. Unknown YAML keys
  are rejected; secrets are sourced from the environment only. The flag surface is
  unchanged. (ADR 0023; `docs/operations/configuration.md`;
  `examples/toise-server.yaml`) (#46)
- **GraphQL API reference** — schema, Relay pagination, the bi-temporal
  `eventTime`/`recordedAt`/`asKnownAt` model, worked example queries, and the
  guardrails. (`docs/reference/graphql.md`) (#85)
- **`connected_to` relation type and topology-as-entities** — ports as
  `network.interface` entities linked by `has_interface`, with bare `connected_to`
  adjacency, so edges stay attribute-free under the embedded model. (#71)
- **`graph-viz` example** — a live GraphQL-subscriptions client rendering the graph
  in real time. (#59)
- Architecture decisions: **ADR 0021** (human interfaces live at the edge, not the
  core), **ADR 0022** (the engine stores facts only), **ADR 0023** (layered
  configuration).

### Removed

- **The `entity.relation.*` relation extension** — separate relation LogRecords,
  edge attributes, and the strict-purity routing path are gone. Relationships are
  embedded-only (see *Changed*). (#74)

### Fixed

- **WebSocket subscriptions no longer hit the per-request timeout.** The GraphQL
  subscription upgrade is routed around `http.TimeoutHandler` (which cannot hijack
  the connection), so long-lived subscriptions work. (#57)

## [0.1.1] - 2026-06-02

First real-world validation against the **real senhub-agent** producer, which
surfaced and fixed a silent OTLP ingestion bug.

### Fixed

- **OTLP ingestion now accepts gzip-compressed exports.** The OTLP/gRPC receiver
  did not register the gzip decompressor, so gzip-compressed exports — the OTel
  SDK default, and what the senhub-agent reference producer ships — failed at the
  gRPC transport (`"Decompressor is not installed"`) *before reaching the handler*
  and were silently dropped (the OTel SDK swallows the export error), surfacing
  only as an empty graph. Found connecting the real senhub-agent to a running
  `toise-server`. (#32)

### Changed

- **Tooling:** the lint CI builds golangci-lint from source with the repository's
  Go toolchain (so it tracks the latest Go) and migrates to golangci-lint v2.
  (#34, #37)

## [0.1.0] - 2026-06-02

First tagged release: the phase-1 backend (M0–M8) plus the producer↔consumer
contract converged with the senhub-agent reference producer.

### Added

- **Data model & proto contract** — OTel-aligned entity/relation model with a
  stable logical entity id plus a 128-bit identity hash, typed attribute values,
  a type registry, and a protobuf wire contract generated with buf. (M1; ADR
  0004, 0005, 0006, 0015, 0017)
- **Event log store** — append-only, bi-temporal log on Pebble with secondary
  indexes (by entity, change type, event time), durable `Append`, crash
  recovery, and heartbeat-coalescing retention. (M2; ADR 0007, 0013)
- **Projection & change detection** — in-memory graph rebuilt from the log, with
  the nine-type change taxonomy, **exact identity matching** (immutable ids), and
  structural relation changes flagged high-priority. (M3; ADR 0008, 0018)
- **OTLP ingestion** — an OTLP/gRPC logs receiver that converts entity-event
  LogRecords into change-engine observations: standard `otel.entity.*` nodes and
  the vendor-neutral `entity.relation.*` edge extension. (M4; ADR 0009)
- **GraphQL API** — schema-first gqlgen API with rich descriptions, Relay cursor
  pagination, subscriptions, a complexity limit and per-request timeout, served
  at `/graphql` with a playground at `/playground`. (M5; ADR 0010)
- **MCP server** — a Model Context Protocol server (official Go SDK) exposing six
  typed tools (`find_entities`, `get_entity`, `get_neighbors`, `entity_history`,
  `recent_changes`, `describe_schema`) over stdio and Streamable HTTP at `/mcp`,
  with a sample Claude Desktop config. (M6; ADR 0011)
- **Debug UI** — a minimal, server-rendered HTML view over the same read model
  (dashboard, entity list, entity detail, recent changes) at `/`. (M7; ADR 0012)
- **Demo fixture** — `toise-demo` seeds the "a day in the life of web-server-1"
  24-hour scenario; `docs/demo/` documents the timeline and twelve LLM example
  prompts. (M8)
- **`toise-server`** — single binary wiring the store, projection, OTLP receiver,
  GraphQL, MCP, and debug UI together; loopback by default. Liveness/robustness
  flags: `--liveness-sweep-interval`, `--relation-buffer-ttl`.

### Added — producer↔consumer contract (senhub-agent #185)

- **Producer vocabulary** in the type registry: entities `service.instance`, `db`,
  `network.device`; relations `monitors`, `runs_on` (also `service.instance→host`),
  `routes_via`, `forwards_to`, `adjacent_to`.
- **Vendor-neutral relation extension `entity.relation.*`** with **strict purity**
  (relation records carry no `otel.entity.*`, discriminated by
  `entity.relation.event.type`), designed to map 1:1 onto the future OTel
  relationships standard.
- **Liveness backstops:** explicit `entity_delete`/`relation_delete` primary, plus
  an `otel.entity.interval` / `entity.relation.interval` TTL sweeper; edge liveness
  derived from endpoints (cascade); an out-of-order edge reconciliation buffer.
- **Per-producer reference counting** for entity liveness, keyed by the OTLP
  Resource `service.instance.id`, so multiple agents observing one entity no longer
  flap on a single producer's delete. (ADR 0019)
- **No silent loss at the boundary:** non-scalar attribute values are logged
  (`Warn`) rather than dropped silently; flat scalar maps are the producer contract.
- **Shared conformance fixture** (`internal/ingest/testdata/conformance/`): an
  OTLP/JSON batch ingested by a contract test, the executable interface between
  Toise and producers.

### Changed

- **Exact identity matching supersedes tolerant matching** (ADR 0018, superseding
  ADR 0017): identities are immutable, so a differing identity is a different
  entity. `entity.identity_changed` is retained in the taxonomy but no longer
  emitted by the engine.

### Security

- **No authentication in phase 1** (ADR 0014). All surfaces bind to loopback by
  default and are intended for trusted networks only; the WebSocket subscription
  endpoint enforces an origin check.

[Unreleased]: https://github.com/toise-dev/toise/compare/0.1.0...HEAD
[0.1.0]: https://github.com/toise-dev/toise/releases/tag/0.1.0
