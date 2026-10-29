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
| `host` | `{host.id}` (machine-id) | `host.name` descriptive. **The rendering is normative: a lowercase hyphenated UUID (8-4-4-4-12).** `/etc/machine-id` holds the same bytes as 32 hex digits with no separator, so a producer reading the file directly and one going through a library that formats it emit two spellings of one machine — a silent, permanent duplicate of every host. The conformance kit **fails** (not advises) on the raw form and on uppercase hex, wherever `host.id` appears: a `host` identity, a `compute.vm`'s hypervisor id, a host-local `network.endpoint`'s fourth key, a relationship target. A producer already emitting another rendering in production must **coordinate** the change — it re-keys every host — rather than switch on upgrade. |
| `service.instance` | `{service.instance.id}` | the agent itself, or a monitored non-DB service (Kafka, RabbitMQ, NATS, Nginx, HAProxy, …). The id is a **stable** technology-reported identifier, else `<service.name>@<host.id>` — **never** `scheme://host:port`. `service.name` is descriptive. Datastores are `db`, not this; per the boundary rule, anything OTel semconv gives a `db.system.name` is a `db`. |
| `process` | `{process.pid, process.creation.time}` — the OTel semconv identity (the creation time disambiguates PID reuse) | a restart = a new process (delete + create), not an update; `process.executable.name` is descriptive |
| `db` | `{db.instance.id}` — a **stable source identifier**: PostgreSQL `system_identifier`, MySQL `server_uuid`, else an operator-configured logical instance name, else the **host-scoped fallback** `<db.system.name>:<port>@<host.id>` | **never network-derived** — `server.address`/`server.port` are mutable (DHCP/failover/VIP) so they stay descriptive attributes. See the fallback rules below. |
| `network.device` | `{network.device.id}` — a single subtype-prefixed value by **precedence**: `serial:` (ENTITY-MIB `entPhysicalSerialNum`) > `engine:` (`snmpEngineID`) > `mac:` (LLDP chassis-id, rendered per the MAC rule below) > `name:` (`sysName`) > `mgmt:` (mgmt IP) | **anchored on SNMP-immutable facts, not LLDP** (often disabled); `mgmt:` is mutable last-resort. Producer canonicalizes (Toise is byte-exact); raw parts descriptive. Endpoints resolved to the canonical id via `ifPhysAddress` before emitting edges. Frozen for Lot 5 — see [`otel-mapping.md`](./otel-mapping.md#networkdevice-identity--snmp-topology-lot-5-frozen). |
| `compute.vm` | `{host.id, vmid}` — the **hypervisor node's** `host.id` plus the hypervisor's vm id | a VM seen **from the hypervisor**, where the guest machine-id is unavailable. **Not** a `host` (a vmid is not a machine-id). `runs_on` the hypervisor `host`. The in-guest `host` (machine-id) is a separate facet, reconciled later by a `same_as` overlay (ADR 0020), never merged. **If the hypervisor *does* surface the guest machine-id (e.g. Hyper-V KVP), carry it as the descriptive attribute `guest.host.id` — evidence, never identity (the id stays `{host.id, vmid}`) — and still do not emit a `host` facet or merge.** That attribute is the `same_as` join key the overlay will consume; per ADR 0020 the producer asserts `same_as` (basis/confidence) once the read-time collapse lands; `same_as` has been a registered relation type since v0.16.0, with endpoints unconstrained, so a producer holding a justified link may assert it today, and the evidence rides as `guest.host.id` only until then. Same pattern as AT8 (redfish/ibmi carry `hw.serial_number` on both facets). |
| `network.interface` | `{host.id, network.interface.name}` on a host, `{network.device.id, network.interface.name}` on a polled device — the owner, plus the interface's name **in the semconv spelling** | an interface is named only **within its owner**: `eth0` exists on every machine, so the name alone identifies nothing. **The spelling is closed for identity**: `interface.name`, the pre-semconv key, stays valid as a descriptive attribute and as a telemetry join key — a graph still holds interfaces observed before the migration — and must **never** appear in an identity. Identities are hashed over the keys as sent, with no normalization, so one port named two ways is two entities joined by nothing, and the duplicate surfaces months later. The conformance kit refuses the retired spelling in an identity and accepts it everywhere else. |
| `host.service` | `{host.id, host.service.name}` — the owner, plus the **full name the supervisor gives**: `nginx.service`, `nginx.socket`, `Spooler` | a unit the system's supervisor manages: a systemd unit on Linux, a Windows service on Windows. **One type for both**, because it is one concept — something the operating system is meant to start by itself. The name is **not** the bare one: `nginx.service` and `nginx.socket` are two different units, and a bare `nginx` fuses them into one entity whose attributes oscillate. Scoped by the host because a service name is unique only **within** its machine, like `eth0`. Emit only what the system is meant to start by itself — **automatic start** on Windows (delayed included), **enabled** units on systemd — plus any the operator names: all ~200 services of a Windows machine would multiply a graph fivefold for a question nobody asked, while a manual one that is not running is normal. **Run state is normative — see AT12**: emit `status` from the frozen value set, and enumerate the **installed** units so a stopped service is emitted rather than absent. Start mode stays descriptive. The claim this row used to make — that an automatic-start service which is not running *is an incident* — is **false as stated**: some automatic-start services stop by design, and it was the justification for the whole type (AT12). `runs_on` to the host. **Our extension** of the upstream `host.*` namespace, on the `entity.host.cpu` precedent — nothing upstream covers this ground, and the two collector-contrib receivers on it use divergent local vocabularies; the spelling is **proposed** until an upstream experience report settles it. |
| `k8s.node` | `{k8s.node.uid}` | the **cluster's view of a machine** — not a second host. It carries the machine's **role in the cluster**: taints, labels, allocatable capacity, conditions, which are not facts about the machine. A node **cordoned while its host is perfectly healthy** is the case that makes the two distinct, the same shape as `compute.vm` beside `host`. Joined to its host by a **`same_as` belief**, which a DaemonSet agent asserts with the cleanest evidence there is: it reads `node.UID` and `node.Status.NodeInfo.MachineID` from the **same** cluster-API object, so it holds both identities itself, and each is a complete identity of one machine — the collapse would lose no identifying attribute, which is the test that makes it a `same_as` rather than a dedicated relation. Cluster-role attributes (`k8s.node.name`, `k8s.kubelet.version`) belong here; `host.uuid` stays on the host. So "which hosts are cluster nodes" becomes a walk over an observed belief instead of a filter on an attribute. |
| `cloud.resource` | `{azure.resource.id}` — the ARM resource id in **lower case** | a resource as the cloud's own control plane holds it, and a **third facet** of a machine beside `host` and `compute.vm`: the control plane's view is not the in-guest view, so they are separate entities joined by a **`same_as`** belief (basis `vm_id_match`, on `azure.vm.id`) and never merged — `host.id` stays the OS machine-id. The type is deliberately **generic**: the observer polls a resource graph, which hands it the provider's identity for the object and nothing it could key the service *inside* that object by, so a per-family type would promise a specialisation no producer can supply. **The identity key is ours on purpose.** `cloud.resource_id` keeps the casing the API returned, because a consumer joins on it to reach Azure's own telemetry and we hold **no** upstream rule fixing that rendering — a value we received, on a key we do not own, is not ours to normalise. An identity has the opposite requirement: identity is byte-exact (ADR 0018) and Azure returns the same id cased differently depending on which API answered, so two spellings would be two entities for one resource. Lower-casing cannot merge two distinct resources (ARM resource and resource-group names are case-insensitive). Subscription, resource group and region are **descriptive attributes, not entities**: they carry no hop. See `docs/design/azure-resource-entity-model.md`. |
| `container` | `{container.id}` | an OCI/Docker container — a compute resource, not a `service.instance`. `image`/`name` descriptive; `status` is a **state key** (see below); `runs_on` its `host`. |
| `network.segment` | `{network.segment.id}` — a single subtype-prefixed value by **precedence**, of which only `swarm:<network-id>` is **frozen** | a broadcast/reachability domain with an **assigned** identifier (ADR 0034). `k8s:` and `vlan:` are deliberately **open**: Kubernetes has no object to identify in the default flat-network case, and a VLAN id is not globally unique while a VLAN trunked across five switches is **one** segment, not five. Emitted **with its `has_segment` edge from the declaring cluster or not at all** — a segment nobody can say whose it is has no business in the graph. Workloads join it with `attached_to` **from the entity holding the network namespace**: a `container` on Docker/Swarm, a `pod` on Kubernetes. Subnet / internal / ingress are descriptive. Membership is **necessary and not sufficient** for reachability. |
| `network.neighbor` | `{host.id, mac}` when a host observed it, `{network.device.id, mac}` when a polled device did — the **observer's** id, plus the neighbour's MAC | a link-layer neighbour an observer saw on one of its own interfaces and could **not** resolve to an entity it already holds (ARP/NDP, an FDB line, an LLDP remote known only by its chassis MAC). **`host.id` here is a SCOPE, not an owner** — the same role it plays as the fourth key of a host-local `network.endpoint`. Scoped on the observer because a MAC is not globally unique (it repeats under virtualization, rotates under randomization) and a broadcast domain carries no assigned identifier (ADR 0034). Two observers on one segment therefore hold two neighbours for one device, on purpose: a wrong merge is permanent, a split is reconciled by a `same_as` belief. **Not** a `network.interface` — see the phantom-port rule. Our own extension: the upstream convention has no neighbour entity. |

#### A MAC is rendered one way, everywhere

**IEEE RA hexadecimal: uppercase, hyphen-separated octets, most significant
first — `AA-BB-CC-DD-EE-FF`.** That is the form semconv requires for `host.mac`,
and it applies wherever a MAC appears in this contract: the descriptive `mac` of a
`network.interface`, the `mac:` rung of `network.device.id`, and the identifying
`mac` of a `network.neighbor`.

It is normative for the same reason the `host.id` rendering is, and the reason is
worth restating because this is the second value to need it. **A MAC can become an
identity**, and it is the value a `same_as` belief is joined on when SNMP later
names the device a neighbour was first seen as. Two renderings of the same six
bytes are two entities under exact identity (ADR 0018): nothing says they are one,
the join silently finds nothing, and the damage is a permanent duplicate that
surfaces months later.

The form is not ours to choose. We take upstream's because the only argument that
could outweigh it — "our own spelling reads better" — is exactly the argument that
produces a model nobody else can join.

A producer already emitting another rendering in production must **coordinate**
the change rather than switch on upgrade: it re-keys every entity whose identity
carries a MAC. Measuring first is cheap — the `mac:`-prefixed `network.device.id`
values and the neighbour identities in the graph either all match the normative
form or they do not.

#### `db.instance.id` — the host-scoped fallback

MariaDB and Redis expose no stable instance identifier (Redis's `run_id` changes
at every restart), and most operators never configure a logical name, so the
fallback is the common case rather than the exception. It must still not be
network-derived: a bare `127.0.0.1:3306` is byte-identical on every machine, so
every local database in the estate collapses into **one** entity whose attributes
flip between servers — measured in production as 118 attribute updates in six
hours, with telemetry join keys pointing at one machine while the attributes
described another.

The fallback is therefore **`<db.system.name>:<port>@<host.id>`**:

```
mysql:3306@8b861704-05bc-4382-b057-7eac3df5e730
redis:6379@8b861704-05bc-4382-b057-7eac3df5e730
```

- The `@<host.id>` form is the one this contract already uses for
  `service.instance` (`<service.name>@<host.id>`). The question "how do you name
  a local thing that has no stable id" gets **one** answer, not one per type.
- The **system name is required** for the scoped form. Without it, fall back to
  the raw `address:port` rather than emitting `:3306@<host.id>`, which would name
  a port on a machine and nothing else.
- The port is a **discriminant within a host** (two Redis on one machine), not
  the basis of the identity. Changing a listening port re-keys the entity, which
  is a deliberate and rare operation.
- Any scheme prefix a producer carries on the remote form (`mssql://`) is
  **dropped** on the local form, where the system name already says it. It stays
  on the remote form, where it is what separates this identifier from another
  product reachable at the same address and port.
- **Routable addresses are left alone.** They already distinguish the target, and
  rewriting them would re-key every remote database without fixing anything.

Because the identity is now unique per machine, the anti-collapse rule below
(host-local literals are never a shared identity) stops holding local databases
back, and each one gains its `runs_on`. A local database was previously in no
host's impact radius at all — that was a hole in the graph, not a modelling
nicety.

**Migrating an identity — what to emit, and how to verify it.** A producer
changing how it spells an existing identity should emit two things alongside the
new entity, both **unconditionally**, with no persisted state to decide:

1. An **explicit `entity.delete` of the old identity**, carrying
   **`entity.delete.reason=rekeyed`**. The reason matters as much as the delete:
   every other value in the table claims something happened — the resource ended,
   the observation ended, a cache forgot — and here **nothing happened at all**.
   The service is still running; only its spelling in this system changed. That is
   why `rekeyed` is its own family, and why it shares its word with the
   `basis="rekey"` of the bridging edge below: a reader who meets one finds the
   other. Without it the old entity
   dies by liveness expiry and the change feed reads `delete_source=liveness_expiry`
   — "the producer went silent" — when the truth is "someone decided". A delete
   naming an identity that never existed is a complete no-op: no event, no error,
   nothing in the change feed or the OTLP response.
2. A **`same_as` from the new identity to the old**, `basis="rekey"`,
   `confidence=1.0`, for one cycle. History is never rewritten, so the merged
   entity stays visible as deleted with its timeline intact; the edge is what keeps
   the two timelines joinable. On a fresh install, where the old identity never
   existed, the edge is simply parked and dropped — no event, no ingest error,
   nothing returned to the producer, at the cost of one `Warn` on the Toise side.

!!! warning "After the cutover the bridging edge reads as absent"
    The cascade removes every edge touching a deleted entity, so once the old
    identity is gone, a current-state query for `same_as` returns nothing and
    `canonical` on the survivor returns null. That is correct — an edge to a
    deleted entity is meaningless in the present — but it means the obvious
    verification reads zero and looks like a failure to emit. **Verify with a
    dated read at the cutover instant**, which shows both entities and the link.

#### `entity.delete.reason` — a distinct axis from `delete_source`

The two answer different questions and are **not** two spellings of one fact:

- **`delete_source`** is *who authored the disappearance*. Toise writes it
  (ADR 0033), the producer never does.
- **`entity.delete.reason`** is *why the producer decided*. The producer writes
  it, Toise stores it verbatim and never validates it (ADR 0022).

Producer values are therefore **not** a subset of `delete_source`. Aligning them
would have the producer restate a fact the consumer already owns, and the two
would drift the moment Toise gains a fourth source — a retention reap or an
operator purge, both anticipated by ADR 0033. The value spaces do not align
either: `liveness_expiry` is meaningless from a producer that is by definition
speaking, and a configuration change is meaningless as an author.

**The enum stays open** — Toise accepts any string. But an open enum with no
recommended core is how every producer invents its own spelling, so the values
below are what Toise recommends. Converge on them; do not be policed by them.

| Recommended value | Meaning | Family |
| --- | --- | --- |
| `terminated` | the resource ended normally | resource lifecycle |
| `evicted` | it was displaced by its scheduler | resource lifecycle |
| `scaled_down` | it was removed by a scaling decision | resource lifecycle |
| `user_requested` | a human asked for its removal | resource lifecycle |
| `expired` | the producer's own retention/TTL ended it | resource lifecycle |
| `parent_removed` | the producer removed it because its parent went away | resource lifecycle |
| `unmonitored` | **the observation ended; the resource may well still exist** | observation |
| `aged_out` | **a cache forgot the entry; nobody saw the thing leave** | observation |
| `excluded_by_config` | **the configuration removed the report, not the observer: the producer still sees the resource and does not publish it** | observation |
| `rekeyed` | **nothing happened in the world: the producer changed how it spells this identity** | producer bookkeeping |

**The rule that matters: never use a lifecycle value when you merely stopped
looking.** A probe removed from the configuration, or a target that left the
scope, deletes the entity for a reason that says nothing about the resource. A
database entity that disappears because someone edited a probe list must not read
as "the database is gone" — that is the reading that causes an incident, and
`unmonitored` is what prevents it. It is the producer-side mirror of
`liveness_expiry`: there Toise says *I stopped hearing*, here the producer says
*I stopped looking*.

`aged_out` and `unmonitored` are both observation values and are **not**
interchangeable: `unmonitored` means *we stopped looking*, `aged_out` means *we
are still looking and the table forgot*. An ARP, NDP or FDB entry that ages out
is the second — and ADR 0006's own note says it: a cache evicts, and **an
eviction is not a departure**.

The three observation values are not interchangeable either, and the two that
speak of configuration are the pair most easily confused: `unmonitored` means
the configuration removed the **observer** — the probe stopped, the target left
the scope, nobody is looking — while `excluded_by_config` means the
configuration removed the **report** of something still observed. The producer
reads the resource, sees it, and its own filter tells it not to publish it. It
is the only one of the three where the operator holds the lever: change the
filter and the thing comes back. A filtered removal announced as `unmonitored`
sends its reader to check the probe inventory; announced as `terminated`, it
asserts the resource left.

`excluded_by_config` is a **transition marker, not a property of state**. It is
emitted only on the cycle where the thing stops being published, so this value
cannot answer *what is excluded today*: a producer that restarts with its filter
already in place retires nothing, and the entities it published before fall to
`liveness_expiry`. What a filter withholds in steady state is read on the
producer's own counter, never on this value.

Do **not** spell that case `evicted`. The word is taken, one row above, for a
scheduler displacing a resource — the opposite family, where the thing really
did end. A producer reporting an aged-out neighbour as `evicted` produces the
worst of the three readings available: `terminated` at least looks wrong on a
cache entry, while `evicted` looks plausible *and* this table would confirm the
wrong conclusion. That mistake was made, from this document's own author
recommending the word without re-reading the table.

`parent_removed` is deliberately **not** spelled `cascade`, even though the
producer-side situation resembles Toise's: `cascade` is a `delete_source` value
meaning *Toise removed an edge whose endpoint died*. Reusing one word across the
two axes would recreate, one level down, the ambiguity ADR 0033 exists to remove.

Read together, the pairs are unambiguous:

| `delete_source` | `delete.reason` | Reading |
| --- | --- | --- |
| `producer` | `terminated` | the thing ended, and the producer saw it |
| `producer` | `unmonitored` | the thing may still exist; we stopped watching |
| `producer` | `aged_out` | the thing may still exist; a cache forgot it |
| `producer` | `excluded_by_config` | the thing exists and we see it; a configuration tells us not to report it |
| `producer` | `rekeyed` | the thing is untouched; this spelling of it was retired |
| `producer` | `parent_removed` | producer-side cascade: the parent went first |
| `producer` | *(absent)* | the producer decided and gave no motive |
| `liveness_expiry` | *(absent)* | the producer went silent past its interval |
| `cascade` | *(absent)* | Toise removed an edge whose endpoint died |

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
| `network.device` | `hw.vendor`, `hw.model`, `hw.firmware_version` (hardware semconv); `sys.name` and `sys.descr` (SNMP, raw/descriptive — the emitted keys are dotted lowercase; `sysName`/`sysDescr` name the MIB objects they are read from, not the keys); mgmt address (descriptive, **mutable**) | — (reachability / admin status is a metric) |
| | **`hw.vendor` value form is normative** — see below. | |
| `compute.vm` | `host.name` (vm name); guest `os.type` / `os.version` when the hypervisor reports it; `guest.host.id` when the hypervisor surfaces the guest machine-id (evidence for the `same_as` overlay, not identity); configured capacity reusing the AT10 keys — `host.cpu.logical.count` (vCPUs) and `host.memory.total` (By) — as **config**, not utilization; `host.virtualization` for the hypervisor platform (the AT11 value set: `hyperv`/`vmware`/`kvm`/…) | power state (`running` / `stopped` / `suspended`) |
| `cloud.resource` | `cloud.resource_id` (the ARM id **as the API returned it** — the upstream key, for the telemetry join, never the identity); `azure.resource.name`, `azure.resource.type`, `azure.resource_group.name`, `azure.resource_provider.namespace`, `azure.tenant.id`, region; `azure.vm.id` on a VM resource (the `same_as` join key, rendered as a **lower-case hyphenated UUID**, same rule as `host.id`) | `azure.resource.provisioning.status` (from ARM's provisioning state) and `azure.resource.status` (the product state) |
| `service.listener` | `process.executable.name`, `process.pid`, `network.transport`, `listen.address`, `port` (the port is also encoded in the `service.endpoint` identity) | — |
| `network.endpoint` | **none by design** — the observer sees only the identity (`server.address`, `server.port`, `network.transport`) and resolves to a canonical entity at read time (#184); populating more would mint a false identity. **Host-local and link-local addresses only** (`127.0.0.0/8`, `::1`, `169.254.0.0/16`, `fe80::/10`) carry a **fourth identity key `host.id`** (the observing host's id, byte-identical to the `host` entity identity) — their scope is narrower than the observation domain, so the 3-key join would falsely merge distinct endpoints (ADR 0032 addendum). Everything else, including RFC1918 and CGNAT, stays 3-key. | — |

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

#### AT12 — `host.service` run state is normative

`host.service` exists so that "an automatic-start service that is not running" can be
asked as a graph question. Until now this contract emitted the start mode and froze
**no** run state at all, saying only that start mode and state "are descriptive" — so
the type answered the easy half of its own justifying question and none of the hard
half (#558). Measured on a production estate: 630 of 630 entities carried no run state
under any spelling, and "which automatic-start services are stopped" returned a clean
empty list, which reads as "none are".

**The key is `status`**, not a new one. It is already in Toise's `stateKeys`, so the
change classifies as `entity.state_changed` with no engine change and no version
coupling between producer and server; `container` (AT5) and `compute.vm` already carry
the same concept for the same question. It does not collide with `hw.state`
(`ok`/`degraded`/`failed`/`predicted_failure`), which is **health**, not run state — a
power supply never runs.

**Frozen value set**, lowercase, one word, extending AT5's three:

`running` · `stopped` · `starting` · `stopping` · `paused` · `failed` · `unknown`

- Windows SCM: `RUNNING`→`running`, `STOPPED`→`stopped`, `PAUSED`→`paused`,
  `START_PENDING`/`CONTINUE_PENDING`→`starting`,
  `STOP_PENDING`/`PAUSE_PENDING`→`stopping`.
- systemd: `active`→`running`, **`reloading`→`running`** (the unit is active, re-reading
  its configuration), `inactive`→`stopped`, `activating`→`starting`,
  `deactivating`→`stopping`, `failed`→`failed`.

**Never omit the attribute.** Emit `unknown` when the state is unreadable. On host
interfaces the equivalent attribute is *omitted* when unreadable, so a consumer writing
`!= "up"` misses that case: an absent attribute reads as a value (#537).

**`failed` cannot come from Windows and `paused` cannot come from systemd**, and both
must be declared. The SCM has no failure state — a crashed service reads `STOPPED` — so
a rule written `status == "failed"` never fires on a Windows estate, and a rule that can
never fire reads as coverage. The load-bearing test is `status != "running"`; `failed` is
a systemd refinement that says why.

**Transitional values are signal, not noise.** Under any realistic cadence a restart
completes inside one poll interval, so `starting`/`stopping` are observed almost only
when the transition is **stuck**, which is the case worth alerting on. They turn noisy
only below roughly ten seconds of cadence.

**The emission enumerates installed units, not running ones.** This is the half without
which the key changes nothing. A producer that keeps enumerating what is running and
merely adds `status` emits `running` on everything, and a stopped service stays
**absent** — the empty list stays empty. Measured on the same estate: 121 `host.service`
deletions in six hours were the producer reporting the service gone, 77 of them one
delayed-start service that shuts down when idle, a cycle every 23 minutes per host. An
absent entity reads as a value exactly as an absent attribute does, and it is worse:
after one stop/start cycle the recreated entity's readable history is two events, so the
stop is not reconstructible even by a reader who knows to look for it. Keep the
`start_mode` filter; change what it is applied to.

**No graph-only predicate is sufficient today, and this contract does not pretend
otherwise.** `start_mode = auto_start AND trigger_start = false AND status != running`
looks complete and is wrong: on the measured estate the two services that stop most
often are both `auto_start` and both `trigger_start = false`, and both stop by design.
492 of 639 entities sit in that set, and at least 28 belong to a service the producer
deleted inside a single six-hour window. `start_delayed` does not separate them either —
the two disagree on it. So either the producer gains a discriminator for "stops when
idle by design", or the rule needs an operator-maintained exclusion. Until one exists
this contract names what is missing rather than shipping a predicate a reader would turn
into an alert rule.

`host.service.trigger_start` stays worth emitting and reading: a trigger-start service is
`auto_start` but launched only on its trigger, so stopped is its normal state. It removes
a known-false population — 147 of 630 on the measured estate — without making the
remainder a clean signal.

**The metric stays.** A run-state metric and this attribute are not duplicates: the
metric carries duration, history at scrape resolution and alerting; the attribute carries
the graph read and the change event. They **will** disagree transiently, being read at
different instants — the attribute is authoritative for the graph, the metric for
alerting, and **neither is a check on the other**.

#### AT13 — `ha.role` is its own key, not a widened `replication.role`

A member of a high-availability pair has a current role: it carries the traffic, or it
waits. A `network.device` in a firewall cluster, a `service.instance` under keepalived,
a `db` behind a virtual address and a `host.service` can all be in such a pair, and this
contract had no key for it.

**It is not `replication.role`.** AT2's rule is one key for one fact, and the test for
"one fact" is whether an entity can legitimately need both values at the same time. It
can: a database node is `replication.role=replica` **and** `ha.role=active` when it
serves reads behind a virtual address while the writes go elsewhere. "Where do the
writes go" and "which member carries the traffic" are two questions whose answers do not
always coincide, and one key cannot hold two values. Widening `replication.role` would
also have made `replication.role == "primary"` a sentence about a firewall.

**State-bearing.** A failover is a state change, not an attribute edit, so `ha.role` is
in Toise's `stateKeys` and `TestStateKeysSet` pins the membership — the same mechanism
that caught `hw.state` being state-bearing in this document and absent from the set.

**Frozen value set**, lowercase, one word:

`active` · `standby` · `unknown`

**The mapping is declared per source**, because the vendors disagree and a key whose
values are not comparable is not a contract — the `hw.vendor` lesson, three occurrences:

- keepalived / VRRP: `MASTER` -> `active`, `BACKUP` -> `standby`.
- clusters that speak of primary and secondary (firewall pairs among them):
  `primary` -> `active`, `secondary` -> `standby`.
- any other source **declares its mapping in this table before emitting the key**. An
  undeclared vocabulary is the defect this rule exists to prevent, not a detail to settle
  in the producer.

**Never omit it.** Emit `unknown` when the role is unreadable. An absent attribute reads
as a value, so a consumer writing `!= "active"` misses the unreadable case — the reason
AT12 forbids omitting `status`, and the defect of #537.

**What it does not say.** `ha.role=active` names which member carries the traffic now. It
does **not** say the pair is healthy, and it does **not** say a failover would work. A
measured incident is the case in point: a keepalived failover completed correctly and
repaired nothing, because the pair was not redundant on the inbound path. A reader must
not infer redundancy from two members holding complementary roles — that inference is
the reason this paragraph is in the contract rather than in a commit message.

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
| Owning team (any entity) | `senhub.owner.team` | ours (was `entity.owner.team`) | where `service.namespace` does not apply |
| Escalation contact | `senhub.owner.contact` | ours (was `entity.owner.contact`) | optional |
| Criticality / tier | `service.criticality` | semconv (Development) | values `critical`/`high`/`medium`/`low`; semconv scopes it to services, Toise applies it to any entity |
| Physical location | `senhub.location.site` / `.datacenter` / `.rack` / `.room` | ours (was `entity.location.*`) | on-prem; semconv covers only cloud regions |
| Lifecycle / maintenance | `senhub.lifecycle.status` | ours (was `entity.lifecycle.status`) | open enum, e.g. `active` (in service) / `maintenance` / `decommissioning` / `retired`; **distinct from `deployment.environment.name`** (prod/staging/dev) — orthogonal axes |
| Free-form operator labels | `senhub.label.<key>` | ours (was `entity.label.<key>`) | arbitrary operator keys under one prefix (e.g. `senhub.label.cost_center`), string values; the prefix lets a consumer surface all operator labels at once |

All optional. Emit what the operator supplies (config/labels); never fabricate. `senhub.owner.contact` is free-form (email, Slack, pager) — not email-only.

**Why these keys left `entity.*`.** The semconv naming rule is explicit: "It is not recommended to use existing OpenTelemetry semantic convention namespace as a prefix for a new company- or application-specific attribute name." `entity.*` is the entity-events convention's own namespace, and `entity.id`, `entity.type`, `entity.state` and the event names come from it — those stay. The governance keys never did: they were ours, inside someone else's namespace. If upstream defines `entity.owner.team` with other semantics, data already in the field would mean two things at once with nothing to tell them apart, and it would happen after a 1.0 freeze rather than before it.

**Old spellings are not rejected and not rewritten.** Governance attributes are descriptive, so the engine kept whatever a producer sent (ADR 0022) and the `entity.*` values emitted before the rename are still in the graph. `describe_schema` lists each key's previous spellings in a `was` field, because a consumer filtering over history needs both the key to use and the key to find. A producer migrates by emitting the new spelling; nothing re-keys and no entity is re-minted, these never being identity. `service.criticality` keeps the semconv value set (`critical`/`high`/`medium`/`low`); do not invent a parallel `tier_0/1/2`.

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
| `mac` | string | `AA-BB-CC-DD-EE-FF` | hardware address, stable per NIC. **Rendering normative** — see the MAC rule below |
| `mtu` | int | octets | config, not utilization |
| `interface.type` | enum | `physical`/`virtual`/`wireless`/`loopback` | start with physical/virtual |
| `duplex` | enum | `full`/`half`/`unknown` | renegotiable |
| `network.interface.addresses` | string[] | `["10.10.0.60/24"]` | the interface's own addresses, each with its prefix |
| `network.interface.subnets` | string[] | `["10.10.0.0/24"]` | the subnets those addresses sit in, deduplicated — **provisional**, see below |

- **Addresses and subnets are arrays, and their value form is upstream's.** Both are
  `string[]`, one element per address — **never** a joined string. A comma appears in
  no IP text form, which is exactly what makes a joined value dangerous: it parses
  cleanly as a single address, so a consumer that forgets to split shows one and is
  never told it is wrong. The array is also the established upstream form — `host.ip`
  and `host.mac` are both `string[]`. The notation is the one semconv specifies for
  CIDR values: IPv4 in dotted-decimal with a prefix length of 0 to 32, IPv6 in RFC
  5952 canonical text with a prefix length of 0 to 128.

  **Host bits are the one place the two keys disagree, deliberately.** `subnets`
  follows the frozen canonicalization and has its host bits zeroed
  (`10.10.0.0/24`); `addresses` keeps them (`10.10.0.60/24`), because the address is
  the point. The two values are indistinguishable by shape, so anything that
  pattern-matches "looks like CIDR" will read one of them wrong. Read the key, never
  the shape.

  **`addresses` is the observed fact; `subnets` is derived from it.** Masking an
  address by its prefix yields the subnet with no additional knowledge, so the two
  can never legitimately disagree — a disagreement is a producer bug, not an
  ambiguity for a consumer to resolve. Both keys are **absent** when no prefix is
  known, including on a device that does not answer `ipAdEntNetMask`. Absent, never
  present and empty: an empty string reads as an answer.

  **`subnets` is provisional.** The OpenTelemetry Network Observability project lists
  "IP subnet" among the L3 entities it intends to define, in its October 2026 to
  January 2027 window. A subnet has an identity — its prefix — and a lifecycle, and
  this document's own rule is that a fact which must persist belongs on an entity
  rather than on an attribute. Expect this key to be superseded by an entity and a
  traversal, and do not build anything that cannot absorb that.
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

### AT14 — `network.neighbor`: what an observer saw and could not name

A `network.neighbor` is the home for a link-layer observation that resolves to
nothing: an ARP or NDP entry, an FDB line, an LLDP remote known only by its chassis
MAC. It exists so that such an observation can be recorded **without claiming
anything the producer did not see** — and in particular without fabricating an
interface for it.

**It is our own extension.** The upstream convention has no neighbour entity, and no
entity keyed on a MAC; `host.mac` exists as an attribute and `network.interface.name`
is a release candidate, and that is the whole of the upstream surface here. Read this
section as a Toise vocabulary decision, not as a convention to expect elsewhere.

**Identity: the OBSERVER's id plus the MAC** — `{host.id, mac}` when a host saw
it, `{network.device.id, mac}` when a polled device did (a MAC learned on a port,
read from the bridge forwarding table). Two shapes, one rule: the observer is in
the identity. That is the same role the key plays as the fourth key of a
host-local `network.endpoint` — a scope, never an owner.

**The MAC rendering is normative, and it is upstream's: IEEE RA hexadecimal —
uppercase, hyphen-separated octets, most significant first (`AA-BB-CC-DD-EE-FF`).**
Not the lowercase colon form. See the rule below, which now covers every place a
MAC appears.

| Key | Type | Value | Note |
| --- | --- | --- | --- |
| `network.neighbor.addresses` | string[] | `["10.10.0.1"]` | the addresses observed answering at this MAC, one element per address, **never** a joined string — the same array form and the same reason as `network.interface.addresses` |
| `network.neighbor.observed.interface` | string | `eth0` | the observing interface's `network.interface.name`. **Host sources only**: a host neighbour is seen on one of the host's own NICs, and naming it saves a traversal. **A device source must not emit it** — a MAC a switch learned is reached through `neighbor_of` to *every* port it was learned on, so an attribute holding one of them picks arbitrarily and hides the case worth seeing: the same MAC on two ports is a loop, a mislabelled uplink, or a machine that moved. **Descriptive and not identifying**, deliberately: a neighbour seen by two NICs of one host is one neighbour, not two, and putting the local interface name in the key would also make `network.interface.name` mean the *observing* interface here while it means the entity's own name everywhere else |
| `hw.vendor` | string | `Cisco` | the OUI owner, when the producer resolves it. The value must be **comparable** (the registry spelling), not the most literal — the rule that already governs `hw.vendor` on a device |

No `first_seen` / `last_seen`. The bi-temporal log already answers both exactly, and
an attribute holding "when I last saw this" changes every cycle, which turns every
neighbour into a modification event per cycle and drowns the change feed.

**Lifecycle: an ARP cache evicts, and an eviction is not a departure.** Three rules,
and the first is the one that matters:

1. **Never retract a neighbour by absence.** An entry missing from a read says
   nothing about the neighbour. This follows ADR 0036 — an inventory is not a sample —
   and a producer that cannot guarantee a read is exhaustive must not let absence
   speak.
2. Declare a **long** `entity.report.interval`, about 24 h, so the liveness backstop
   never expires a neighbour at the cadence of an ARP read. The interval is declared
   per entity, and must match the **slowest** method that reports it.
3. Each collection method emits **its own complete set** for an entity it
   describes. Removal by absence is scoped to one entity's own
   `entity.relationships` array **per collection method**, so a method retracts
   only what it itself asserted before, and two methods describing one entity no
   longer retract each other. Emit what the method observes; do not assemble a
   union across collectors, and above all do not keep one — a cache that
   re-asserts after a restart what a method said last is an observation that did
   not happen, and it refreshes a liveness deadline on evidence that does not
   exist.

**Relation: `neighbor_of`** (neighbour → the observing `network.interface`), read
actively — this neighbour was seen on that interface. It carries **no impact in
either direction**, like `same_as`: an observation is not a failure path. Measured
before this was settled, an edge carrying impact from the interface made `impact_of`
on a host walk `has_interface` and then every `neighbor_of`, reporting each ARP
neighbour as impacted — a host with a few hundred ARP entries would have claimed a
few hundred third-party devices. A printer does not stop working because the machine
watching it died. Losing sight of a segment is a **coverage** question, and it does
not belong in a blast radius.

**Identification later is a belief, never a merge.** When SNMP identifies the device,
or an agent is installed on the machine, the join to its `network.interface` or `host`
is a weighted `same_as` (ADR 0020). Nothing rewrites the neighbour's identity, and
nothing merges the two silently — which is what makes the observer scope safe: a
split is reparable, a wrong merge is not.

**Filter before emitting.** Multicast and broadcast MACs, incomplete entries, and the
addresses that repeat by convention — the range table in this contract is the
**floor, not the test**. The test is the one written there: would this value be the
same on a machine that has nothing to do with this one? Locally administered random
MACs belong in the same bin when they can be told apart.

### Time & liveness — explicit delete + interval backstop

`event_time` = LogRecord `Timestamp`; `recorded_at` stamped by Toise. Liveness uses
**both**: an explicit **`entity.delete`** as the primary signal **and**
`entity.report.interval` (seconds) as a TTL backstop for missed deletes (`kill -9`,
crash, host off, net partition). Toise expires entities not re-asserted within
their interval — **exactly, with no hidden grace** (plus at most one sweep tick).

**Interval sizing (corrected after the #454 flapping incident): the ×3 slack
applies to the EFFECTIVE re-emission cadence, not the heartbeat tick.** The
cadence that matters is the longest gap between two `entity.state` events the
consumer can see; with unchanged-state suppression at 2× the tick, that is the
suppression cadence. Concretely: tick 60 s, suppression 120 s →
`interval = 3 × 120 = 360 s` (absorbs two consecutive missed re-emissions).
Announcing `3 × 60 = 180 s` gave a real slack of ×1.5 and zero tolerance to the
first miss — the mechanical flap of #454.

**Edge liveness is derived from endpoints (decided Q2 = Option A):** deleting an
entity (explicitly or by expiry) **cascades `relation.removed`** for its incident
edges, so the agent does **not** track edge liveness separately — keep the
endpoints alive and the edges live with them. To retire an edge while both
endpoints stay live (e.g. the agent stops monitoring a still-running db), the agent
**drops that descriptor** from the source's next state event; the reconciler removes
it by absence. A *missed* such removal is covered by the **source entity's own
`entity.report.interval`** — no separate per-edge delete or interval exists.

**Multi-producer liveness — reference counting per SPEAKER (Q1, done):** liveness
is reference-counted per **speaker**: the Resource `service.instance.id` *and* the
instrumentation scope it spoke through (ADR 0019 and its 0.21.0 amendment).
Several agents on the same entity converge; an explicit `entity.delete` (or an
interval lapse) by one is a **silent release** — the entity stays live while any
other reference survives, and is deleted only when the **last** one goes. No flap,
and no producer change beyond keeping `service.instance.id` on the Resource (the
agent already does). An observation with no producer counts as one anonymous
producer, and one with no scope keys on the producer alone, exactly as before.

**What that guarantees you, now that the method is part of the key.** A collection
method's silence expires **only its own** references, and a method's retraction
retracts only what that same method asserted. So you may run six collectors at six
cadences over overlapping ground without them deleting each other's facts — which
is the reason the union across methods is no longer asked of you, and the reason
caching one would be wrong: re-asserting after a restart what a method said last
refreshes a liveness deadline on an observation that never happened.

The read surface answers at the same grain: `collected_by` on an entity, and
`via_collected_by` on an edge, name **the set** of methods asserting it — so a link
an adjacency protocol states and a link inferred from a forwarding table are told
apart by a consumer, instead of both arriving as a bare `connected_to`.

**Shared-entity attribute rule (design note, agreed):** a shared entity carries only
**observer-independent** attributes (system name, version…). Anything per-observer
("this agent monitors this db") is a distinct **`monitors` relation** per agent,
never an entity attribute (which would flap last-writer-wins).

### Relayed-span provenance — `telemetry.relay.*` (agreed 2026-07, senhub-agent#698)

An agent that relays third-party spans stamps the relaying agent's context on
them, so a consumer can later join "relayed-by" onto the `host` entity. The
keys are **vendor-neutral by contract rule**: the fact ("this telemetry passed
through this agent on this host") is generic to any collector/agent/gateway,
and a shared-contract key must not require knowing a vendor to read it.
Semconv has no convention for a relaying intermediary (only `telemetry.sdk.*`
/ `telemetry.distro.*`); the need is posed upstream in
[semconv#759](https://github.com/open-telemetry/semantic-conventions/issues/759),
still open. These keys are **provisional** (same posture as the `entity.*`
governance keys): to be co-submitted to the SIG referencing #759, renamed if
upstream settles differently.

| Key | Value constraint |
| --- | --- |
| `telemetry.relay.host.id` | byte-identical to the `host.id` in the relaying agent's `host` entity identity (ADR 0018 — no normalization) |
| `telemetry.relay.host.name` | the relaying host's name (informational) |
| `telemetry.relay.instance.id` | **the same** `service.instance.id` the agent sets on the Resource of its entity emissions (ADR 0019 — the per-producer reference key; divergence breaks the join) |

Rules:

- **First relay wins, atomically.** A hop inserts the **set of three** only if
  **none** of the three is already present (insert-if-absent on the whole set) —
  a downstream hop completing a partial set would mix the first relay's host
  with its own instance and mint a *false* join.
- **Toise does not ingest traces** (entity events only): these keys serve the
  trace backend. A "relayed-by" edge in the graph must be emitted as a
  relationship carried by an entity the agent already emits.
- **What the set can and cannot say.** Both rules are stated as if every hop
  implements the convention. In a mixed pipeline that is not guaranteed, and a
  hop that does not take part leaves no trace, so the next participating hop
  cannot tell that it is not the first. The guarantee is therefore narrower
  than "first relay wins": a present set names the first participating relay,
  which is the first hop only when every hop takes part; and an absent set
  means no participating relay marked the payload, not that the payload was
  never relayed. For liveness, absence has to be read as "relay unknown". The
  two failures are not symmetric: an absent set invites a question, while a
  present set that names the wrong relay invites a conclusion, which is how a
  dead node collector can leave a live relay named in the data of every host
  behind it. This cannot be closed from inside the convention, since a
  participating hop cannot observe a non-participating one, and an ordered
  chain has the same gap. It is not closed so much as compensated, out of
  band, by knowing the deployment topology, which is the very knowledge the
  keys were meant to supply. Credit to @Santoshkumarpuppala for raising it;
  our own pipeline is homogeneous and we had not seen it.
- **A partial set is read as an absence.** The set is atomic by rule, so a
  conformant producer cannot emit one; receiving one means a non-conformant hop
  or a lossy processor touched the payload, and a relaying agent passes it
  through untouched rather than completing or stripping it. Do not use the key
  that is present as a join key: `telemetry.relay.host.id` without
  `telemetry.relay.instance.id` invites a join on the host that misses the
  per-producer key, which is the false join atomicity exists to prevent,
  arriving by another door. This guidance is for the trace backend and for any
  consumer joining relay provenance. Toise is not among them: it ingests entity
  events only and never sees these keys, so it has neither handled this case nor
  been exposed to it.

The **"What the set can and cannot say"** bullet is kept **byte-identical** to the body of
[semconv#4024](https://github.com/open-telemetry/semantic-conventions/issues/4024).
Two wordings of one fact in two documents is where the next divergence starts:
amend both or neither. The bullet that follows it is ours alone: it tells a
reader how to read a partial set, which the convention governs for writers and
leaves unsaid for readers.

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

### A fact whose two halves have different emitters

Some relations point at an entity the emitting producer does **not** itself
produce. The edge comes from one probe and its target from another — sometimes on
another host, sometimes behind a probe that is off by default. Three in this
contract are like that today, and a reader should not have to discover it:

| relation | emitted by | its target comes from |
| --- | --- | --- |
| `attached_to` (container→segment) | the container probe, on the workload's host | the cluster probe, on a manager |
| `connected_to` (port↔port) | the poll of the device whose table was read | the poll of the device at the far end |
| `resolves_to` (neighbor→interface) | the observer that heard the address | whoever polls the device owning that interface |

**The rule that binds a single emitter does not apply to these.** "Emitted with
its edge or not at all" is honourable when one producer owns both halves; it is
not actionable when it owns one, because **it cannot know whether the other
emitter is running**. Telling it to stay silent loses a real observation — the
container genuinely is attached to that network. Telling it to emit regardless is
what this contract means.

So, for a cross-emitter relation:

- **Emit the edge.** A half-observed fact is still an observation, and the
  reconciliation buffer exists for exactly this: an edge waits for an endpoint
  that has not arrived yet.
- **Spell the target in the frozen identity form, byte for byte.** This is the
  load-bearing requirement. The two halves are joined by nothing except the
  identity matching exactly, and an invented or near-miss spelling produces two
  things joined by nothing — discovered months later, if at all.
- **The obligation to run the other emitter belongs to the DEPLOYMENT, not to the
  producer.** If a cluster has overlay networks, some agent in that cluster must
  emit the segments. No per-producer rule can substitute for that, and this is
  the sentence that was missing.

!!! warning "An unmet obligation is discarded, not stored"
    An edge whose endpoint never arrives is dropped when the reconciliation
    window closes. A deployment that emits one half forever therefore produces a
    permanent shortfall, re-made and re-discarded on every cycle. Measured in
    production: **47,327** `attached_to` edges discarded in ten days, every one
    of them, because the probe that emits the segment was enabled nowhere — the
    identifier existed the whole time and nothing was unidentifiable.

    The graph shows nothing of this, which is Toise's own defect and tracked as
    toise#508. Until that is answered, the only trace is a `Warn` line on the
    Toise side, so **a deployment enabling one half of a pair should verify that
    the other half arrives** rather than assume it.

### Topology as entities — edges stay bare (ADR 0022)

Toise's engine is a **faithful, facts-only store** of the OTel entity-events
standard (ADR 0022), and the embedded relationship model carries **no edge
attributes**. So anything a producer would have hung on an edge becomes an
**entity** instead:

- **Ports are entities.** A port is a `network.interface` entity
  (`{network.device.id, network.interface.name}`, with `oper_state`/`speed` as attributes),
  linked by `has_interface` (device→port); physical adjacency is a **bare
  `connected_to`** (port↔port) — never `adjacent_to` carrying `{local_port,
  remote_port}`.
- **A link asserted on one-sided evidence says so, on the local interface.** Each
  device knows only its own ports, so a `connected_to` is emitted in the direction
  that *leaves* the port whose table was read — `A→B` and `B→A` are two separate
  facts with their own speakers, and Toise never synthesises the reverse. When the
  remote end could not be read, the link is still emitted (a local port that is up
  is a real observation), and the local `network.interface` carries
  **`senhub.link.peer_state`**: `unread` (not polled in this pass), `stale` (read,
  but too old to decide) or `unreachable` (the remote device did not answer).
  **Absence means both ends were read**, which is the healthy case and therefore
  costs nothing. Three words rather than a flag, because an operator does not act
  the same way on a device never polled, one polled too long ago, and one that has
  stopped answering.

  Staleness is counted in the **remote device's own poll cadence**, not in a fixed
  window: a device polled every ten minutes would otherwise cross into `stale`
  between two good polls and the attribute would oscillate every cycle, which is
  entity noise rather than information.

  **The word is on the interface, which is one hop before the edge.** An edge
  cannot carry it: ADR 0022 refuses attributes on relations except the belief
  allowlist (`same_as`, `resolves_to`), so an attribute set on a `connected_to` is
  accepted on the wire and dropped on arrival, silently. A consumer judging the
  evidence behind a link therefore reads the **From** interface, not the edge.
> **Interface identity is migrating.** The identifying key is
> `network.interface.name` — the semantic-convention spelling, release candidate
> since semconv v1.44.0. Producers before senhub-agent 0.6.0 emit `interface.name`,
> and the two are **different identities**, so an interface is re-minted once when
> its producer migrates: the old entity expires by liveness, the new one appears.
> That is expected and it happens once. Toise accepts both spellings as telemetry
> join keys and as display names for as long as the retention window holds
> pre-migration observations.

- **Routes are entities, identified by where they point.** A routing-table entry is
  a `network.route`, identity **`{host.id, route.destination, next_hop.ip}`** on a
  host and **`{network.device.id, route.destination, next_hop.ip}`** on a polled
  device (the destination as a canonical CIDR, e.g. `10.20.0.0/16`), linked by
  **`has_route`** (host/device→route). `metric`, `route.protocol` and the egress
  interface — spelled **`network.interface.name`**, the same key the interface
  entity is identified by — ride as descriptive attributes. `next_hop.ip` is
  identity and is **not** repeated as an attribute. `next_hop_via` (route→address)
  and `bound_to` (interface→address) link to the shared `network.address`, subject
  to the rule below.

  **The next hop is part of the identity and the egress interface is not**, and that
  is not a house convention: it is what the MIB describing this exact object has said
  since RFC 2096. `ipCidrRouteEntry` indexes on `{ipCidrRouteDest, ipCidrRouteMask,
  ipCidrRouteTos, ipCidrRouteNextHop}` and `inetCidrRouteEntry` on the same shape
  plus the policy, while both define the interface as an ordinary column —
  `inetCidrRouteIfIndex` states that "a value of 0 is valid and represents the
  scenario where no interface is specified". An identifying attribute must always be
  present, so an attribute the source itself says may be absent cannot be one.

  Without the next hop in the identity, a host with two uplinks — two NICs, or a VPN
  — has two `0.0.0.0/0` entries differing only by gateway and metric, and they
  collapse into a single entity whose next hop oscillates at poll cadence. That reads
  downstream as a real topology change, and it is not one.

  > **This identity and the inventory rule were load-bearing for each other, and
  > the inventory rule has now changed.** It used to read: a direct, on-link route
  > has no next hop at all, so the key would be absent, and it is always present
  > only because a producer emits **indirect routes only** — whoever extends the
  > inventory to direct routes breaks the identity and must revisit both rules
  > together. That warning was written for exactly the decision taken on
  > 2026-10-14, and it did its job: the extension is specified below, with the
  > third key resolved rather than dropped.

#### Connected, rejected and discarded routes — one encoding, three meanings

**A route with no next hop carries `0.0.0.0`, or `::` for IPv6**, so the identity
stays three keys and a type never has two identity shapes. The value is not
invented: `ipCidrRouteNextHop` is defined as the address of the next system on a
remote route **and `0.0.0.0` otherwise**, which is the same reason the next hop is
an identity key at all — we take the encoding the MIB describing this object
already uses.

**And `route.type` is then mandatory, because three different things share that
encoding.** A connected route, a `reject` route and a `blackhole` route all lack a
next hop. Without the type, a blackhole to `10.0.0.0/8` is drawn as *directly
connected* while the traffic is **discarded** — a drop rendered as a connection,
which is the error class this model spends its effort removing.

| `route.type` | code | means | traversable |
|---|---|---|---|
| `local` | 3 | directly connected, on-link | **yes** |
| `remote` | 4 | reached through the next hop | **yes** |
| `reject` | 2 | administratively refused (`unreachable`, `prohibit`) | **no — a deliberate end** |
| `blackhole` | 5 | silently discarded | **no — a deliberate end** |
| `other` | 1 | the source says other | no |

`reject` and `blackhole` are **ends of a path, not gaps**. A gap means *we do not
know*; a blackhole means *we know, and it stops here*. Two absences a drawing must
not confuse.

Projections: SNMP `inetCidrRouteType` directly, and `ipCidrRouteType` (RFC 2096)
which has no `blackhole`; Windows `dwForwardType` 3 → `local`, 4 → `remote`,
1 → `other`, and 2 (`invalid`) is **not a route** and counts as omitted;
Linux `rtm_type` in the main table, `RTN_UNICAST` with a gateway → `remote`,
without → `local`, `RTN_BLACKHOLE` → `blackhole`, `RTN_UNREACHABLE` and
`RTN_PROHIBIT` → `reject`.

**That last projection loses a real distinction** — administratively prohibited is
not the same as unreachable — and the loss is accepted because `inetCidrRouteType`
cannot express it and inventing a sixth value would put us outside the convention
for a nuance nobody consumes. **No raw code is carried for the type**, and the
asymmetry with the protocol below is deliberate: a projection that can *fail*
keeps the raw value, a projection that *coarsens into a declared vocabulary* does
not. Adding one for symmetry would be the wrong reason to add a field.

#### `route.protocol` — a closed vocabulary, and a raw code when it fails

`IANAipRouteProtocol` (IANA-RTPROTO-MIB), lowercase, underscore between words,
conventional spelling for acronyms:

| code | value | | code | value |
|---|---|---|---|---|
| 1 | `other` | | 11 | `cisco_igrp` |
| 2 | `local` | | 12 | `bbn_spf_igp` |
| 3 | `netmgmt` | | 13 | `ospf` |
| 4 | `icmp` | | 14 | `bgp` |
| 5 | `egp` | | 15 | `idpr` |
| 6 | `ggp` | | 16 | `cisco_eigrp` |
| 7 | `hello` | | 17 | `dvmrp` |
| 8 | `rip` | | 18 | `rpl` |
| 9 | `isis` | | 19 | `dhcp` |
| 10 | `esis` | | 20 | `ttdp` |

The list is **enumerated rather than derived**. "The IANA names, lowercased"
sounds like a rule that saves writing them out; it is not, because the MIB spells
`isIs` and `ciscoIgrp` in camel case and the transposition needs a judgement per
value.

Projections: SNMP and Windows (`dwForwardProto`) carry the IANA code directly.
Linux `rtm_protocol`: `kernel` → `local` (**the most common route on any host** —
the kernel adds it from an interface's own address, which is literally IANA's
"local interface"), `static` → `netmgmt`, `dhcp` → `dhcp`, `ra` → `icmp`, and
`bgp`, `ospf`, `isis`, `rip`, `eigrp` → their own values. `boot`, `babel`,
`zebra`, `bird` and the rest have **no IANA value**.

**`network.route.protocol.code`** carries the source's raw integer, always. When
the projection has no name for it, **`route.protocol` is absent and the code is
present** — three distinct states, and the middle one is the useful one:

| state | means |
|---|---|
| attribute absent, no code | the producer did not look |
| name present | it looked and can name it |
| name absent, code present | it looked, saw something, and cannot name it |

`other` is **never** the bucket for a failed projection: it is the MIB's own value
meaning "other". Conflating the two would make the distinction unrecoverable in
six months.

#### The egress interface — an attribute, and no relation

`network.interface.name`, the same key the interface entity is identified by, so
with the route's owner it is a **complete identity** a consumer resolves exactly.
It is **not** a relation, and the reasoning is recorded because it was decided the
other way first.

A relation `egress_via` (route→interface) was specified for half a day and
withdrawn after review. Two arguments were given for it and neither survives:

- *"a traversal needs a hop, not a resolution."* It confused two different walks.
  A shortest-path search would never cross it: the egress interface belongs to the
  route's **own owner** and is already one hop away by `has_interface`, so
  `owner → route → interface` is two hops and loses. And the walk that genuinely
  asks the question — replaying a forwarding decision, which asks *from the
  route* — is engine code that can resolve a complete identity directly.
- *"a dangling attribute is silent, a dangling edge is visible."* It is not
  silent. A path resolving the attribute and finding no such interface **reports a
  gap at that position**, which is the rule everywhere else in this model. The
  edge would have made the dangling case expire quietly instead.

What stands against it: the vocabulary grows for a fact already carried exactly,
and two representations of one fact without an arbiter is the defect this release
spent itself removing. Three relations were proposed in one day; this is the one
that was not needed.

**Do not emit the attribute on a `reject` or `blackhole` route, even when the
source gives one.** Linux often shows `dev lo` on a prohibit route; that is not "packets
leave by `lo`", it is how the kernel represents a drop. Emitting it draws a hop to
the loopback — a false claim built from a true observation. This is the one place
the contract asks a producer **not** to emit what its source says.

#### Bounding the inventory, and declaring the bound

A full BGP table must **not** be inventoried route by route. The bound is not a
performance knob: it is the admission that this model does not fit that device.

- **`senhub.routes.omitted`** (int) and **`senhub.routes.limit`** (int) on the
  **owner**, `host` or `network.device`, **always present**, zero when nothing was
  left out. Absent would mean *the producer did not say*, which is a third thing.
- Connected routes and the default route are **always kept**; the remainder is
  ordered deterministically.
- `omitted` counts every route observed and not emitted — the bound *and* lines
  skipped for any other reason, such as a Windows `invalid` entry. A counter that
  excludes one kind of omission lies.

They are `senhub.*` and not `network.*` because the completeness of our inventory
is **our** notion, carried by an entity whose namespace is upstream's: a semconv
namespace must not prefix application-specific keys, which is the rule the
`entity.*` → `senhub.*` migration paid for in 0.20.0.

#### Does this machine forward? — `senhub.host.ip_forwarding.*`

**`senhub.host.ip_forwarding.ipv4`** and **`senhub.host.ip_forwarding.ipv6`**,
booleans on the `host`, each **absent when its switch could not be read** — a fact
not read is never false.

**Two booleans and no aggregate**, because the question is asked per family: a
path's family is known from its destination, and a host forwarding IPv4 but not
IPv6 would otherwise be crossed on an IPv6 path using IPv4 evidence. A consumer
wanting "does it route at all" takes the disjunction itself, knowing it took it.

It exists because the graph says nowhere whether a node forwards, and a traversal
needs exactly that: two interfaces of a **router** are connected through it, two
interfaces of a **host** are not unless it forwards, and `has_interface` is the
same relation for both. Without the fact, a dual-attached machine — a backup VLAN,
an out-of-band NIC, a storage network — is rendered as a transit between segments
that were deliberately separated.

Sources: `/proc/sys/net/ipv4/ip_forward` and
`/proc/sys/net/ipv6/conf/all/forwarding` on Linux, `MIB_IPSTATS.dwForwarding` per
family on Windows.

**A known limitation, with the reason it is tolerated**, so it does not get
re-examined every six months: Linux IPv6 forwarding is per interface and `all` is
the global switch, so a host forwarding on one interface only reads `all` as 0 and
is taken for a non-router. **The error goes in the safe direction** — the
traversal then refuses the transit and *names* it at that position, which is wrong
and honest. The opposite error would draw a false path between separated segments
and show nothing.

#### Link aggregation — `aggregated_into`

The addresses, the routes and therefore the egress interface sit on the **logical**
interface (`bond0`, `Port-Channel1`), while LLDP and the forwarding table see the
**physical** members. Without an edge between them a path stops at every
aggregate, which means at every network core and every hypervisor.

**`aggregated_into`**, physical `network.interface` → logical `network.interface`.
Part to whole, like `bound_to` and `attached_to`. **Structural** — a port leaving
a port-channel is a degradation operators alert on, and membership does not churn.
**`ImpactNone`**, and for a measured reason rather than caution: a member failing
does **not** take the aggregate down, that being the entire point, so propagating
would assert the false alarm the aggregate exists to prevent. The real impact is
conditional on being the *last* active member, and the impact walk works on edges
without counting them.

- **`senhub.interface.aggregation.state`** on the member, **only where the concept
  exists**: LACP (from the actor state — collecting and distributing) and
  `active-backup`. On a static port-channel there is no standby notion and the
  attribute is **absent**. It is not filled with `active`, which would then mean
  both "LACP says it forwards" and "there is no standby here".
- **`senhub.interface.aggregation.mode`** on the aggregate, always: `lacp`,
  `static`, `active-backup`. It is what tells a reader how to read an absent
  state — `static` plus absent means no standby notion, `lacp` plus absent means
  the producer could not read it and that is a gap.
- Traversal: **cross a member unless its state says `standby`.** Not "cross active
  members", which would cross nothing on a static aggregate — most of them.
- Crossing an aggregate does **not** multiply the path by its member count. Two
  members to one switch are one path and two links; two to two switches (MLAG) are
  genuinely two paths. `connected_to` downstream says which.

**The borrowed MAC.** A bond takes the address of one of its members, so the
logical interface and a physical member present the same MAC and `resolves_to` —
which points at *the interface that owns the MAC* — becomes ambiguous. Not an
identity problem — the MAC is descriptive, and an interface is identified by its
owner plus its name — but a **join** problem, and an ambiguous join produces a wrong match months later.

**Resolve to the interface whose MAC is its own permanent hardware address — the
physical member.** `IFLA_PERM_ADDRESS` on Linux, `PermanentAddress` on Windows,
distinct per-vmnic addresses on ESXi. SNMP has no permanent-address notion, so the
**structure** decides: when `ifStackTable` places an `ieee8023adLag` interface
above a member and both carry the same `ifPhysAddress`, the owner is the member.
When two **non-aggregated** interfaces carry one MAC — some chassis reuse a base
address across routed ports — it is undecidable and **no `resolves_to` is
emitted**. One identity and a half is not two.

  **A gateway change is a disappearance followed by an appearance**, not an attribute
  change — which is what actually happened: the route through A is gone, a route
  through B exists. Consumers reading an incident get the two events rather than one
  mutated field.

  **Documented boundary.** The MIB index also carries TOS on the older table and the
  policy on the newer one. A producer reads neither, so two routes differing only by
  TOS or policy collapse into one entity here. That is a known limit, stated so that
  a reader does not mistake it for a loss.

  **What a route inventory does not contain.** So that "no route to X" can be read
  correctly, here is the complete list of what is absent by design.

  Connected routes are **in** the inventory, since 0.6.4 — the paragraph that used to
  stand here said "indirect routes only" and it no longer holds. What is omitted is
  loopback, **the polled device's own management address** (that row describes the
  device reaching itself, not a path through another device), and **a connected route
  whose destination no path could target**.

  That last exclusion is a rule and not a list, because a list drifts. A connected
  route is inventoried only when its destination is a prefix a path could be aimed
  at. Link-local (`fe80::/64`, `169.254.0.0/16`), multicast (`224.0.0.0/4`,
  `ff00::/8`) and limited broadcast (`255.255.255.255/32`) are therefore out: none of
  them can be the far end of a path from A to B, and each appears **once per
  interface**, so inventorying them would collide several routes of one owner into a
  single entity whose egress interface oscillates at poll cadence — exactly the
  flapping that putting the next hop in the identity was introduced to remove.

  Everything omitted here counts in `senhub.routes.omitted`. Exact
  `(destination, next hop)` repeats are collapsed, which on a device is normally the
  same route appearing in both `ipCidrRouteTable` and `inetCidrRouteTable`. Nothing
  else is dropped: two distinct next hops are two distinct routes and both are
  emitted, so an equal-cost pair is visible rather than arbitrated.

  > **A residual collision, named rather than fixed.** Two *connected* routes to the
  > same **routable** prefix on two interfaces of one owner still collapse: the
  > identity is `{owner, destination, 0.0.0.0}` for both, and the egress interface is
  > an attribute, so it oscillates. A host with two NICs on one subnet is the case.
  >
  > It is not fixed because every fix costs more than the defect. The egress
  > interface cannot become a fourth key for all routes — the MIB states that an
  > interface index of 0 is valid and means none specified, and a key that may be
  > absent is not a key. Making it a fourth key for connected routes only would give
  > one type two identity *arities*, which every consumer would then have to handle.
  > And putting the interface in the third key when there is no next hop would make
  > that key polymorphic: a reader could not tell whether `eth0` is a next hop or an
  > interface.
  >
  > So it stays, it is written here, and the bench's quiet-window criterion is what
  > would tell us the case is real: `graph_diff` over a window where nothing changes
  > must report nothing. If it reports this, we revisit with a measurement instead of
  > a guess.

  Read together with the TOS/policy collapse above, that means the absence of a route
  says: no route to that destination was observed, among those this inventory
  contains. It does not say the destination is unreachable, and it never says a route
  was removed.
- **A bare IP is not a globally unique identity — do not link the ones that
  repeat.** `network.address` is identified by the address alone, so two producers
  emitting the same text land on the same entity. That is correct when the value
  really does name one thing across the fleet — a routable gateway shared by the
  machines behind it — and **wrong when the value repeats by convention on
  unrelated machines**. The same `172.17.0.1` exists, independently, on every host
  running Docker. Linking each of them to one shared entity makes the graph assert
  a neighbourhood that does not exist, and the engine cannot tell that edge from a
  real one afterwards.

  So a producer **MUST NOT** emit `next_hop_via` or `bound_to` toward a bare-IP
  `network.address` when the address repeats by convention on unrelated machines.
  **The ranges below are a floor, not the test.** They are the cases known to
  repeat, listed so nobody has to rediscover them; a range absent from the table
  is not thereby allowed.

  | range | why it repeats |
  |---|---|
  | `0.0.0.0`, `::` | wildcard, names no host |
  | `127.0.0.0/8`, `::1` | loopback, present on every machine |
  | `169.254.0.0/16`, `fe80::/10` | link-local (RFC 3927 / RFC 4291), scoped to one link |
  | `172.17.0.0/16` | Docker's default bridge, the same gateway on every Docker host |
  | pod and overlay networks | a cluster CNI gives every node the same bridge gateway: `10.42.0.1` on k3s/flannel `cni0`, a Calico or Cilium block, a non-default Docker bridge on `172.18+`. The addresses differ per distribution, so no range can be enumerated here |

  **The test, which the table only approximates: would this value be the same on a
  machine that has nothing to do with this one?** The discriminator is **not** "is
  it private". A private gateway that several machines genuinely share —
  `10.10.0.1` for the VMs behind one hypervisor bridge — is exactly the case this
  relation exists to express, and must be linked.

  A producer that can see which interface a route leaves by has a second,
  independent filter available, and it is not redundant with the table: a gateway
  reached through a container bridge, a virtual switch, a `veth`, `cilium` or
  `vEthernet` interface is local to its host whatever its address. Where both
  filters are available, apply both.

  **On a polled network device that second filter does not exist, so the rule is
  narrower there.** A switch's interfaces are all real network interfaces:
  `Vlan1`, `Port-channel1`, `mgmt0` tell a reader nothing about whether a next hop
  is locally scoped, and the MIB does not either — `ipCidrRouteType` /
  `inetCidrRouteType` distinguish local from remote destinations, not shared from
  private next hops. A device route therefore links **only a next hop that is
  public and routable**, which can name one machine in the world; a private next
  hop keeps `next_hop.ip` as a descriptive attribute and gets no edge.

  **This is a deliberate asymmetry and a reader must not mistake it for a fact.**
  The same private gateway can carry an edge from a `host` — `10.10.0.1` behind one
  hypervisor bridge is exactly the case the relation exists to express — and carry
  none from a switch that routes through it too. The graph then understates the
  device's path rather than asserting a false one, which is the trade this contract
  takes everywhere: a missing edge is a gap a reader can see and ask about. So "the
  device has no route via that gateway" **cannot** be read off a missing
  `next_hop_via`; read `next_hop.ip` on the route.

  **When in doubt, do not emit the relation.** A missing edge is a gap a reader can
  see and ask about; a wrong edge is a false statement that reads exactly like a
  true one. Per ADR 0018 the engine matches identity byte-exactly and never merges
  on a guess — which also means it cannot un-merge what a producer asserted.

  Scoping a host-local address, rather than dropping it, is a live design question
  (the `vlan:` scope left open by ADR 0034 is the same shape) and is **not** decided
  here. Until it is, silence is the honest answer.

  **The range table is necessary and not sufficient — filter by the owning interface
  too.** The table catches Docker's default `172.17.0.0/16` and misses every
  user-defined Docker network, which lands on `172.18.0.0/16`, `172.19.0.0/16` or a
  custom range and is indistinguishable by address alone. The owning interface is
  not: `docker*`, `br-<12 hex>`, `virbr*`, `cni*`, `cbr*`, `flannel*`, `lxcbr*`,
  `kube*`, `cali*`, `antrea*`, `weave*` and `ovs-system` all name a host-local
  virtualization bridge. A producer knows which interface a route leaves by and which
  interface an address is bound to; a consumer reading only the address does not.
  That asymmetry is the whole reason this rule lives in the producer.

  A route leaving by such an interface keeps `network.interface.name` and its next
  hop — the fact is true and worth recording — and emits **no** shared
  `network.address` and **no** `next_hop_via`.

  **Match on `br-` with the hyphen, never on `br`.** Plain `br0`, `br1`, `bridge0`
  and `vmbr0` are ordinary routed bridges on hypervisors and routers. Their gateway
  is a genuinely shared address and must stay linked — it is often the only thing
  joining two machines in the graph.

  **Known limitation.** OpenWrt names real routed bridges `br-lan`, `br-wan` and
  `br-guest`, which match the `br-` prefix. On such a device this rule suppresses an
  edge that should exist. We accept it, for the reason above: a missing edge is a gap
  a reader can see and ask about, a false one is not. A deployment for which it
  matters needs a discriminator based on the address's reachability rather than on
  the interface's name, and that is not decided here either.

- **Address canonicalization is frozen** (identity is byte-exact, so text form IS
  identity): `route.destination` is CIDR with the prefix **always** explicit
  (`/32` and `/128` included), host bits zeroed (`10.20.3.0/24`, never
  `10.20.3.7/24`), default routes `0.0.0.0/0` / `::/0`. **Every IPv6 address in
  any identity key** is RFC 5952 text form (lowercase, single `::` compression);
  zone indices are kept verbatim lowercased, never fabricated when absent. This now
  binds `next_hop.ip` too: it became an identity key, so its text form is its
  identity and an uncanonicalized gateway mints a second entity for one route.
- **Provenance → instrumentation scope.** Which collection method observed a fact
  rides on the **instrumentation scope** — **one scope per source**
  (`senhub-agent/snmp-lldp`, `senhub-agent/snmp-route`, `senhub-agent/snmp-fdb`, …),
  not a `source` attribute on the entity or edge.
- **`senhub.device.role`** (e.g. `switch`, `router`) is an **optional descriptive**
  attribute (never identity); infer best-effort from `sysServices` (L3 bit → router,
  L2 → switch) and omit when ambiguous. It was spelled `device.role`, which squatted
  semconv's `device.*` namespace — reserved there for the end-user device a telemetry
  SDK runs on, not for a polled network asset. Same rename, same reason as the
  governance keys above; the old spelling is still readable in older data.
- **`hw.vendor` carries a comparable value, not the most authoritative one.** A
  device can state its manufacturer two ways: `entPhysicalMfgName` from ENTITY-MIB,
  which is what the device says about itself, and the enterprise that registered
  its `sysObjectID` branch, resolved through the IANA enterprise number. The second
  is the normative source, and **not** because it is truer — the first is arguably
  more authoritative. Because it comes from a controlled registry, every device of
  one vendor renders the identical string, which is the whole point of the key.

  Nobody reads `hw.vendor` to display it alone; they read it to group and filter.
  A free-form string typed by a manufacturer gives `Cisco Systems, Inc.` on one
  device and `Cisco` on another, so "show me my Cisco estate" returns half of it
  and the reader concludes they own less than they do. That is the same defect as
  two keys for one fact, moved from the key to the value.

  So: **`hw.vendor` is the short lowercase name derived from the IANA enterprise
  number** (`cisco`, `juniper`, `arista`, `dell`, `extreme`, `mikrotik`, …).
  ENTITY-MIB is the **fallback only**, used when the enterprise number is unknown.
  The device's verbose self-description keeps its own home in `sys.descr`, so
  nothing is lost.

  **Documented boundary:** the enterprise-number-to-name mapping is a finite table
  in the producer. A vendor absent from it falls back to the ENTITY-MIB string, so
  its value has a different shape from its neighbours'. That is accepted, on the
  condition below — which is a mechanism, not a recommendation.

  **The fallback is visible in the data, not in a log.** Beside `hw.vendor` the
  producer emits the descriptive key **`senhub.hw.vendor.source`**, frozen to two
  values: `iana_pen` when the value came from the enterprise number, `entity_mib`
  when it came from the fallback. "Which of my devices carry a vendor value I
  cannot group on" then becomes a graph query, and the answer is historized like
  any other attribute.

  An earlier version of this section required only that an unresolved number be
  **visible** — "counted, or warned once per number". Nothing satisfied it and
  nothing contradicted it: the fallback ran for a month on two production devices,
  wrong in two different ways, and no counter and no warning ever fired (#557). The
  requirement described a witness that no mechanism implemented, which is how a
  defect stays invisible next to its own description. A counter still helps and is
  **not** the requirement, because a counter obliges someone to look, while an
  attribute answers when asked.

  **The key is `hw.vendor`; the bare key `vendor` is forbidden.** `vendor` was
  retired, and this section never named it — which is how a producer went on
  emitting it without contradicting anything written here. A value form is fixed
  only if the key carrying it is fixed too: a correct value on a retired key is
  unreadable to anyone who follows the contract, and a reader who filters on
  `hw.vendor` simply does not see that device.

  The same rule governs `hw.model` and `hw.serial_number` on this entity the day a
  producer emits them from more than one source. Today ENTITY-MIB is their only
  source, so no form conflict exists yet.

- **Descriptive key casing is dotted lowercase** (`sys.name`, `mgmt.ip` — not
  `sys_name`). Toise does not validate descriptive keys, but follow this for
  cross-producer consistency.
- **State-bearing keys are a fixed recognized set with exact spellings** the change
  engine matches: `oper_state`, `admin_state`, `status`, `replication.role`,
  `read_only`, `hw.state`, `ha.role` (note the **underscore** forms — they predate and override
  the dotted convention). A change to one of these classifies as `entity.state_changed`; emit
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

## One export has a size cap, and splitting a snapshot is safe

`otlp_max_recv_bytes` caps one OTLP/gRPC export and defaults to gRPC's 4 MiB.
Past it the export is refused **whole**, with `ResourceExhausted`, and nothing
partial lands.

**The refusal is visible to the producer only.** It happens at the transport,
before the ingest handler, so Toise neither counts it nor logs it (#556). A
reader of Toise sees entities missing and no reason given — this server will not
tell anyone that an export was refused. Size exports on the producer side and
watch for `ResourceExhausted` there; this paragraph used to say "nothing is
silent", which was true of the producer and false of everyone reading the graph. A 5,000-host inventory is around 10 MB, so an
estate of that size cannot send its snapshot in one export (#351). Either raise
the cap deliberately, remembering that one export is buffered in memory per
in-flight request, or split.

**Splitting between entities is safe. Splitting within an entity is not.**

An entity **omitted** from an export is not deleted. Absence removes nothing at
the entity level: an omitted entity expires only if no producer reasserts it
within its declared `entity.report.interval`, and that expiry carries
`liveness_expiry`, never `producer`.

What absence removes is **edges**, and only for an entity the export *does*
re-emit. The `entity.relationships` array is the sole on-wire relationship form,
and removal is by absence on re-emit. So an entity you send must carry its
**complete** array, or you delete the edges you left out of it.

**Complete for the collection method that sends it**, which is the grain the diff
is keyed on: the instrumentation scope, the producer and the entity. A method
retracts only what that same method asserted before. That is the third spelling
of one key — keyed by entity alone, a producer that merely mentioned an entity
deleted another producer's edges (#396); keyed by producer, a producer running
several methods had each method delete the others' (#455) — so if you add a
fourth source of truth about an entity, ask first what distinguishes it from the
other three.

An **absent** array is not a reservation. It is read as an empty one, so it
retracts everything that method had asserted on that entity. There is no way to
say "I am not speaking about relationships this cycle"; send what you observe.

Two rules follow, and the second is the one that bites at scale:

- split **between** entities, never **within** one;
- land every part within the **shortest** `entity.report.interval` in the batch,
  or the entities you have not sent yet expire while you are still sending.

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
