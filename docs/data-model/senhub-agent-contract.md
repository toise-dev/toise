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

## Producer ↔ consumer contract (agreed 2026-06-01)

Negotiated against the implemented ingest boundary (`internal/ingest`). Both
sides implement the *same* wire shape so the contract is symmetric.

### Nodes — standard OTel entity events

senhub-agent emits **`entity_state` / `entity_delete`** LogRecords using the
standard `otel.entity.*` attributes (`type`, `id` map, `attributes` map; event
type in `otel.entity.event.type`). Toise classifies a record by the **presence of
`otel.entity.event.type`** and does **not** require the `otel.entity.entity_event`
scope flag — the agent may emit it for spec fidelity; Toise ignores it. We never
bend `otel.entity.*` to carry anything non-standard.

### Relations — the `toise.relation.*` extension (not `senhub.*`)

OTel does not model entity relationships yet (OTEP 0256 *Future Work*). Edges are
emitted via a clearly non-standard extension that never poses as standard OTel.

**Decision:** the on-the-wire relation keys are **`toise.relation.*`**, *not*
`senhub.relationship.*`. Rationale: the relation format is the **consumer's**
contract and must be **producer-agnostic** — senhub-agent is one producer among
potentially many feeding the same boundary, so the keys belong to the format
Toise parses, not to a single producer. (senhub-agent may keep a `senhub.*`
concept internally, but what crosses the wire is `toise.relation.*`.) Each edge is
a `relation_state` / `relation_delete` LogRecord carrying `toise.relation.type`,
`toise.relation.from.{type,id}`, `toise.relation.to.{type,id}`, and optional
`toise.relation.attributes`. Both sides commit to **migrating to the OTel standard
together** once relationships land in the spec.

**Endpoints resolve by exact identity against live entities:** emit the endpoint
entities **before** the relation that connects them, and reference each endpoint
by its **current** identity (after an identity change, use the new values).

### Values — flat maps of scalars

`id` and `attributes` are OTLP maps. Toise keeps only **scalar leaves**
(`string`/`int64`/`double`/`bool`) of the **top-level** map; nested values are
**dropped, not flattened**. The agent must **pre-flatten** with dotted keys
(`server.address`, `server.port`), not nest sub-maps.

### Time & liveness

`event_time` = the LogRecord `Timestamp` (producer/reality); `recorded_at` is
stamped by Toise at ingest and never taken from the producer. **Liveness is
event-driven, not interval-driven:** `otel.entity.interval` is not consumed; the
agent must send an explicit **`entity_delete`** when an entity disappears, and a
periodic re-emitted `entity_state` serves as a heartbeat (`entity.unchanged`).

### Identity conventions & the matching constraint

Immutable identities, descriptive everything-else. Agreed starting set:

| Entity | Identity (`otel.entity.id`) | Notes |
| ------ | --------------------------- | ----- |
| `host` | `{host.id}` (machine-id) | `host.name` is descriptive |
| `service.instance` | `{service.instance.id}` | = agent key |
| `db` | a **single composite key** (e.g. `{db.instance.id}` synthesised from system+address+port) | see constraint |
| `network.device` | `{<stable device id>}` (e.g. `host.id`-scoped asset id) | discovered network asset |

**Constraint (important):** Toise matches identity *tolerantly* — two entities
that differ in **exactly one** identifying value are treated as the **same** one
that changed identity (ADR 0017). So distinct instances must differ in **≥2**
identifying values **or** use a **single composite key**. This is why `db` should
*not* use `{db.system.name, server.address, server.port}` directly (two DBs on one
host differing only by port would merge): prefer a single composite `db.instance.id`.

### Type vocabulary — registry coordination

`host` is already in Toise's registry. **`service.instance`, `db`,
`network.device`, and relations `monitors` / `adjacent_to` / `routes_via` /
`forwards_to` are not yet registered and would be rejected** at the boundary.
Adding them (entity types; relation types with their endpoint-type constraints
and structural flags) is a small, non-breaking **Toise registry change**, and is
the explicit coordination point: the agent and Toise agree the vocabulary, Toise
lands the registry extension, then the agent emits.

### Planning

`#185` is **non-blocking** for Toise phase 1, which shipped using a synthetic OTel
SDK client as the reference producer (M4) and is feature-complete. The **real
senhub-agent producer is a phase-2 integration** ("first real producer"); the wire
contract above is stable to build against now, with the registry-vocabulary
extension as the one Toise-side prerequisite.

## Follow-up

- **senhub-agent #185** implements the emitter against the contract above; the
  node side is standard OTel and unblocked today, the relation side uses
  `toise.relation.*`.
- **Toise (this repo)** lands the **type-registry extension** for the agreed
  producer vocabulary (`service.instance`, `db`, `network.device`; relations
  `monitors`, `adjacent_to`, `routes_via`, `forwards_to`) — the one Toise-side
  prerequisite — as part of the phase-2 first-real-producer integration. Until
  then, only the phase-1 registry types are accepted.
