# Phase 1 — Progress

Tracking the milestones of the Toise phase 1 backend (brief v2). Each milestone
ends at a checkpoint where work stops for maintainer validation before
continuing. The development brief itself is the session source of truth; this
file is the actionable tracker reflecting brief v2 (patches 1–9 applied).

Status legend: `not started` · `in progress` · `awaiting checkpoint` · `done`

| # | Milestone | Checkpoint | Status |
|---|-----------|------------|--------|
| 0 | Prerequisites validation | 0 | awaiting checkpoint |
| 1 | Data model and proto contract | A | not started |
| 2 | Event log store on Pebble (+ retention) | B | not started |
| 3 | Projection engine and change detection | C | not started |
| 4 | OTLP ingestion receiver | D | not started |
| 5 | GraphQL API (+ pagination/limits) | E | not started |
| 6 | MCP server | F | not started |
| 7 | Debug UI | G | not started |
| 8 | Temporal fixtures and demo scenario | H | not started |

## Milestone 0 — Prerequisites (brief v2, patch 1)

- [x] Go version check — latest stable go1.26.3; `go.mod` `go 1.26` (latest minor; CI pulls latest patch). `go build ./...` OK.
- [x] Repository inventory — bootstrap present (LICENSE, README, Makefile, CI, ADR 0001–0002, golangci).
- [x] Pebble proof of concept — `scratch/pebble-poc/` (isolated module, Pebble v1.1.5): 10k events, write ~20.9k evts/s, scan ~8.6M evts/s, 1.68 MB on disk. Batch p99 15.1 ms (over 10 ms target, but macOS dev HW + per-batch fsync — re-measure on Linux ref). → ADR **0016**.
- [x] senhub-agent integration check — `senhub-io/senhub-agent` (private) speaks OTLP/OTel + LogRecords but emits **no entity events** yet ("not yet" case). Non-blocking; M4 uses a synthetic OTel SDK client. → `docs/data-model/senhub-agent-contract.md`. Issue in senhub-agent repo drafted, pending go-ahead.
- [x] MCP Go SDK availability — **official `github.com/modelcontextprotocol/go-sdk` is GA (v1.6.1)** and will be used at M6 (no hand-rolled protocol). `…/sdk-go` (brief guess) does not exist; `mark3labs/mcp-go` (community) exists but the official SDK is preferred. To be recorded in ADR 0011 (M6).
- [x] ADR 0003 (deps strategy), ADR 0014 (no auth), ADR 0016 (pebble validation) written.

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
| 0003 | keeping-dependencies-current | M0 | planned |
| 0004 | data-model-aligned-with-otel-entities | M1 | planned |
| 0005 | bi-temporal-event-model (revised query semantics) | M1 | planned |
| 0006 | change-taxonomy | M1 | planned |
| 0007 | pebble-as-event-log-store | M2 | planned |
| 0008 | in-memory-projection-from-event-log | M3 | planned |
| 0009 | otlp-ingestion-via-entity-events | M4 | planned |
| 0010 | graphql-as-primary-query-language | M5 | planned |
| 0011 | mcp-server-design | M6 | planned |
| 0012 | debug-ui-minimal-html | M7 | planned |
| 0013 | event-log-retention-strategy | M2 | planned |
| 0014 | no-authentication-in-phase-1 | M0 | planned |
| 0015 | tracking-otel-entity-events-spec | M1 | planned |
| 0016 | pebble-validation (PoC results) — *was patch-1 "0007", reassigned* | M0 | planned |
| 0017 | entity-identity-and-stability — *was patch-3 "0006", reassigned* | M1 | planned |

## Demo scenario (patch 9)

"A day in the life of web-server-1" — 24h simulated host evolution (discovery,
new container daemon, eth0 down, eth0 back with new IP/subnet, postgres starts,
default gateway change, nginx restart, container crash). ≥10 LLM example
prompts in `docs/demo/llm-prompts.md` covering current state, topology
traversal, recent changes, history, causal, anomaly, and `asKnownAt` audit
queries — each with expected MCP tool calls and answer shape.
