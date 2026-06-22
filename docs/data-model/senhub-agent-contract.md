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
| `service.instance` | `{service.instance.id}` | the agent itself, or a monitored non-DB service (Kafka, RabbitMQ, NATS, Nginx, HAProxy, …). The id is a **stable** technology-reported identifier, else `<service.name>@<host.id>` — **never** `scheme://host:port`. `service.name` is descriptive. Datastores are `db`, not this; per the boundary rule, anything OTel semconv gives a `db.system.name` is a `db`. |
| `process` | `{process.pid, process.creation.time}` — the OTel semconv identity (the creation time disambiguates PID reuse) | a restart = a new process (delete + create), not an update; `process.executable.name` is descriptive |
| `db` | `{db.instance.id}` — a **stable source identifier**: PostgreSQL `system_identifier`, MySQL `server_uuid`, else an operator-configured logical instance name | **never network-derived** — `server.address`/`server.port` are mutable (DHCP/failover/VIP) so they stay descriptive attributes |
| `network.device` | `{network.device.id}` — a single subtype-prefixed value by **precedence**: `serial:` (ENTITY-MIB `entPhysicalSerialNum`) > `engine:` (`snmpEngineID`) > `mac:` (LLDP chassis-id) > `name:` (`sysName`) > `mgmt:` (mgmt IP) | **anchored on SNMP-immutable facts, not LLDP** (often disabled); `mgmt:` is mutable last-resort. Producer canonicalizes (Toise is byte-exact); raw parts descriptive. Endpoints resolved to the canonical id via `ifPhysAddress` before emitting edges. Frozen for Lot 5 — see [`otel-mapping.md`](./otel-mapping.md#networkdevice-identity--snmp-topology-lot-5-frozen). |
| `compute.vm` | `{host.id, vmid}` — the **hypervisor node's** `host.id` plus the hypervisor's vm id | a VM seen **from the hypervisor**, where the guest machine-id is unavailable. **Not** a `host` (a vmid is not a machine-id). `runs_on` the hypervisor `host`. The in-guest `host` (machine-id) is a separate facet, reconciled later by a `same_as` overlay (ADR 0020), never merged. **If the hypervisor *does* surface the guest machine-id (e.g. Hyper-V KVP), carry it as the descriptive attribute `guest.host.id` — evidence, never identity (the id stays `{host.id, vmid}`) — and still do not emit a `host` facet or merge.** That attribute is the `same_as` join key the overlay will consume; per ADR 0020 the producer asserts `same_as` (basis/confidence) once that layer lands, but until it does (`same_as` is not yet a registered relation type) the evidence rides as `guest.host.id`. Same pattern as AT8 (redfish/ibmi carry `hw.serial_number` on both facets). |
| `container` | `{container.id}` | an OCI/Docker container — a compute resource, not a `service.instance`. `image`/`name` descriptive; `status` is a **state key** (see below); `runs_on` its `host`. |

### Descriptive attributes — vocabulary & state semantics (toise#216)

Identity is settled above; this fixes the **descriptive** attribute vocabulary and
which attributes carry **state** semantics. Decided on toise#216 (AT1–AT7), grounded
in the change taxonomy (`stateKeys` → `entity.state_changed` vs
`entity.attribute_updated`, ADR 0006) and OTel semconv.

| Entity | Descriptive | State-bearing (`stateKeys`) |
| --- | --- | --- |
| `db` | `db.system.name`, `db.system.version`, `server.address`, `server.port`, `deployment.environment.name`, hosting platform (`cloud.provider`/`cloud.platform` or `db.deployment.platform`) | `replication.role` (`primary`/`replica`/`standby`), `read_only` |
| `service.instance` | `service.name`, `service.version`, `service.namespace` | — |
| `host` | `host.name`, `host.arch`, `os.type`, `os.version`, `os.description` | — |
| `container` | `container.name`, `container.image.name`, `container.image.tag`, `container.image.digest`, `container.runtime` | `status` (`running`/`stopped`/`paused`) |
| `network.device` | `hw.vendor`, `hw.model`, `hw.firmware_version` (hardware semconv); `sysName` and `sysDescr` (SNMP, raw/descriptive); mgmt address (descriptive, **mutable**) | — (reachability / admin status is a metric) |
| `compute.vm` | `host.name` (vm name); guest `os.type` / `os.version` when the hypervisor reports it; `guest.host.id` when the hypervisor surfaces the guest machine-id (evidence for the `same_as` overlay, not identity); configured capacity reusing the AT10 keys — `host.cpu.logical.count` (vCPUs) and `host.memory.total` (By) — as **config**, not utilization; `host.virtualization` for the hypervisor platform (the AT11 value set: `hyperv`/`vmware`/`kvm`/…) | power state (`running` / `stopped` / `suspended`) |
| `service.listener` | `process.executable.name`, `process.pid`, `network.transport`, `listen.address`, `port` (the port is also encoded in the `service.endpoint` identity) | — |
| `network.endpoint` | **none by design** — the observer sees only the identity (`server.address`, `server.port`, `network.transport`) and resolves to a canonical entity at read time (#184); populating more would mint a false identity | — |

- **AT1 — version key.** `service.version` (semconv) for services; `db.system.version`
  for databases (consistent with the `db.system.*` namespace). The technology name is a
  descriptive attribute (`db.system.name` / `service.name`), **never** an identity key.
- **AT2 — replication role.** One key `replication.role`, values `primary` / `replica`
  / `standby`. No bare `role`.
- **AT3 — environment vs platform — two axes, do not conflate.** Deployment tier →
  `deployment.environment.name` (semconv, free-form values, do not over-enumerate).
  Hosting platform (self_hosted / rds / aurora / cloudsql) → a **separate** key
  (`cloud.provider` / `cloud.platform`, or `db.deployment.platform`).
- **AT4 — stateKeys.** Toise's `stateKeys` gains `replication.role` and `read_only`, so
  a failover or a read-only flip classifies as `entity.state_changed` while the
  attribute stays distinct (a db can be `status=up` **and** `role=replica`). The agent
  emits them as plain descriptive attributes; Toise owns the state classification.
- **AT5 — container state.** Emit `status` (the existing stateKey: `running` /
  `stopped` / `paused`); no dedicated `container.state`.
- **AT6 — version change.** No dedicated lifecycle change type — `entity.attribute_updated`
  on `*.version` is enough; the LLM reads the before→after in `changed_keys`. Version
  stays descriptive (not a stateKey).
- **AT7 — attribute vs metric boundary.** On the entity: descriptive facts that change
  rarely and whose change explains an incident (version, role, environment, image
  tag/digest). On the metric rail: anything moving at scrape cadence (utilization,
  rates, lag). `replica_count` is **not** an attribute — model replicas as
  entities/relations (the count is derivable) or a metric.

**Follow-ups:** AT4 is **done** — `replication.role` + `read_only` are in Toise's
`stateKeys` (#217). Agent-side rollout starts with AT1 (redis `db.version` →
`db.system.version`; thread the captured version onto the mysql/postgres/oracle entity).

### AT8 — remote out-of-band probes & active checks

Probes that observe a target they do **not** run on (BMC/Redfish, remote SQL, ICMP/HTTP
checks) split into two families. The rule: **emit an entity only when the target has a
durable, non-network-derived identity the producer can assert (a serial, an operator-assigned
name); otherwise the check is pure telemetry (an `up`/latency metric), no entity.** This keeps
ADR 0018 intact — never mint a permanent identity from an IP or a URL.

- **redfish** (physical server via BMC) → `service.instance`, `service.name=redfish`,
  `id=redfish:<serial|uuid>`. Hardware facts use the OTel **`hw.*`** namespace: `hw.vendor`,
  `hw.model`, `hw.serial_number`, `hw.firmware_version`, `hw.bios_version` (descriptive);
  `hw.state` (`ok`/`degraded`/`failed`/`predicted_failure`) is **state-bearing**. Sensor
  readings (temperature, fan RPM, PSU watts) are **metrics**, not attributes. The same box's
  in-OS `host` (machine-id) is a **separate facet** — echo the serial as a descriptive
  attribute on **both** so a future `same_as` overlay (ADR 0020) can reconcile them; never merge.
- **ibmi** (LPAR via JT400) → `service.instance`, `service.name=ibmi`, `id=ibmi:<serial>`
  (QSRLNBR). Descriptive: system name, `os.type=ibmi`, `os.version`, `hw.model` /
  `hw.serial_number`, `deployment.environment.name`. **Not a `host`** (same collision argument).
  The IBM i Db2 is a **separate `db` entity** (boundary rule: anything semconv gives a
  `db.system.name` is a `db`), linked by `monitors`; do not fold the partition and the database
  into one entity.
- **gateway** (ICMP reachability of an IP) → **no entity**: a ping is reachability telemetry.
  Do not key a `network.endpoint` on the IP. If the gateway is wanted in inventory, the right
  model is a SNMP-discovered `network.device` (real identity), with the ping attached as a metric.
- **webapp** (URL check) → `service.instance` **only when the operator supplies a stable app
  name** (`service.name=<app>`, descriptive `url.full`, `service.version` if readable,
  `deployment.environment.name`); reachability stays a metric. With no operator-stable name,
  **no entity** — never key on the URL/host.

### AT9 — cross-cutting governance attributes

Operator-supplied descriptive facts that may decorate **any** entity type and make the
graph answerable for an LLM/human ("who owns this, how critical, where, what lifecycle").
They are plain descriptive attributes — Toise stores them as-is (ADR 0022), never requires
or normalizes them; an entity carrying none is valid. Reuse the semconv key where one
exists; the `entity.*` keys are Toise-provisional where semconv is silent (SIG-raise
candidates). Toise advertises this vocabulary on `describe_schema` (so a consumer discovers
the keys before any are observed) and filters it via `find_entities` (toise#231).

| Dimension | Key | Source | Notes |
| --- | --- | --- | --- |
| Owning team (services) | `service.namespace` | semconv (Stable) | reuse when the entity is a service; do not override an existing value |
| Owning team (any entity) | `entity.owner.team` | Toise-provisional | where `service.namespace` does not apply |
| Escalation contact | `entity.owner.contact` | Toise-provisional | optional |
| Criticality / tier | `service.criticality` | semconv (Development) | values `critical`/`high`/`medium`/`low`; semconv scopes it to services, Toise applies it to any entity |
| Physical location | `entity.location.site` / `.datacenter` / `.rack` / `.room` | Toise-provisional | on-prem; semconv covers only cloud regions |
| Lifecycle / maintenance | `entity.lifecycle.status` | Toise-provisional | open enum, e.g. `active` (in service) / `maintenance` / `decommissioning` / `retired`; **distinct from `deployment.environment.name`** (prod/staging/dev) — orthogonal axes |
| Free-form operator labels | `entity.label.<key>` | Toise-provisional | arbitrary operator keys under one prefix (e.g. `entity.label.cost_center`), string values; the prefix lets a consumer surface all operator labels at once |

All optional. Emit what the operator supplies (config/labels); never fabricate. `entity.owner.contact` is free-form (email, Slack, pager) — not email-only. `service.criticality` keeps the semconv value set (`critical`/`high`/`medium`/`low`); do not invent a parallel `tier_0/1/2`.

### AT10 — host capacity attributes

Nameplate/capacity facts on the `host` entity; the paired utilization stays a metric
(AT7). Keys extend OTel's `host.*` / `host.cpu.*` namespaces and are Toise-provisional
where semconv is silent.

| Key | Type | Unit | Example |
| --- | --- | --- | --- |
| `host.cpu.logical.count` | int | count | `48` |
| `host.cpu.physical.count` | int | count | `24` |
| `host.cpu.frequency.nominal` | int | **Hz** | `2100000000` |
| `host.memory.total` | int | By | `137438953472` |
| `host.disk.total` | int | By | `1920383410176` |

`host.cpu.frequency.nominal` is in **Hz (integer)**, not a human GHz string — UCUM-consistent
and machine-comparable; formatting to GHz is a consumer concern. (semconv-ratified host
nameplate already shipped — `os.name`, `os.build_id`, `host.cpu.model.name`,
`host.cpu.vendor.id`, and the DMI `hw.vendor`/`hw.model`/`hw.serial_number` — needs no
decision; the `hw.serial_number` on the in-OS host is the join key that lets the BMC facet
(AT8 redfish) reconcile via a `same_as` overlay, never merged.)

### AT11 — `host.virtualization` value vocabulary

OTel reserves `host.type` for the cloud machine type, so virtualization has no key. One
descriptive attribute `host.virtualization`, normalized lowercase, open enum:

`none` · `kvm` · `vmware` · `xen` · `hyperv` · `virtualbox` · `qemu` · `lxc` · `openvz` · `bhyve` · `unknown`

(`none` = bare metal; `unknown` = virtualized, type undetected.) Toise-provisional.

### AT12 — `host.chassis.type` value vocabulary

SMBIOS defines ~30 numeric chassis codes; raw codes are high-cardinality and unfriendly, so
normalize to: `desktop` · `laptop` · `server` · `blade` · `vm` · `other`. Mapping (abridged):
desktop/tower/all-in-one → `desktop`; portable/laptop/notebook/convertible → `laptop`;
main-server/RAID/rack/multi-system → `server`; blade/blade-enclosure → `blade`; `vm` derived
when chassis is Other/Unknown **and** `host.virtualization != none`; everything else → `other`.
Toise-provisional.

### AT13 — `network.interface` descriptive attributes

Beyond the `oper_state` state key (see the casing rule above) and `speed`, the host/SNMP
interface carries:

| Key | Type | Value | Note |
| --- | --- | --- | --- |
| `mac` | string | `aa:bb:cc:dd:ee:ff` | hardware address, stable per NIC |
| `mtu` | int | octets | config, not utilization |
| `interface.type` | enum | `physical`/`virtual`/`wireless`/`loopback` | start with physical/virtual |
| `duplex` | enum | `full`/`half`/`unknown` | renegotiable |

- **`speed` is in bit/s** (convert at the source: SNMP `ifSpeed` is bit/s, Linux `/sys` is
  Mbit/s). One `speed` key = the **negotiated/effective** rate; a separate `speed.max`
  (capability) is deferred until a use-case needs it.
- **Emit beyond the IP-bearing interfaces, but not the ephemeral churn.** The point of
  carrying IP-less NICs is that a *physical* link going down is then a clean
  `entity.state_changed` (with `oper_state`) rather than a disappearance — a signal that does
  not apply to the hundreds of ephemeral `veth*`/`cali*`/`cni*`/`lxc*` interfaces a container
  runtime creates and destroys at pod cadence. Emitting those as entities (each with a
  heartbeat and a teardown delete) is pure cardinality and churn for interfaces with no IP and
  no standalone meaning. So the inventory rule is:
  - **every interface that has an IP** — including named virtual ones (bridges, bonds, vlans),
    which have standalone meaning;
  - **plus IP-less `physical`/`wireless` NICs** — the down-link signal we want;
  - **excluding IP-less `virtual` interfaces** (the ephemeral/plumbing ones).

  The IP-resolution / connection-topology overlay still uses only the subset that have
  addresses.

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

The registry holds the agreed vocabulary: entities `service.instance`, `db`,
`network.device`, `network.interface`, `network.route`, `compute.vm`, `container`;
relations `monitors`, `has_interface`, `has_route`, `connected_to`, `same_as`. **`runs_on`**
is the foundational edge: `service.instance`, `compute.vm`, and `container` each
`--runs_on--> host`. **`same_as`** is a producer-asserted **identity belief** (ADR 0020):
any entity either side, edge attributes `confidence` (0–1) + `basis` (e.g. `hyperv-kvp`,
`serial_match`); non-structural, no failure impact, never merges. It is the graduation
target for the `guest.host.id` / `hw.serial_number` reconciliation evidence — assert
`same_as` once you hold a justified link. Toise stores `confidence`/`basis` as-is (ADR 0022);
the canonical collapse over high-confidence edges is a deferred read-time overlay (Lot B). `monitors` source is the `service.instance`
(the agent); targets are the monitored entity — `host`, `db`, and later `netscaler`,
`veeam`, `redfish`, `citrix`, `ibmi`, `network.device` (registered when their lot
lands).

**Do not emit `adjacent_to` / `routes_via` / `forwards_to`.** They remain registered
(legacy) but are superseded by the topology-as-entities model below — emit
`connected_to` (port↔port), `network.route` + `has_route`, and `connected_to` to the
learned port, respectively. Rollout:

- **Lot 1:** entities `host` + `service.instance`; relation `runs_on`.
- **Lot 2:** monitored systems (`db` first); relation `monitors`.
- **Lot 4 (host routing/ARP):** **host-sourced** topology from the host's own tables,
  modeled as entities (host routes as `network.route`, neighbors as port-to-port
  `connected_to` once ports are known). The host↔network.device identity twin (§6b)
  stays deferred. ARP is high-cardinality — filter to infrastructure/known devices.
- **Lot 5 (SNMP):** `network.device`, `network.interface`, `network.route` with
  `has_interface` / `has_route` / `connected_to`. `network.device.id` is **frozen**
  (precedence ladder + canonicalization above); rollout 5a LLDP → 5b routing → 5c FDB
  → 5d ARP, identity anchored on SNMP (serial/engine) so it does not depend on LLDP.
  The serial tier is vendor-namespaced `serial:<PEN>:<n>` (PEN from `sysObjectID`) and
  fires only with a single chassis + PEN; cross-vendor collisions and stacks (>1
  `entPhysicalClass=3`) both fall back to the globally-unique `engine:<engineID>`.

### Topology as entities — edges stay bare (ADR 0022)

Toise's engine is a **faithful, facts-only store** of the OTel entity-events
standard (ADR 0022), and the embedded relationship model carries **no edge
attributes**. So anything a producer would have hung on an edge becomes an
**entity** instead:

- **Ports are entities.** A port is a `network.interface` entity
  (`{network.device.id, interface.name}`, with `oper_state`/`speed` as attributes),
  linked by `has_interface` (device→port); physical adjacency is a **bare
  `connected_to`** (port↔port) — never `adjacent_to` carrying `{local_port,
  remote_port}`.
- **Routes are entities.** A routing-table entry is a `network.route`, identity
  **`{network.device.id, route.destination}`** (the destination as a canonical CIDR,
  e.g. `10.20.0.0/16`), linked by **`has_route`** (device→route). Its `metric`,
  `route.protocol`, and **`next_hop.ip`** ride as descriptive attributes. The next
  hop stays a scalar attribute because **`network.address` is deferred**; when it
  lands, `next_hop_via` (route→address) and `bound_to` (interface→address) follow.
- **Provenance → instrumentation scope.** Which collection method observed a fact
  rides on the **instrumentation scope** — **one scope per source**
  (`senhub-agent/snmp-lldp`, `senhub-agent/snmp-route`, `senhub-agent/snmp-fdb`, …),
  not a `source` attribute on the entity or edge.
- **`device.role`** (e.g. `switch`, `router`) is an **optional descriptive**
  attribute (never identity); infer best-effort from `sysServices` (L3 bit → router,
  L2 → switch) and omit when ambiguous.
- **Descriptive key casing is dotted lowercase** (`sys.name`, `mgmt.ip` — not
  `sys_name`). Toise does not validate descriptive keys, but follow this for
  cross-producer consistency.
- **State-bearing keys are a fixed recognized set with exact spellings** the change
  engine matches: `oper_state`, `admin_state`, `status`, `replication.role`,
  `read_only` (note the **underscore** forms — they predate and override the dotted
  convention). A change to one of these classifies as `entity.state_changed`; emit
  the exact string — a dotted variant like `oper.state` is silently treated as an
  ordinary attribute and the state change never fires.
- **Identity is unchanged** (`network.device.id` precedence `serial:<PEN>`/…, exact
  matching, per-producer liveness) — those are facts and stay.

The **conformance fixture demonstrates this model** (device + port + route entities,
embedded `has_interface`/`has_route`/`connected_to`). The producer-side resync is
tracked at [senhub-agent #222](https://github.com/senhub-io/senhub-agent/issues/222).
See ADR 0022 and `docs/architecture/migration-embedded-relationships.md`.

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
