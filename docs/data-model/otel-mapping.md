# OpenTelemetry entity-events mapping

This document records the mapping between the
[OpenTelemetry Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
/ entity-events specification and the Toise schema. It is maintained per
[ADR 0015](../architecture/adr/0015-tracking-otel-entity-events-spec.md) and kept
up to date as either side changes.

## How OTel entity events reach Toise

OTel entity events are carried as **OTLP `LogRecord`s** annotated with the entity
semantic conventions. Toise ingests them at the **OTLP boundary** in
**Milestone 4** (`internal/ingest`): the boundary is the single place where the
OTel wire shape is translated into the internal Toise event model.

**Transport.** OTLP/gRPC over the logs service (`127.0.0.1:4317` by default).
Toise accepts uncompressed **and gzip-compressed** exports — gzip is the OTel SDK
default and what senhub-agent ships, so it works out of the box with no
`compression:` override required on the producer.

## Wire shape: the exact LogRecord attributes Toise reads

The ingest boundary classifies each `LogRecord` by its **`EventName`**: a record
whose EventName is `entity.state` or `entity.delete` is an **entity event**, any
other record is ignored (treated as an ordinary log). This follows the merged OTel
entity-events spec (`specification/entities/entity-events.md`, **merged 2026-06-04**),
which models entity events as Logs-Data-Model **Events** keyed by `EventName` — not
by a payload attribute. **Relationships are not separate records** — they ride
**embedded** on `entity.state` events (see below).

### Entity events (OTel entity-events convention)

The event is identified by the LogRecord **`EventName`** (`entity.state` /
`entity.delete`); the rest is carried in LogRecord attributes:

| Carrier                    | Type    | Required | Meaning                                                |
| -------------------------- | ------- | -------- | ------------------------------------------------------ |
| `EventName` (LogRecord)    | string  | yes      | `entity.state` (upsert) or `entity.delete` (soft delete) |
| `entity.type`              | string  | yes      | the entity type — **must be in Toise's type registry** |
| `entity.id`                | **map** | yes      | identifying attributes (the entity's identity); spec type `map<string,string>` |
| `entity.description`       | **map** | no       | descriptive, non-identifying attributes                |
| `entity.report.interval`   | int     | no       | heartbeat cadence in **seconds**; arms the liveness backstop (see *Entity liveness*) |
| `LogRecord.Timestamp`      | —       | yes      | becomes `event_time` (falls back to `ObservedTimestamp`, then ingest time) |

Notes:

- `entity.id` and `entity.description` are genuine OTLP **maps**
  (`AnyValue` kvlist), parsed structurally — see *AnyValue restriction* below. The
  spec types `entity.id` as `map<string,string>`; Toise's parser is more permissive
  (it accepts typed scalars) but producers should keep identity values as strings.
- The OTLP **Resource** `service.instance.id` identifies the producing agent and
  keys per-producer liveness reference counting (ADR 0019; see *Entity liveness*).
  Producers should set it on every export.
- A `schema_url` is part of the model but is **not currently read** at the OTLP
  boundary; entities ingested via OTLP carry an empty schema URL for now.

### Relationships — embedded on entity-state events (OTel standard)

Relationships are carried **embedded** in an `entity.state` event, per the merged
OTel entity-events spec (`specification/entities/entity-events.md`): an
`entity.relationships` array on the source entity, each descriptor a map naming the
**target** (the source is the emitting entity). This is the **sole on-wire
relationship form** — there is no separate relation record and no edge-attribute
channel ([ADR 0022](../architecture/adr/0022-engine-stores-facts-only.md): the
engine stores the standard's facts, nothing else).

| `entity.relationships[]` descriptor field | Type    | Required | Meaning                                             |
| ----------------------------------------- | ------- | -------- | --------------------------------------------------- |
| `relationship.type`                       | string  | yes      | the relation type — **must be in Toise's registry** |
| `entity.type`                             | string  | yes      | the **target** endpoint entity type                 |
| `entity.id`                               | **map** | yes      | the **target** endpoint identity                    |

The ingest boundary parses `entity.relationships` on each entity-state event and
translates each descriptor into the engine's first-class relation event (`from` =
the emitting entity, `to` = the descriptor target), **reconciling per source**: new
descriptors are observed, and a descriptor the source **stops listing** is
**removed by absence** — there is no explicit relation-delete on the wire. The
per-source bookkeeping is in-memory; after a restart the first re-emit
re-establishes the set, and the interval liveness backstop covers a producer that
vanishes (see *Edge liveness*).

**No edge attributes.** A relationship descriptor carries only `relationship.type` + target;
anything that wants to describe *how* two things relate becomes an **entity** (a
port is a `network.interface`, a route is a `network.route`) or rides on the
instrumentation scope (provenance) — never on the edge. See *topology as entities*
below.

**Endpoint resolution is by exact identity**, against a **live** entity by
`(type, identity)`. The descriptor's `entity.id` must be the target's current
identity. Producers should emit the endpoint `entity.state` events **before** the
entity whose state embeds the edge, but ordering is **not required**: with the
**reconciliation buffer** enabled (`--relation-buffer-ttl`, on by default in
`toise-server`), an edge whose endpoint is not yet present is **parked** and
retried as later entities arrive, and dropped with a `Warn` only if its endpoints
have not appeared within the TTL — so out-of-order delivery (OTLP guarantees no
inter-batch order) never silently loses edges. With the buffer disabled, a missing
endpoint is a retriable ingest error instead. See *Robustness backstops* below.

## Mapping table

| OTel concept                              | Toise field                                                                 |
| ----------------------------------------- | --------------------------------------------------------------------------- |
| Entity type                               | `Entity.type`                                                               |
| Entity identifying attributes             | `Entity.identity`                                                           |
| Entity descriptive attributes            | `Entity.attributes`                                                          |
| Entity `schema_url`                       | `Entity.schema_url`                                                          |
| `LogRecord` timestamp / observed timestamp | `event_time` (producer/reality)                                            |
| (ingestion)                               | `recorded_at` — set by Toise at ingest, never taken from the producer       |
| OTel `AnyValue`                           | Toise typed `Value` (`string` / `int64` / `double` / `bool`)                |

### AnyValue restriction

Toise's typed `Value` is a deliberate subset of OTel `AnyValue`. Only the four
scalar kinds — `string`, `int64`, `double`, `bool` — are supported in phase 1.

The `entity.id` / `entity.description` (and relation-endpoint id) attributes are
themselves **maps**, and Toise reads them structurally: it iterates the **top-level**
map and keeps each entry whose **leaf value is one of the four scalars**. A
non-scalar leaf (a nested `kvlist`/`array`/`bytes`) is dropped — the boundary does
**not** recurse into nested structures.

The practical contract for producers: the `id` and `attributes` maps must be
**flat maps of scalars**. Pre-flatten any structure with dotted keys before
emitting — send `{"server.address": "10.0.0.1", "server.port": 5432}`, never
`{"server": {"address": "10.0.0.1", "port": 5432}}` (the nested `server` value
would be discarded).

**No silent loss:** dropping a nested value is **surfaced**, not silent — the
boundary logs a `Warn` naming the dropped key(s) (e.g.
`entity.description.foo`) so the loss is observable. Pre-flattening remains
the producer's contract; the consumer never drops data quietly.

### Entity liveness — explicit delete primary, interval backstop

Liveness uses **two mechanisms, not one**:

1. **Explicit `entity.delete` is the primary signal.** When a producer knows an
   entity is gone, it emits `entity.delete` and Toise soft-deletes it. A heartbeat
   is a re-emitted `entity.state` (unchanged → `entity.unchanged`, coalesced under
   retention, ADR 0013).
2. **`entity.report.interval` is a backstop.** A producer often *misses* the clean
   delete — `kill -9`, crash, host powered off, network partition — so relying on
   the explicit delete alone accumulates zombie entities. OTel's `Interval` exists
   exactly for this ("resilient to event losses"). An entity observed with an
   `entity.report.interval` (heartbeat cadence, in **seconds**) is armed with a deadline; a
   periodic sweeper (`toise-server --liveness-sweep-interval`, default 30s) expires
   it with `entity.deleted` if it is not re-asserted within the interval. The
   producer should **size the interval to include slack** (e.g. a few heartbeats)
   so a single late heartbeat does not expire a live entity.

Producers emit **both**: the explicit delete *and* the interval. Only entities that
carry an interval are ever expired, so the sweeper is safe to leave on.

#### Edge liveness — derived from endpoints (primary), per-edge TTL (optional)

An edge to a deleted node is meaningless, so edge liveness is **primarily derived
from its endpoints**: when an entity is deleted — explicitly or by expiry — Toise
**cascades `relation.removed`** for every incident edge. A producer therefore does
**not** need to track or expire edges separately: keep the endpoints alive
(heartbeat + interval) and the edges live with them; let a node die and its edges
die with it.

To retire an edge **while both endpoints stay live** (e.g. an agent stops
monitoring a still-running db), the producer simply **stops listing that
descriptor** on the source's next state event: the reconciler removes it by
absence. Because the edge rides on the source entity, a *missed* removal is covered
by the **source's own `entity.report.interval`** — when the producer vanishes, the
source expires and its incident edges cascade out. There is no separate per-edge
delete or per-edge interval; the source entity's liveness is the edge's liveness.

#### Multi-producer liveness — per-producer reference counting

Liveness is **reference-counted per producer** (ADR 0019). Toise records, per
entity, the set of producers asserting it (keyed by the OTLP **Resource
`service.instance.id`**, which producers already set), each with its own interval
deadline. An entity is **live while any producer references it**; it is deleted
only when the **last** reference is released — by that producer's explicit
`entity.delete` or by its interval lapsing. So an explicit delete (or a crash) by
one of several agents observing the same entity no longer flaps it: a `delete`
from one producer is a silent release while others still observe.

This needs no producer change beyond keeping `service.instance.id` on the
Resource. An observation with no producer is treated as a single anonymous
producer, so single-producer deployments behave exactly as before. (Two distinct
producers both omitting `service.instance.id` would collapse into one anonymous
reference — a misconfiguration.)

**Corollary — shared entities carry only observer-independent attributes.** Because
`entity.state` is full-state and identity is shared across producers, a shared
entity must only carry attributes that are **independent of the observer** (system
name, version, …). Anything *per-observer* ("**this** agent monitors this db") is a
distinct **`monitors` relation** per agent — never an entity attribute, which would
flap under last-writer-wins as different agents assert different values.

### Identity matching — exact (immutable Id)

Entity identity is matched **exactly** against the producer-declared Id, in line
with the OTel model where an entity's Id is **immutable** (ADR 0018, superseding
the tolerant matching of ADR 0017).

Background: phase-1 Toise matched *tolerantly* (an observation differing in at
most one identifying value was treated as the same entity that "changed identity",
ADR 0017). That conflates two cases the data cannot distinguish — "same entity,
attribute changed" vs "a different, similar entity" — and causes **silent
over-merges** (two databases on a host differing only by port; two NICs; two jobs
collapse into one). For an infrastructure source of truth that is the wrong
trade-off.

Under exact matching:

- An entity keeps its Id; descriptive attributes change → `entity.attribute_updated`
  / `state_changed` (already exact-Id matched).
- The Id changes → a *different* entity (delete-and-create from the consumer's
  view). Fuzzy "identity changed" detection is dropped; if continuity across an Id
  change is ever needed, the producer signals it explicitly.
- A single-key identity (`host.id`, the agent key) is **valid again** — no need for
  the "≥2 values / composite key" rule.

The corollary for producers: **Ids must be immutable.** A *bare* reused value is not
an identity on its own, but becomes one when paired with a discriminator stable for
the resource's lifetime — the OTel semconv `process` identity is **`process.pid` +
`process.creation.time`** (the creation time disambiguates PID reuse), so a restart
is a new process, not a mutated one, and `process.executable.name` stays descriptive.
Where no such discriminator exists the value stays descriptive: a leased IP / **network
address** is never identifying, and in particular a `db` identity must be a **stable
source identifier** (PostgreSQL `system_identifier`, MySQL `server_uuid`, or an
operator-configured logical instance name), **not** a network-derived composite like
`host:port` — the address moves under DHCP/failover/VIP, which would make the instance
look like a brand-new entity and orphan its edges. `server.address` / `server.port` /
`db.system.name` stay descriptive attributes.

*(Implemented: ADR 0017 is superseded by ADR 0018; the change engine matches
exactly and no longer emits `entity.identity_changed`, though the type is retained
in the taxonomy for replay/wire compatibility.)*

#### `network.device` identity — SNMP topology (Lot 5, frozen)

A discovered network asset is keyed by the single identity key
**`network.device.id`**, whose value is **subtype-prefixed** and chosen by the
producer from a fixed **precedence ladder** — highest available tier wins:

| Tier | Value | Source | Why |
|------|-------|--------|-----|
| 1 | `serial:<PEN>:<n>` | ENTITY-MIB `entPhysicalSerialNum`, namespaced by the IANA PEN from `sysObjectID` | immutable hardware id, vendor-namespaced; **only when a single chassis and a PEN are present** |
| 2 | `engine:<hex>` | `snmpEngineID` | globally unique by construction (RFC 3411); the robust fallback — covers no-PEN **and stacks** (one engine id stack-wide) |
| 3 | `mac:<addr>` | LLDP chassis-id (subtype 4) | strong, but **LLDP is often disabled** |
| 4 | `name:<n>` | `sysName` | may be unset / non-unique |
| 5 | `mgmt:<ip>` | the polled management address | last resort — **mutable**, so weakest |

Identity is **anchored on SNMP-immutable facts (serial/engine), not on LLDP**:
LLDP is frequently off, and `mgmt:<ip>` is network-derived (the `db` anti-pattern).
The raw parts (`sysName`, management IP, vendor, model) ride as **descriptive
attributes**, never as identity.

**Values are opaque pass-through tokens** — Toise never parses or validates them.
There is deliberately **no standard for the serial's content**: `entPhysicalSerialNum`
is a *vendor-specific* `SnmpAdminString` (ENTITY-MIB, RFC 6933) and may be empty —
which is exactly why the ladder degrades. What is frozen is the **wrapping** (the
prefix + the canonicalization below), not the content; observer-independence comes
from *same OID, same device, same bytes*, not from a value format (the same is true
of `host.id`, `db.instance.id`, etc.). Two `serial:` identity-*scope* hazards are
handled by the top two tiers — neither is a format question:

- **Cross-vendor uniqueness.** Serials are unique *per vendor*, not globally, so the
  serial tier is **vendor-namespaced**: `serial:<PEN>:<n>`, where `<PEN>` is the IANA
  Private Enterprise Number — the segment after `1.3.6.1.4.1.` in `sysObjectID`. The
  serial tier fires **only when a PEN is readable**; otherwise it drops to `engine:`,
  which is globally unique by construction (RFC 3411 embeds the PEN + vendor-unique
  bytes) and so needs no namespace.
- **Stacked-device scope.** The logical `network.device` is an **SNMP management
  entity**. A stack — detected as **more than one `entPhysicalClass=3` chassis row**
  in `entPhysicalTable` — is keyed by its single stack-wide `engine:<engineID>`,
  **not** a per-member serial (a designated master would flip at failover and break
  immutability). The physical members surface later as sub-components (entPhysical
  inventory) and/or a `member` relation, never as the head node.

**Canonicalization is the producer's responsibility** — Toise matches the id string
byte-for-byte and never normalizes, so two agents must render identically:

| Prefix | Canonical form |
|--------|----------------|
| `mac:` | lowercase hex, colon-separated per octet (`00:1a:2b:3c:4d:5e`) |
| `engine:` | lowercase hex, no separator |
| `serial:` / `name:` | `TrimSpace`, case preserved (they are identifiers) |
| `mgmt:` | `net.IP.String()` form (IPv4 no leading zeros; IPv6 RFC 5952 lowercased) |

**Resolve to the canonical id before emitting edges.** Because the agent reads each
polled device's interface MAC table (`ifPhysAddress`), an FDB/ARP/route endpoint
that belongs to a known device is emitted pointing at that device's *canonical* id
(`serial:…`), not at a raw `mac:…`/`mgmt:…`. Provisional `mac:<addr>` / `mgmt:<ip>`
ids are the **unresolved exception**. This keeps Lot 5 fully exact and minimizes
identity-promotion churn (a provisional→canonical promotion is a delete+create, with
edges re-pointed and the stale node expiring by cascade + interval).

**Topology as entities (ADR 0022).** Because the embedded relationship model carries
**no edge attributes**, anything an edge would have described is promoted to an
entity so edges stay bare. The network topology vocabulary (entirely Toise's — OTel
standardizes no network entities, only `network.*` span/metric attributes) is:

| Entity | Identity | Descriptive attributes | Attached by |
| --- | --- | --- | --- |
| `network.device` | `{network.device.id}` (precedence ladder below) | `sys.name`, `mgmt.ip`, `device.role`, … | — (the discovered asset) |
| `network.interface` (a port) | `{network.device.id, interface.name}` | `oper_state` (state key), `speed`, … | `has_interface` (device→interface) |
| `network.route` | `{network.device.id, route.destination}` (CIDR) | `metric`, `route.protocol`, `next_hop.ip` | `has_route` (device→route) |

Physical adjacency is a **bare `connected_to`** (interface↔interface). Device-level
adjacency (the former `adjacent_to` with `local_port`/`remote_port`) becomes a
**derived** read-side view (a surcouche) over the port `connected_to` edges, not a
stored fact. Likewise `routes_via` (device→device) is superseded by the
`network.route` + `has_route` model, and `forwards_to` (FDB) by `connected_to` to
the learned port. **Provenance** (which collection method observed a fact) rides on
the **instrumentation scope** (one scope per source, e.g. `senhub-agent/snmp-lldp`,
`.../snmp-route`), never on the entity or edge.

`network.address` (and `bound_to` interface↔address, `next_hop_via` route→address) is
**deferred**: until it lands, a route's next hop rides as the scalar `next_hop.ip`
attribute and interface addresses stay descriptive. The **conformance fixture
demonstrates the model** (port + route entities, `has_interface`/`has_route`/bare
`connected_to`).

**Descriptive-attribute key casing.** Identity keys are fixed by the registry; for
**descriptive** attributes the convention is **dotted, lowercase** (`sys.name`,
`mgmt.ip` — not `sys_name`). Toise does **not** validate descriptive keys (only
entity/relation *types* are registered), so this is a cross-producer convention, not
a rejection — but producers should follow it for consistency. **Exception — state
keys:** the change engine recognizes a fixed set whose exact spellings it matches
(`oper_state`, `admin_state`, `status`, `replication.role`, `read_only`; note the
underscore forms). Emit the exact string for the change to classify as
`entity.state_changed` — a dotted variant such as `oper.state` is treated as an
ordinary descriptive attribute and the state change is lost.

**Remote endpoints known only by MAC.** `connected_to` requires **two
`network.interface` entities with exact identity** `{network.device.id,
interface.name}`. When a neighbor is known only by a MAC (FDB/ARP, some LLDP
remotes) and cannot be resolved to a `(device, interface.name)`, **do not fabricate
a phantom port** — that would violate exact identity. Resolve the MAC→device via the
producer's inventory before emitting the edge (as the identity ladder already
requires); otherwise omit it. A future `network.address` of MAC subtype may carry
such unresolved endpoints.

