# senhub-agent ↔ Toise contract

Originally a Milestone 0 capability check (2026-05-29); extended 2026-06-01 with
the **agreed producer↔consumer contract** negotiated against the now-implemented
ingest boundary, for the senhub-agent entity-event emitter
([senhub-io/senhub-agent#185](https://github.com/senhub-io/senhub-agent/issues/185)).
The exact wire shape lives in [`otel-mapping.md`](./otel-mapping.md); this file
records the decisions and the producer-side conventions.

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

To become a Toise producer, senhub-agent needs an entity-event emitter that maps
its collected inventory/topology into OTLP LogRecords following the OTel Entity
Data Model — per entity a `type`, an identifying attribute set, a descriptive
attribute set, and an `event_time` — plus relation edges via the Toise extension.
The exact wire mapping is in [`otel-mapping.md`](./otel-mapping.md); the agreed
shape and conventions are below.

## Producer ↔ consumer contract (converged 2026-06)

Negotiated against the implemented ingest boundary (`internal/ingest`). Both
sides implement the *same* wire shape. Round 2 incorporates the senhub-agent
feedback on #185; the guiding principle is **no silent loss or collision** for an
infrastructure source of truth.

### Nodes — standard OTel entity events

senhub-agent emits **`entity_state` / `entity_delete`** LogRecords with the
standard `otel.entity.*` attributes (`type`, `id` map, `attributes` map; lifecycle
in `otel.entity.event.type`). Toise classifies a record by the **presence of
`otel.entity.event.type`**. The `otel.entity.entity_event=true` scope flag is
**accepted and ignored** — never required, never rejected (it is an interop
fast-path for other OTel producers/collectors). `otel.entity.*` is never bent to
carry non-standard data.

### Relations — the vendor-neutral `entity.relation.*` extension

OTel does not model relationships yet (OTEP 0256 *Future Work*, which cites exactly
`process runs_on host`). Edges use a **vendor-neutral** extension — neither
`toise.*` (consumer) nor `senhub.*` (producer) — chosen so any producer/consumer
can speak it and it maps **1:1 onto the future OTel standard**, making migration
trivial for everyone. `otel.entity.relationship.*` is deliberately avoided (it
would squat the reserved OTel namespace before the spec exists).

**Strict purity:** a relation record carries **no `otel.entity.*` attribute** —
its lifecycle is `entity.relation.event.type` ∈ {`state`, `delete`}, plus
`entity.relation.type`, `entity.relation.from.{type,id}`,
`entity.relation.to.{type,id}`, and optional `entity.relation.attributes`. A
relation thus never looks like a malformed entity event to a standard OTel
consumer (which keys off `otel.entity.*`); it is cleanly ignored there. Both sides
commit to migrating to the OTel standard once it lands.

Endpoints resolve by **exact identity** against live entities; reference each by
its current identity. Emit endpoints **before** their edge. Out-of-order edges are
handled by a reconciliation buffer rather than required ordering — see *Robustness*.

### Values — flat maps of scalars, no silent drop

`id` and `attributes` are OTLP maps. Toise keeps **scalar leaves**
(`string`/`int64`/`double`/`bool`) of the **top-level** map; nested values are
dropped. The agent **pre-flattens** with dotted keys (`server.address`,
`server.port`). The drop is **surfaced** (a `Warn` naming the dropped key), never
silent.

### Identity — exact match, immutable Id

**Matching is exact** on the producer-declared Id (OTel-aligned: Ids are
**immutable**). No fuzzy/tolerant matching, so two distinct entities are never
silently merged; a single unique key is valid. Never put a mutable value (pid,
leased IP) in the identity — those are descriptive attributes. Agreed identities:

| Entity | Identity (`otel.entity.id`) | Notes |
| ------ | --------------------------- | ----- |
| `host` | `{host.id}` (machine-id) | `host.name` descriptive |
| `service.instance` | `{service.instance.id}` (agent key) | the agent |
| `db` | `{db.instance.id}` — single composite key (system+address+port) | a clean immutable key, by choice |
| `network.device` | `{net.device.id}` = LLDP chassis-id (`lldpLoc/RemChassisId`), fallback management IP | frozen at the SNMP collection lot; not emitted before then |

### Time & liveness — explicit delete + interval backstop

`event_time` = LogRecord `Timestamp`; `recorded_at` stamped by Toise. Liveness uses
**both**: an explicit **`entity_delete`** as the primary signal **and** the OTel
`interval` as a TTL backstop (for missed deletes — `kill -9`, crash, host off, net
partition). The agent emits both; Toise expires entities not re-asserted within
`last_seen + interval + margin`. Same for edges (`relation_delete` + TTL).

### Vocabulary & rollout lots

The registry already holds the agreed vocabulary (Toise PR #16): entities
`service.instance`, `db`, `network.device`; relations `monitors`, `routes_via`,
`forwards_to`, `adjacent_to`. **`runs_on`** (already registered) is the foundational
edge: `service.instance --runs_on--> host`. `monitors` source is the
`service.instance` (the agent); targets are the monitored entity — `host`, `db`,
and later `netscaler`, `veeam`, `redfish`, `citrix`, `ibmi`, `network.device`
(those further types are registered when their collection lot lands). Rollout:

- **Lot 1:** entities `host` + `service.instance`; relation `runs_on`.
- **Lot 2:** monitored systems (`db` first); relation `monitors`.
- **Lot 5 (SNMP):** `network.device` and `routes_via`/`forwards_to`/`adjacent_to`.

### Planning & status

`#185` is **non-blocking** for Toise phase 1 (shipped with a synthetic producer,
feature-complete). The real senhub-agent producer is a phase-2 integration. The
wire shape and vocabulary are stable to build against now. Toise-side work items
from this round:

| Item | Status |
| ---- | ------ |
| Vendor-neutral `entity.relation.*` keys | **done** (PR #17) |
| Producer vocabulary in the registry | **done** (PR #16) |
| Conformance fixture / contract test | **done** (PR #17) |
| Exact-Id matching (retire fuzzy `identity_changed`) | **done** (ADR 0018) |
| Interval TTL sweeper (entity + edge expiry) | accepted — pending |
| Out-of-order edge reconciliation buffer | accepted — pending |
| Explicit `Warn` on dropped nested value | accepted — pending |

## Follow-up

- **senhub-agent #185** implements the emitter against the converged contract:
  standard OTel nodes (unblocked today) and `entity.relation.*` edges. Emit to
  reproduce the shared conformance fixture
  (`internal/ingest/testdata/conformance/entity-events.json`).
- **Toise (this repo)** has landed exact-Id matching (ADR 0018) and ships the
  remaining accepted items from the *Planning & status* table — the interval TTL
  sweeper, the edge reconciliation buffer, and the explicit drop warning — as the
  phase-2 first-real-producer hardening. The wire shape and vocabulary are already
  in place to build against.
