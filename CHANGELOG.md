# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

These are the changes staged for the first tagged release (**0.1.0**, phase 1).
On release this section is renamed to `## [0.1.0] - <date>`.

### Added

- **Data model & proto contract** — OTel-aligned entity/relation model with a
  stable logical entity id plus a 128-bit identity hash, typed attribute values,
  a type registry, and a protobuf wire contract generated with buf. (M1; ADR
  0004, 0005, 0006, 0015, 0017)
- **Event log store** — append-only, bi-temporal log on Pebble with secondary
  indexes (by entity, change type, event time), durable `Append`, crash
  recovery, and heartbeat-coalescing retention. (M2; ADR 0007, 0013)
- **Projection & change detection** — in-memory graph rebuilt from the log, with
  the nine-type change taxonomy, tolerant identity matching that keeps the
  logical id stable across identity changes, and structural relation changes
  flagged high-priority. (M3; ADR 0008)
- **OTLP ingestion** — an OTLP/gRPC logs receiver that converts entity-event
  LogRecords into change-engine observations (entity state/delete and the Toise
  relation extensions). (M4; ADR 0009)
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
  GraphQL, MCP, and debug UI together; loopback by default.

### Security

- **No authentication in phase 1** (ADR 0014). All surfaces bind to loopback by
  default and are intended for trusted networks only; the WebSocket subscription
  endpoint enforces an origin check.