**Cadence:** poll topology **slower than metrics** (≈5–15 min); set
`entity.report.interval` to ≈**3× the topology cadence** (not the metric cadence) or
the liveness sweeper reaps devices between polls. Emit a **full snapshot per cycle
as one OTLP export** (one batched durable append); **no sampling** — a partial
snapshot would read as deletes. The committed conformance fixture carries the
`serial:`/`mac:` switches with their **`network.interface` ports** and a bare
**`connected_to`** edge (the topology-as-entities model).

*(A weighted multi-source identity model — evidence + `same_as` edges with
`confidence` + a derived canonical view — is the additive Phase-2 path for sources
that cannot converge on one exact id; see ADR 0020 (draft). Lot 5 ships exact /
producer-resolved and needs none of it.)*

### Type registry — types must be known

`entity.type` and each embedded relationship descriptor's `relationship.type` are
validated against Toise's **type registry**; an unregistered type is **rejected** at
the boundary. The registry is:

- **entities:** `host`, `process`, `network.interface`, `network.address`,
  `network.route`, `service.listener`, and the producer vocabulary
  `service.instance`, `db`, `network.device`, `network.endpoint`, `compute.vm`,
  `container`;
- **relations:** `runs_on`, `has_interface`, `has_route`, `bound_to`,
  `next_hop_via`, `listens_on`, `connected_to`, and the producer vocabulary
  `monitors` and `depends_on`, plus `same_as` (each with declared endpoint types
  and a structural flag);
