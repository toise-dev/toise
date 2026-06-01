# Phase 1 — Progress

Tracking the milestones of the Toise phase 1 backend (brief v2). Each milestone
ends at a checkpoint where work stops for maintainer validation before
continuing. The development brief itself is the session source of truth; this
file is the actionable tracker reflecting brief v2 (patches 1–9 applied).

Status legend: `not started` · `in progress` · `awaiting checkpoint` · `done`

| # | Milestone | Checkpoint | Status |
|---|-----------|------------|--------|
| 0 | Prerequisites validation | 0 | awaiting checkpoint |
| 1 | Data model and proto contract | A | awaiting checkpoint |
| 2 | Event log store on Pebble (+ retention) | B | awaiting checkpoint |
| 3 | Projection engine and change detection | C | awaiting checkpoint |
| 4 | OTLP ingestion receiver | D | awaiting checkpoint |
| 5 | GraphQL API (+ pagination/limits) | E | awaiting checkpoint |
| 6 | MCP server | F | done |
| 7 | Debug UI | G | awaiting checkpoint |
| 8 | Temporal fixtures and demo scenario | H | awaiting checkpoint |

## Milestone 0 — Prerequisites (brief v2, patch 1)

- [x] Go version check — latest stable go1.26.3; `go.mod` `go 1.26` (latest minor; CI pulls latest patch). `go build ./...` OK.
- [x] Repository inventory — bootstrap present (LICENSE, README, Makefile, CI, ADR 0001–0002, golangci).
- [x] Pebble proof of concept — `scratch/pebble-poc/` (isolated module, Pebble v1.1.5): 10k events, write ~20.9k evts/s, scan ~8.6M evts/s, 1.68 MB on disk. Batch p99 15.1 ms (over 10 ms target, but macOS dev HW + per-batch fsync — re-measure on Linux ref). → ADR **0016**.
- [x] senhub-agent integration check — `senhub-io/senhub-agent` (private) speaks OTLP/OTel + LogRecords but emits **no entity events** yet ("not yet" case). Non-blocking; M4 uses a synthetic OTel SDK client. → `docs/data-model/senhub-agent-contract.md`. Issue in senhub-agent repo drafted, pending go-ahead.
- [x] MCP Go SDK availability — **official `github.com/modelcontextprotocol/go-sdk` is GA (v1.6.1)** and will be used at M6 (no hand-rolled protocol). `…/sdk-go` (brief guess) does not exist; `mark3labs/mcp-go` (community) exists but the official SDK is preferred. To be recorded in ADR 0011 (M6).
- [x] ADR 0003 (deps strategy), ADR 0014 (no auth), ADR 0016 (pebble validation) written.

## Milestone 1 — Data model & proto contract (awaiting Checkpoint A)

- Layout realigned to brief Part 5: `cmd/toise` → `cmd/toise-server`; bootstrap placeholders (`internal/core,query,reconciler`, `receivers`, `pkg`) removed; added `internal/model`, `internal/version`.
- Proto contract `proto/toise/v1/events.proto` (`Value`, `KeyValue`, `ChangeType`, `Entity`, `Relation`, `EntityEvent`, `RelationEvent`, `Event`); codegen via **buf** (`buf.yaml`/`buf.gen.yaml`, `make proto`), generated `events.pb.go`.
- `internal/model`: hand-written domain types + `ToProto`/`FromProto`, typed `Value`, ULID logical IDs + 128-bit SHA-256 identity hash, validation, type registry. **Coverage 91 %**. Benchmarks: `IdentityHash` ~284 ns/op, `EntityToProto` ~486 ns/op.
- ADRs 0004, 0005, 0006, 0015, 0017 written; data-model docs refreshed (`README.md`, `otel-mapping.md`).
- `go build`/`go vet`/`gofmt`/`golangci-lint`/`go test -race` clean; `govulncheck` 0 affecting.

## Milestone 2 — Event log store on Pebble (awaiting Checkpoint B)

