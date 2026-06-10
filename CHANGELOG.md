# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

<!-- Add new changes here under Added / Changed / Deprecated / Removed / Fixed / Security as the project evolves. -->

## [0.4.0] - 2026-06-10

**The correctness and LLM-querying release.** A full multi-dimensional audit of
the server (46 confirmed findings, every high/medium counter-verified against
the code) drove this release end to end: first the correctness lot that makes
"the log is the source of truth" actually hold under failure, then a product
lane that turns the MCP surface into a precise, budget-aware query layer —
three new tools, edge-aware traversal, and bounded results — plus tenant
security, an ingestion that is finally observable, and maintenance that no
longer stalls ingest. No wire-contract change for producers; a handful of
sharper behaviors are listed in the
[0.3 → 0.4 migration guide](docs/migration/0.3-to-0.4.md). Validated end to end
on a live staging deployment fed by a real agent before tagging.

### Fixed

- **Ingest integrity: the projection can no longer run ahead of the durable
  log.** Batched commits are a staged unit of work — events reach the in-memory
  graph and the subscribers only after the durable append succeeds, so a failed
  flush leaves no phantom state and a producer retry regenerates everything
  (previously a relation lost in a failed flush was never written again).
  Records violating the wire contract (unknown `entity.type`, malformed
  identity) are rejected **per record** via OTLP partial success instead of
  poisoning their whole batch, and removal of an already-cascaded relation is
  the no-op the contract promises instead of a poison pill that failed every
  subsequent export from that producer. (#108, #109, #110)
- **Restarts no longer corrupt the graph.** Projection snapshots omit
  soft-deleted entities (restoring no longer resurrects the dead), and replay
  rebuilds the identity/type indexes from update events — after retention
  pruning, an entity whose first surviving event was an update used to become
  unmatchable, minting permanent duplicates on its next observation. (#106, #107)
- **OTLP `Export` returns proper gRPC status codes**: `InvalidArgument` for
  permanent caller errors (invalid tenant ids, refused tenants),
  `Unavailable` for transient store failures — previously everything surfaced
  as `Unknown`, which spec-compliant exporters treat as non-retryable, silently
  dropping batches the design intends to be retried. (#111)
- **Lifecycle**: the sweep/compaction/snapshot loops are joined before the
  stores close (no more `panic: pebble: closed` when shutdown coincides with a
  maintenance tick); a post-startup OTLP receiver failure now exits the process
  instead of leaving a green `/readyz` over a dead ingest; and a deploy with
  connected streaming clients (MCP SSE, GraphQL WebSocket) exits clean instead
  of failing its shutdown grace. (#112, #130)
- A mis-typed `entity.report.interval` (e.g. a string) is surfaced on the
  dropped-keys path instead of silently disarming the liveness backstop; the
  out-of-order relation buffer is capped and swept; configurations that would
  silently not do what they say (half-set TLS, retention without a compaction
  interval, unknown log level) are rejected at startup. (#115)

### Added

- **Three new MCP tools.** `graph_diff` folds the change log between two
  instants into the net difference — created / deleted / changed, plus a
  first-class *transient* bucket for flapping entities and relations.
  `find_path` finds the shortest relation path between two entities
  (`reachable: false` is an answer, not an error). `telemetry_keys` derives the
  exact join keys that locate an entity's metrics and logs in observability
  backends — own and 1-hop-inherited OTel resource attributes, each with its
  Prometheus-style flattened label form and usage caveats. (#115)
- **Result budgets across the MCP timeline tools.** `recent_changes` and
  `entity_history` exclude heartbeats by default, accept `change_type` and
  `include_heartbeats` filters, bound their output with `limit`, and report a
  digest (`total`, `truncated`, `heartbeats_excluded`, per-type counts) so an
  LLM can narrow without paging blind. `get_neighbors` now tells *how* each
  entity was reached (`via_relation`, `direction`, `depth`). Every tool call
  runs under a 30-second budget, and store reads honor caller cancellation.
  (#115)
- **Ingest is observable**: hot-path Prometheus counters for export outcomes,
  per-record results (handled / ignored / rejected), dropped attribute values,
  tenant rejections, and authentication failures — "is ingest healthy?" now has
  an answer on `/metrics`. (#113)
- **Tenant security.** Bearer tokens can be bound to tenants
  (`TOISE_TENANT_TOKENS` takes `tenant:token` pairs): a scoped token is
  authorized only for its tenant, enforced on the HTTP surfaces (403) and on
  ingest per *resolved* tenant (`PermissionDenied`) — the per-`ResourceLogs`
  `tenant.id` override cannot bypass it. Runtime tenant creation is bounded
  (`tenant_auto_create`, `tenant_allowlist`, `max_tenants`), query surfaces can
  no longer create a tenant by reading it (unknown tenants are a 404), and
  startup warns loudly when a listener is exposed without auth or TLS.
  (#104, #115)
- **`toise-server checkpoint`** — a consistent, per-tenant cold-backup command
  (the operator-facing trigger `Store.Checkpoint` was documented to have), with
  a new Backups page in the user guide. (#115)
- ADR 0026 fixes the reconciliation policy for Resource-borne entities (OTel
  spec PR 5147) ahead of implementation: entity events stay authoritative for
  lifecycle, resource refs associate and may opt-in bootstrap presence. (#105)

### Changed

- **Store maintenance no longer stalls ingestion.** Heartbeat coalescing and
  retention pruning scan on a Pebble snapshot off the append mutex — each
  maintenance tick used to block that tenant's ingest for the duration of a
  full-log scan, growing with history. Pruning also stopped re-marshaling every
  pruned event just to count bytes. (#115)
- The `toise_entities_by_type` metric reports the 50 largest types and folds
  the tail into `other` (the label was producer-controlled and unbounded).
- Tenant stacks open outside the registry's global mutex (one tenant's Pebble
  open no longer blocks every other tenant's requests), with single-flight
  deduplication.
- Internal error details no longer leak to HTTP clients on tenant-resolution
  failures; `/readyz` names the failing tenant.

### Security

- Cross-tenant read/write/create via a client-chosen `X-Scope-OrgID` is closed
  when tenant-scoped tokens are configured; tenant minting is bounded; reading
  can never create. See *Added → Tenant security*. (#104)


## [0.3.0] - 2026-06-08

**The production-readiness and multi-tenancy release.** 0.3.0 turns the phase-1
backend into something deployable in a real, multi-tenant production posture:
native authentication and TLS, operational endpoints and structured logging,
bounded on-disk growth, fast restart, packaged release artifacts, and — the
headline — **per-tenant isolated graphs**. No wire-contract change to the OTLP
producer payload; the only behavioral change for existing deployments is the
on-disk layout (auto-migrated). See the
[0.2 → 0.3 migration guide](docs/migration/0.2-to-0.3.md).

### Added

- **Multi-tenancy: per-tenant isolated graphs.** One Toise instance can now serve
  multiple tenants with fully isolated graphs. Each tenant gets its own
  `{store, projection, change-engine}` stack under `<data-dir>/<tenant>/` (ADR 0025).
  The tenant id is generic and vendor-neutral — read from the `X-Scope-OrgID`
  request metadata (the Mimir/Loki/Tempo/VictoriaMetrics de-facto standard; HTTP
  header on queries, gRPC metadata on ingest) or a `tenant.id` resource attribute,
  falling back to `default`. Ingest routes per `ResourceLogs` (so one OTLP stream
  can carry several tenants); the GraphQL, MCP, and debug-UI surfaces are scoped by
  `X-Scope-OrgID`; the liveness sweep, compaction, and snapshotting run per tenant;
  `/metrics` reports the sum across tenants. A pre-existing single-tenant data
  directory is migrated to `<data-dir>/default/` automatically on first start, and a
  deployment that never sets a tenant id behaves exactly as before. (#95, #100, #101)
- **Native bearer-token authentication and TLS** on the data surfaces. Tokens are
  supplied via the environment only (`TOISE_AUTH_TOKENS`); the gRPC ingest and the
  HTTP query surfaces enforce them when set. TLS is enabled by pointing at a
  cert/key pair. Both are off by default — the trusted-network posture (ADR 0014) is
  preserved. The operational probes and the metrics scrape stay public. (ADR 0024;
  #43)
- **Operational endpoints and structured logging.** `/healthz` (liveness),
  `/readyz` (readiness — checks every tenant store), and a Prometheus `/metrics`
  endpoint sampled at scrape time (entities, relations, events, disk usage,
  retention/pruning and snapshot counters, build info). Logs are structured; the
  level is set with `--log-level`. (#44)
- **Retention pruning** to bound on-disk growth. With `retention_max_age` set, a
  compaction goroutine prunes events older than the horizon while preserving the
  current-state projection (the keep-set is the latest event per live entity).
  Heartbeat coalescing runs alongside it. (ADR 0013; #45)
- **Projection snapshots for fast restart, plus backup/restore.** With
  `snapshot_interval` set, the server periodically writes a projection snapshot into
  the store; on the next start it loads the snapshot and replays only the tail —
  restart time is bounded by snapshot age, not by total history. `Store.Checkpoint`
  produces a consistent, lock-free backup copy. (#49)
- **Packaged release artifacts.** Tag-triggered CI builds static binaries for
  linux/darwin/windows and a distroless OCI image (GHCR); a `Dockerfile` and a
  `deploy/` directory with systemd and docker-compose examples ship in-tree. (#47)
- **Versioned documentation site** at [toise.dev/docs](https://toise.dev/docs)
  (MkDocs Material, deployed per release with mike): user guide, configuration,
  operations, data model, querying, and migration guides. (#91)

### Changed

- **Production HTTP hardening.** A single `--production` flag (or
  `TOISE_PRODUCTION`) locks down the development surfaces at once — GraphQL
  introspection, the `/playground`, and the debug UI — and an `allowed_origins`
  allowlist gates browser WebSocket origins. Each lever is also individually
  configurable. (#48)
- **On-disk layout is now per tenant** (`<data-dir>/<tenant>/`). A pre-existing
  single-tenant data directory is migrated under `<data-dir>/default/`
  automatically on first start. Take a backup before upgrading, as with any
  store-format change. (#95)
- **`toise-probe` emits topology as first-class entities and `connected_to`
  relations** instead of the legacy fabric `adjacent_to`, aligning the bundled
  probe with the current topology model. (#90)

### Security

- Authentication (bearer tokens) and TLS are now available for the ingest and query
  surfaces, and `--production` removes the development affordances from a public
  deployment. Multi-tenant isolation is by `X-Scope-OrgID`; note that a valid token
  may still set any tenant id, so isolation relies on the upstream OTel Collector
  authenticating each client and stamping its tenant (per-token tenant binding is
  future work — see ADR 0025).

## [0.2.0] - 2026-06-08

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

[Unreleased]: https://github.com/toise-dev/toise/compare/0.2.0...HEAD
[0.2.0]: https://github.com/toise-dev/toise/compare/0.1.1...0.2.0
[0.1.1]: https://github.com/toise-dev/toise/compare/0.1.0...0.1.1
[0.1.0]: https://github.com/toise-dev/toise/releases/tag/0.1.0