- **`same_as` — producer-asserted identity belief (ADR 0020).** Any entity type
  either side; edge attributes `confidence` (0–1) and `basis` (e.g. `hyperv-kvp`,
  `serial_match`). Non-structural and **no failure impact** — it is not a
  dependency, so it never enters a blast radius. The producer states evidence it
  can justify; it never merges the entities. The canonical collapse over
  high-confidence `same_as` edges is a deferred read-time overlay (ADR 0020 Lot B);
  until then the edges accumulate and are queryable as-is. Per ADR 0022 Toise
  stores the asserted `confidence`/`basis` as-is and does not police their values.
- **legacy relations** `routes_via`, `forwards_to`, `adjacent_to` remain registered
  (so the boundary still accepts them) but are **superseded and not to be emitted** —
  use `network.route` + `has_route` + `next_hop_via`, and port-to-port
  `connected_to`, respectively.

`runs_on` is the foundational producer edge — `service.instance`, `compute.vm`,
and `container` each `--runs_on--> host`, as well as the existing
`process --runs_on--> host`. Endpoint-type pairings are **advisory** (not
runtime-enforced), so `monitors` may target a host, db, network.device,
compute.vm, or container. A host's own routing table is modeled the same way as a device's:
a `network.route` keyed `{host's network.device.id-equivalent…}` — in practice
host-sourced routing stays deferred with the host↔network.device *identity* twin
(§6b). Resolve each device endpoint to its canonical id where known (else
provisional `mac:`/`mgmt:`). Further monitored-system types (e.g. those discovered
by later collection lots) are added to the registry when introduced — the vocabulary
is the explicit coordination point with a producer (see `senhub-agent-contract.md`).