- `internal/store`: Pebble-backed append-only log. Sequence-keyed primary records + secondary indexes (entity / change-type / event_time); relation events indexed under both endpoints. Atomic durable `Append(...)` (one Sync'd batch). Reads: `Scan` (append order), `ReadByEntity`, `ReadByType`, `ReadByTimeRange`.
- Crash recovery test (subprocess writes with Sync then exits without Close; reopen recovers all events + sequence from the WAL). **Coverage 84 %**.
- Retention (ADR 0013): `CoalesceHeartbeats` collapses consecutive `entity.unchanged` runs to first+last; `Config{RetentionMaxAge, CompactionInterval}` + `--retention-*` flags on `toise-server`. Snapshot stub interface only (phase 2).
- ADRs 0007, 0013 written; `docs/operations/storage-sizing.md` + `performance.md` added.
- Benchmark `AppendBatch100` ~4.1 ms (under the 10 ms target on dev HW). `go build`/`vet`/`gofmt`/`golangci-lint`/`go test -race` clean; `govulncheck` 0 affecting.

## Milestone 3 — Projection engine & change detection (awaiting Checkpoint C)

- `internal/projection`: in-memory graph (entities, relations, in/out adjacency, identity-hash + type indexes), concurrent-safe (`RWMutex`). `Apply` per change type, `Replay` from the log, reads (`GetEntity`, `Neighbors` bounded BFS, counts), `MatchIdentity` (exact + tolerant). **Coverage 89 %**.
- `internal/change`: classifies observations into all nine taxonomy types; tolerant identity matching (ADR 0017) keeps the logical ID stable across `entity.identity_changed`; appends to store, applies to projection, notifies subscribers; **structural relation add/remove flagged high-priority**. Injectable clock. **Coverage 91 %**.
- Integration test: engine + real store, then a fresh graph rebuilt via `Replay` matches.
- ADR 0008 written. Benchmark `Replay`: ~0.44 s extrapolated for 1M events (target ≤30 s).
- **Toolchain bump go1.26.1 → go1.26.3** (`toolchain go1.26.3` in go.mod) resolving 4 stdlib `crypto/x509`/`crypto/tls` advisories (fixed in 1.26.2). govulncheck now **0 affecting**.

## Milestone 4 — OTLP ingestion receiver (awaiting Checkpoint D)

- `internal/ingest`: OTLP/gRPC logs server (collector `pdata` `plog`/`plogotlp`). Filters entity-event LogRecords by `otel.entity.event.type`, converts them to change-engine observations, routes them; non-entity records ignored. Supports `entity_state`, `entity_delete`, and the Toise `relation_state`/`relation_delete` extensions. **Coverage 90 %**.
- Added `change.Engine.DeleteEntity` (emits `entity.deleted` for a matched entity).
- Integration test: a real OTLP gRPC client exports entity + relation LogRecords over loopback; the projection reflects them (entities, relation, attributes), and `entity_delete` soft-deletes. Unit tests cover conversion error paths and typed values.
- ADR 0009 written; LogRecord convention documented (otel-mapping.md / ADR 0009).
- Benchmark `RouteEntityState` ~90 ns/op (conversion only; gRPC excluded). build/vet/gofmt/golangci-lint/`go test -race`/govulncheck all clean.

## Milestone 5 — GraphQL API (awaiting Checkpoint E)

- `internal/graphql`: schema-first gqlgen API with **rich descriptions on every type/field/arg** (LLM reads them via introspection). Queries `entity`, `entities`, `relations`, `entityHistory`, `recentChanges`; subscriptions `entityChanged`, `relationChanged`.
- **Relay cursor pagination** on all list queries (`first`/`after`, `Connection`/`edges`/`pageInfo`/`totalCount`). Generated code in `generated/`; hand-written `resolvers/` over the projection (current state) + store (history). `entityHistory` honours `since`/`until` (event_time) and `asKnownAt` (audit view, ADR 0005).
- HTTP handler with POST/GET + **WebSocket subscriptions**; **complexity limit** (default 1000), **per-request timeout** (default 10s), introspection on; LLM-friendly limit/cursor/window errors.
- ADR 0010 written. Coverage: graphql 92 %, combined resolvers+graphql 79 %.
- **Classification fix (found by GraphQL tests):** tolerant identity matching now requires an unchanged identifying value as an anchor (`diffs < len(identity)`), so a single-key identity no longer over-merges distinct entities.
- build/vet/gofmt/golangci-lint/`go test -race`/govulncheck all clean.

## Milestone 6 — MCP server (done, Checkpoint F passed)

- `internal/mcp`: MCP server on the **official Go SDK** (`modelcontextprotocol/go-sdk` v1.6.1, ADR 0011), exposing six **typed tools** via `mcp.AddTool[In, Out]` — input validation is a property of the Go struct + inferred JSON schema, not hand-written checks. Tools: `find_entities` (type + attribute filter + limit), `get_entity`, `get_neighbors` (**depth capped at 5**, friendly over-limit error), `entity_history` (`since`/`until` in event-time + optional `as_known_at` audit view, ADR 0005), `recent_changes` (Go-duration window + entity/relation/**structural** filter), `describe_schema` (NL summary + per-type counts to bootstrap the LLM).
- **Same read model as GraphQL**: tools read the in-memory projection (current state, ADR 0008) and event log (history, ADR 0007) through narrow `Graph`/`EventReader` interfaces, so the two surfaces stay consistent. Outputs are **name-bearing** (each entity carries a human-readable `label` derived from its identity, types alongside ids) so the model reasons without a second lookup; errors are plain messages.
- Served over **two transports**: Streamable HTTP mounted at `/mcp` on the existing server, and **stdio** via `--mcp-stdio` (Claude Desktop drives the binary as a subprocess; HTTP/OTLP servers are skipped). Sample config in `docs/demo/claude-desktop-config.json`.
- End-to-end test exercises the real MCP protocol over an in-memory transport (tool discovery, structured-content round-trip, tool-error vs transport-error). **Coverage 90 %.**
- Benchmarks: `FindEntities` over 10k hosts ~1.86 ms/op (target ≤10 ms); `GetNeighbors(depth=2)` ~330 ns/op (target ≤100 ms). build/vet/gofmt/golangci-lint/`go test -race`/govulncheck all clean.

## Milestone 7 — Debug UI (awaiting Checkpoint G)

- `internal/debugui`: minimal **server-rendered HTML** over the same read model as GraphQL/MCP (projection + log, via narrow `Graph`/`EventReader` interfaces). `html/template` embedded with `//go:embed`; **no client framework, no external assets/fonts, no JS** beyond one progressive-enhancement filter submit (`<noscript>` fallback). ADR 0012.
- Four read-only pages: **dashboard** (entity/relation type counts, totals, recent changes), **entity list** (filter by type, capped at 500), **entity detail** (identity, attributes, directly-connected neighbors with the linking relation + structural flag, full history oldest-first), **changes** (duration window + entity/relation/**structural** filter).
- **Safe by construction**: read-only (no mutation endpoints); all dynamic values render through `html/template` contextual auto-escaping — a test asserts an attribute value containing `<script>` is escaped, not rendered.
- **Routing**: debug UI at `/`, GraphQL stays `/graphql`, playground moved to `/playground`, MCP at `/mcp` (Go 1.22+ `ServeMux` routes specific API paths first, everything else to the UI). Live smoke confirms `/`,`/entities`,`/changes`,`/playground` → 200; `/entity?id=<unknown>` → 404.
- `httptest` handler tests per page (status, content, type filter, not-found/bad-request, escaping, unknown-path 404). **Coverage 84 %.** build/vet/gofmt/golangci-lint/`go test -race`/govulncheck all clean.

## Milestone 8 — Temporal fixtures & demo scenario (awaiting Checkpoint H)

- `internal/demo`: the **"a day in the life of web-server-1"** fixture — a 24h simulated host evolution applied **through the change engine** exactly as live OTLP ingestion would be (classification → log → projection), with a settable bi-temporal `Clock` so one fact (eth0 going down) is **recorded 20 min late** for a meaningful `asKnownAt` audit. The eight beats (discovery, dockerd start, eth0 down, eth0 back on a new subnet, postgres start, gateway change, nginx restart, container crash) exercise **all nine change types**, incl. `entity.identity_changed` on the nginx restart (tolerant identity, ADR 0017).
- **Classification finding (caught while building the fixture):** two service listeners with identity `{host.name, service.port}` over-merged — they differ in a single identifying value, which is within the tolerant-matching budget. Fixed in the fixture by giving listeners a **single composite identity key** (`service.endpoint`), forcing exact matches. (A latent trap for any type whose instances differ in exactly one identifying value; noted for phase 2.)
- `cmd/toise-demo`: seeds a fresh data dir with the scenario (default start = now−24h so windows are live; `--start` for reproducible stamps), refuses a non-empty dir, prints a summary + next steps. Added to `make build` (now builds both binaries).
- Docs: `docs/demo/scenario.md` (24h timeline + final state) and `docs/demo/llm-prompts.md` (**12 example prompts** across current-state, topology traversal, recent-changes, history, causal, anomaly, and `asKnownAt` audit — each with the expected MCP tool call(s) and answer shape).
- Tests assert the expected final graph (9 live entities, nginx identity-changed to pid 1010, deletions), coverage of all nine change types, and the late-recorded fact. **Coverage 84 %.** Live end-to-end validated: seed → `toise-server` → GraphQL (9 entities, 22 changes/24h) and the debug UI render the populated graph correctly.
- build/vet/gofmt/golangci-lint/`go test -race`/govulncheck all clean.

## Phase 1 — complete (pending Checkpoint H validation)

All eight milestones (M0–M8) implemented behind their checkpoints. The phase-1
backend ingests OTLP entity events, maintains a bi-temporal event log and an
in-memory projection with change classification, and serves three consumer
surfaces over one read model — GraphQL, MCP, and a debug UI — plus a runnable
demo. Checkpoint H is the phase-1 completion gate: on validation, cut **v0.1.0**
(CHANGELOG + README phase-1 summary updated; tag **not** pushed without explicit
approval).

## Key cross-cutting rules (brief v2)

- **Bi-temporality (patch 2):** default queries operate in `event_time` space (reality view). `asKnownAt` opt-in constrains to `recorded_at <= t` (audit view). Every event exposes both `eventTime` and `recordedAt`. Schema descriptions must teach the LLM which mode to pick.
- **Entity identity (patch 3):** two concepts — a **logical entity ID** (stable surrogate assigned on first sight, survives identity changes) and an **identity hash** (SHA-256 of current identifying attrs, used for lookup/idempotency). Change engine matches with tolerance and keeps the logical ID stable across `entity.identity_changed`. See ADR **0017**.
- **Retention (patch 4):** heartbeat (`entity.unchanged`) coalescing; `--retention-max-age` (default unlimited), `--retention-compaction-interval` (default 1h). Snapshots/archival = phase 2 but architecture must accommodate. ADR 0013. `docs/operations/storage-sizing.md`.
- **GraphQL limits (patch 5):** Relay cursor pagination on all list queries (`first`/`after`, `Connection`/`edges`/`pageInfo`/`totalCount`); `getNeighbors` maxDepth 5; query-complexity cap (default 1000); per-query timeout (default 10s); plain-language LLM-friendly limit errors; rich descriptions mandatory.
- **Performance (patch 6):** explicit phase-1 targets, `benchmark_test.go` per core package, `make bench`, `docs/operations/performance.md` updated each checkpoint. Reference HW: Linux x86_64, 8 cores / 16 GB.
- **Auth (patch 7):** none in phase 1; `--listen` defaults to `127.0.0.1`; README security disclaimer. ADR 0014.
- **OTel spec tracking (patch 8):** track the entities SIG, pin OTel libs, Toise `schema_version` independent of OTel spec version, migration at ingest boundary. ADR 0015, `docs/data-model/otel-mapping.md`.

## ADR ledger (phase 1, brief v2)

| ADR | Title | Milestone | Status |
|-----|-------|-----------|--------|
| 0003 | keeping-dependencies-current | M0 | written |
| 0004 | data-model-aligned-with-otel-entities | M1 | written |
| 0005 | bi-temporal-event-model (revised query semantics) | M1 | written |
| 0006 | change-taxonomy | M1 | written |
| 0007 | pebble-as-event-log-store | M2 | written |
| 0008 | in-memory-projection-from-event-log | M3 | written |
| 0009 | otlp-ingestion-via-entity-events | M4 | written |
| 0010 | graphql-as-primary-query-language | M5 | written |
| 0011 | mcp-server-design | M6 | written |
| 0012 | debug-ui-minimal-html | M7 | written |
| 0013 | event-log-retention-strategy | M2 | written |
| 0014 | no-authentication-in-phase-1 | M0 | written |
| 0015 | tracking-otel-entity-events-spec | M1 | written |
| 0016 | pebble-validation (PoC results) — *was patch-1 "0007", reassigned* | M0 | written |
| 0017 | entity-identity-and-stability — *was patch-3 "0006", reassigned* | M1 | written |

## Demo scenario (patch 9)

"A day in the life of web-server-1" — 24h simulated host evolution (discovery,
new container daemon, eth0 down, eth0 back with new IP/subnet, postgres starts,
default gateway change, nginx restart, container crash). ≥10 LLM example
prompts in `docs/demo/llm-prompts.md` covering current state, topology
traversal, recent changes, history, causal, anomaly, and `asKnownAt` audit
queries — each with expected MCP tool calls and answer shape.
