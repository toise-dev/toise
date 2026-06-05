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
attribute set, and an `event_time` — plus relationships **embedded** on those
entity-state events (the OTel standard). The exact wire mapping is in
[`otel-mapping.md`](./otel-mapping.md); the agreed shape and conventions are below.

## Producer ↔ consumer contract (converged 2026-06)

Negotiated against the implemented ingest boundary (`internal/ingest`). Both
sides implement the *same* wire shape. Round 2 incorporates the senhub-agent
feedback on #185; the guiding principle is **no silent loss or collision** for an
infrastructure source of truth.

### Nodes — OTel entity events (merged spec, 2026-06-04)

senhub-agent emits LogRecords whose **`EventName`** is **`entity.state`** (upsert)
or **`entity.delete`** (soft delete), per the merged OTel entity-events spec
(`specification/entities/entity-events.md`). The payload is in LogRecord attributes:
**`entity.type`**, **`entity.id`** (map, `map<string,string>` — keep identity values
as strings), **`entity.description`** (descriptive attribute map), and
**`entity.report.interval`** (seconds). Toise classifies a record by its EventName
— there is no payload event-type attribute and no scope flag.

### Relations — embedded on entity.state events (OTel standard)

Relationships ride **embedded** on the source entity's `entity.state` event per the
merged spec: an `entity.relationships` array, each descriptor a map
`{ relationship.type, entity.type, entity.id }` naming the **target** (the source is
the emitting entity). This is the **sole on-wire relationship form** — there is no
separate relation record and no edge-attribute channel (ADR 0022: the engine stores
the standard's facts, nothing else).

Removal is **by absence**: a relationship the source stops listing on its next
state event is removed — there is **no explicit relation-delete** on the wire. The
agent therefore emits each entity's *full current* relationship set every state
event (like its attribute set), never an incremental add/remove.

Endpoints resolve by **exact identity** against live entities; reference each
target by its current identity. Emit endpoint entities **before** the entity whose
state embeds the edge. Out-of-order edges are handled by a reconciliation buffer
rather than required ordering — see *Robustness*.

### Values — flat maps of scalars, no silent drop

`entity.id` and `entity.description` are OTLP maps. Toise keeps **scalar leaves**
(`string`/`int64`/`double`/`bool`) of the **top-level** map; nested values are
dropped. The agent **pre-flattens** with dotted keys (`server.address`,
`server.port`). The drop is **surfaced** (a `Warn` naming the dropped key), never
silent.

### Identity — exact match, immutable Id

**Matching is exact** on the producer-declared Id (OTel-aligned: Ids are
**immutable**). No fuzzy/tolerant matching, so two distinct entities are never
silently merged; a single unique key is valid. Never put a mutable value (pid,
leased IP) in the identity — those are descriptive attributes. Agreed identities:

| Entity | Identity (`entity.id`) | Notes |
| ------ | --------------------------- | ----- |
| `host` | `{host.id}` (machine-id) | `host.name` descriptive |
| `service.instance` | `{service.instance.id}` (agent key) | the agent |
| `process` | `{process.pid, process.creation.time}` — the OTel semconv identity (the creation time disambiguates PID reuse) | a restart = a new process (delete + create), not an update; `process.executable.name` is descriptive |
| `db` | `{db.instance.id}` — a **stable source identifier**: PostgreSQL `system_identifier`, MySQL `server_uuid`, else an operator-configured logical instance name | **never network-derived** — `server.address`/`server.port` are mutable (DHCP/failover/VIP) so they stay descriptive attributes |
| `network.device` | `{network.device.id}` — a single subtype-prefixed value by **precedence**: `serial:` (ENTITY-MIB `entPhysicalSerialNum`) > `engine:` (`snmpEngineID`) > `mac:` (LLDP chassis-id) > `name:` (`sysName`) > `mgmt:` (mgmt IP) | **anchored on SNMP-immutable facts, not LLDP** (often disabled); `mgmt:` is mutable last-resort. Producer canonicalizes (Toise is byte-exact); raw parts descriptive. Endpoints resolved to the canonical id via `ifPhysAddress` before emitting edges. Frozen for Lot 5 — see [`otel-mapping.md`](./otel-mapping.md#networkdevice-identity--snmp-topology-lot-5-frozen). |

### Time & liveness — explicit delete + interval backstop

`event_time` = LogRecord `Timestamp`; `recorded_at` stamped by Toise. Liveness uses
**both**: an explicit **`entity.delete`** as the primary signal **and**
`entity.report.interval` (seconds, sized with slack — the agent emits 3× its
heartbeat cadence) as a TTL backstop for missed deletes (`kill -9`, crash, host off, net
partition). Toise expires entities not re-asserted within their interval.

**Edge liveness is derived from endpoints (decided Q2 = Option A):** deleting an
entity (explicitly or by expiry) **cascades `relation.removed`** for its incident
edges, so the agent does **not** track edge liveness separately — keep the
endpoints alive and the edges live with them. To retire an edge while both
endpoints stay live (e.g. the agent stops monitoring a still-running db), the agent
**drops that descriptor** from the source's next state event; the reconciler removes
it by absence. A *missed* such removal is covered by the **source entity's own
`entity.report.interval`** — no separate per-edge delete or interval exists.

**Multi-producer liveness — per-producer reference counting (Q1, done):** liveness
is reference-counted **per producer**, keyed by the Resource `service.instance.id`
(ADR 0019). Several agents on the same entity converge; an explicit `entity.delete`
(or an interval lapse) by one is a **silent release** — the entity stays live while
any other producer references it, and is deleted only when the **last** reference
goes. No flap, and no producer change beyond keeping `service.instance.id` on the
Resource (the agent already does). An observation with no producer counts as one
anonymous producer.

**Shared-entity attribute rule (design note, agreed):** a shared entity carries only
**observer-independent** attributes (system name, version…). Anything per-observer
("this agent monitors this db") is a distinct **`monitors` relation** per agent,
never an entity attribute (which would flap last-writer-wins).

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
- **Lot 4 (host routing/ARP):** **host-sourced** topology edges from the host's own
  tables — `host --routes_via--> <gateway network.device>` and
  `host --adjacent_to--> <neighbor>`. Reuses the frozen relation types with a `host`
  source (endpoints advisory, not enforced); **no new relation type, no host↔device
  identity merge** (§6b deferred). Device endpoints resolve to the canonical id where
  known, else provisional `mac:`/`mgmt:`. ARP is high-cardinality — filter to
  infrastructure/known devices, do not emit `adjacent_to` for every ARP peer.
- **Lot 5 (SNMP):** `network.device` and `routes_via`/`forwards_to`/`adjacent_to`.
  `network.device.id` and the relation shapes are **frozen** (precedence ladder +
  canonicalization above); rollout 5a LLDP → 5b routing → 5c FDB → 5d ARP, with
  identity anchored on SNMP (serial/engine) so it does not depend on LLDP. The serial
  tier is vendor-namespaced `serial:<PEN>:<n>` (PEN from `sysObjectID`) and fires only
  with a single chassis + PEN; cross-vendor collisions and stacks (>1
  `entPhysicalClass=3`) both fall back to the globally-unique `engine:<engineID>`
  (see `otel-mapping.md`).

### Topology as entities — edges stay bare (ADR 0022)

Toise's engine is a **faithful, facts-only store** of the OTel entity-events
standard (ADR 0022), and the embedded relationship model carries **no edge
attributes**. So anything a producer would have hung on an edge becomes an
**entity** instead:

- **Ports are entities.** A port is a `network.interface` entity
  (`{network.device.id, interface.name}`, with `speed`/`oper_state` as attributes),
  linked by `has_interface` (device→port); physical adjacency is a **bare
  `connected_to`** (port↔port) — never `adjacent_to` carrying `{local_port,
  remote_port}`.
- **Attribute-bearing facts move onto their entity.** A route's `metric` rides on the
  `network.route` entity, an address's `preferred` on `network.address`, and
  **provenance** (`source`/how an edge was observed) on the **instrumentation
  scope** — never on the edge.
- **Identity is unchanged** (`network.device.id` precedence `serial:<PEN>`/…, exact
  matching, per-producer liveness) — those are facts and stay.

The **conformance fixture demonstrates this model** (port entities + embedded
`has_interface`/`connected_to`). The producer-side resync is tracked at
[senhub-agent #222](https://github.com/senhub-io/senhub-agent/issues/222); the
Toise-side contract is tracked at #73. See ADR 0022 and
`docs/architecture/migration-embedded-relationships.md`.

### Planning & status

`#185` is **non-blocking** for Toise phase 1 (shipped with a synthetic producer,
feature-complete). The real senhub-agent producer is a phase-2 integration. The
wire shape and vocabulary are stable to build against now. Toise-side work items
from this round:

| Item | Status |
| ---- | ------ |
| Embedded `entity.relationships` ingestion (sole on-wire form) | **done** (#74) |
| Producer vocabulary in the registry | **done** (PR #16) |
| Conformance fixture / contract test | **done** (embedded-only, #74) |
| Exact-Id matching (retire fuzzy `identity_changed`) | **done** (ADR 0018) |
| Entity interval TTL sweeper | **done** (`--liveness-sweep-interval`) |
| Edge liveness derived from endpoints (cascade) + removal-by-absence | **done** |
| Out-of-order edge reconciliation buffer | **done** (opt-in, `--relation-buffer-ttl`) |
| Explicit `Warn` on dropped nested value | **done** |
| Multi-producer liveness (per-producer ref-counting) | **done** (ADR 0019) |

## Follow-up

- **senhub-agent #185** implements the emitter against the converged contract:
  standard OTel nodes with **embedded `entity.relationships`** (the sole edge form,
  per [#222](https://github.com/senhub-io/senhub-agent/issues/222)). Emit to
  reproduce the shared conformance fixture
  (`internal/ingest/testdata/conformance/entity-events.json`).
- **Toise (this repo)** has landed exact-Id matching (ADR 0018) and ships the
  remaining accepted items from the *Planning & status* table — the interval TTL
  sweeper, the edge reconciliation buffer, and the explicit drop warning — as the
  phase-2 first-real-producer hardening. The wire shape and vocabulary are already
  in place to build against.