### Robustness backstops (the "no silent loss" principle)

A graph that aims to be an infrastructure source of truth must not lose or merge
data **silently**, and must not assume fragile invariants (event ordering, a
reliably-delivered delete). The agreed backstops, some implemented and some
planned:

| Concern | Decision | Status |
| ------- | -------- | ------ |
| Entity collisions | exact-Id matching (no fuzzy merge) | **done** (ADR 0018) |
| Missed entity deletes | explicit `entity.delete` + `entity.report.interval` TTL backstop | **done** |
| Edge liveness | derived from endpoints (cascade `relation.removed`) + removal-by-absence on the source's state | **done** |
| Out-of-order edges | reconciliation buffer (park & flush, opt-in) | **done** |
| Nested values | explicit `Warn` on drop (never silent) | **done** |
| Multi-producer liveness | per-producer reference counting (keyed by Resource `service.instance.id`) | **done** (ADR 0019) |
| Event identification | by LogRecord `EventName` (`entity.state`/`entity.delete`), per the merged spec — no payload attribute, no scope flag | **done** |

### Timestamps

OTel supplies the producer-side time via the `LogRecord` timestamp (or observed
timestamp), which becomes Toise's **`event_time`**. Toise always stamps
**`recorded_at`** itself at ingestion — it is never derived from the producer.
The two-timestamp model is described in
[ADR 0005](../architecture/adr/0005-bi-temporal-event-model.md).

## Schema versioning and spec churn

The Toise **`schema_version` (`"1.0"`) is independent of the OTel spec version.**
When the OTel entity-events spec changes, the change is absorbed by a
**migration layer at the ingest boundary** rather than by mutating the internal
model. This concentrates all spec-churn handling in one place and keeps stored
history and the projections built on it insulated from upstream version changes.

The OTel entity-events specification is **experimental** and still evolving. Its
shape can change between releases in ways that affect the wire contract Toise
consumes. Toise tracks the spec actively and pins its OTel dependencies; see
[ADR 0015](../architecture/adr/0015-tracking-otel-entity-events-spec.md) for the
tracking policy.

## Producers and the reference producer

A producer-side issue tracking entity-event emission in **senhub-agent** is open
at [senhub-io/senhub-agent#185](https://github.com/senhub-io/senhub-agent/issues/185).
Until that lands, **Milestone 4 uses a synthetic OpenTelemetry SDK Go client as
the reference producer**, exercising the OTLP ingest boundary against
spec-conformant entity events.
