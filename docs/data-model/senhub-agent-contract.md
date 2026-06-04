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
| `process` | `{process.pid, process.creation.time}` — the OTel semconv identity (the creation time disambiguates PID reuse) | a restart = a new process (delete + create), not an update; `process.executable.name` is descriptive |
| `db` | `{db.instance.id}` — a **stable source identifier**: PostgreSQL `system_identifier`, MySQL `server_uuid`, else an operator-configured logical instance name | **never network-derived** — `server.address`/`server.port` are mutable (DHCP/failover/VIP) so they stay descriptive attributes |
| `network.device` | `{network.device.id}` — a single subtype-prefixed value by **precedence**: `serial:` (ENTITY-MIB `entPhysicalSerialNum`) > `engine:` (`snmpEngineID`) > `mac:` (LLDP chassis-id) > `name:` (`sysName`) > `mgmt:` (mgmt IP) | **anchored on SNMP-immutable facts, not LLDP** (often disabled); `mgmt:` is mutable last-resort. Producer canonicalizes (Toise is byte-exact); raw parts descriptive. Endpoints resolved to the canonical id via `ifPhysAddress` before emitting edges. Frozen for Lot 5 — see [`otel-mapping.md`](./otel-mapping.md#networkdevice-identity--snmp-topology-lot-5-frozen). |

### Time & liveness — explicit delete + interval backstop

`event_time` = LogRecord `Timestamp`; `recorded_at` stamped by Toise. Liveness uses
**both**: an explicit **`entity_delete`** as the primary signal **and**
`otel.entity.interval` (ms, sized with slack — the agent emits 3× its heartbeat
cadence) as a TTL backstop for missed deletes (`kill -9`, crash, host off, net
partition). Toise expires entities not re-asserted within their interval.

**Edge liveness is derived from endpoints (decided Q2 = Option A):** deleting an
entity (explicitly or by expiry) **cascades `relation.removed`** for its incident
edges, so the agent does **not** emit a relation interval — keep the endpoints
alive and the edges live with them. `relation_delete` retires an edge while both
endpoints live; an *optional* `entity.relation.interval` exists as a backstop for a
missed such delete, but the agent can ignore it.

**Multi-producer liveness — per-producer reference counting (Q1, done):** liveness
is reference-counted **per producer**, keyed by the Resource `service.instance.id`
(ADR 0019). Several agents on the same entity converge; an explicit `entity_delete`
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

### Migration to the embedded OTel standard (ADR 0022)

The OTel entity-events spec landed relationships **embedded** in entity-state events
(spec PR #4836), and Toise's engine is now defined as a **faithful, facts-only store
of that standard** (ADR 0022). The shapes above are **transitional**; the target the
producer migrates to:

- **Relations move from separate `entity.relation.*` records to embedded
  `entity.relationships`** on the source entity's state event — each descriptor a map
  `{ type, entity.type, entity.id }` naming the target. Removal is **by absence** (a
  relation the source stops listing is removed); no explicit relation-delete on the
  wire. Toise **ingests embedded today** (additive), and the extension keeps working
  through the transition, so the producer can move at its own pace.
- **Topology becomes entities; edges become bare.** A port is a `network.interface`
  entity (`{network.device.id, interface.name}`, with `speed`/`oper_state` as
  attributes), linked by `has_interface` (device→port); adjacency is a **bare
  `connected_to`** (port↔port), replacing `adjacent_to` + `{local_port, remote_port}`.
  A route's `metric` rides on the `network.route` entity, an address's `preferred` on
  `network.address`, and **provenance** (`source`) on the **instrumentation scope** —
  never on the edge.
- **Identity is unchanged** (`network.device.id` precedence `serial:<PEN>`/…, exact
  matching, per-producer liveness) — those are facts and stay.

The **conformance fixture now demonstrates this target model**. This contract resync
is tracked at #73. See ADR 0022 and
`docs/architecture/migration-embedded-relationships.md`.

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
| Entity interval TTL sweeper | **done** (`--liveness-sweep-interval`) |
| Edge liveness derived from endpoints (cascade) + optional per-edge TTL | **done** |
| Out-of-order edge reconciliation buffer | **done** (opt-in, `--relation-buffer-ttl`) |
| Explicit `Warn` on dropped nested value | **done** |
| Multi-producer liveness (per-producer ref-counting) | **done** (ADR 0019) |

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
