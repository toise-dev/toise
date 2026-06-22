# Data Model

This document introduces the Toise data model as it stands in **phase 1**. It is
the conceptual overview; the formal, authoritative contract is the Protocol
Buffers definition in [`proto/toise/v1/events.proto`](../../proto/toise/v1/events.proto)
(package `toise.v1`).

## Alignment with OpenTelemetry

Toise is OpenTelemetry-native. Its data model aligns with the
[OpenTelemetry Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
so that Toise entities slot into an existing OTel deployment rather than
introducing a parallel vocabulary, and so that Toise interoperates with the
broader OTel ecosystem (see
[ADR 0004](../architecture/adr/0004-data-model-aligned-with-otel-entities.md)).

In OTel terms, Toise tracks **entities** (things that exist), the **attributes**
that describe them, and the **relations** that connect them. Toise adds the
dimension that OTel signals (metrics, logs, traces) do not carry on their own:
the live topology and inventory of the infrastructure those signals come from,
together with how that topology changes over time.

## Entities

An **Entity** has four parts:

- a **`type`** — a string id such as `host` or `process`;
- an **identity** — a set of identifying key/value attributes whose values
  together uniquely identify the entity;
- a set of **descriptive attributes** — informational, non-identifying metadata;
- a **`schema_url`** — versions the entity definition.

Attribute values are a typed **`Value`**: a `oneof` over `string`, `int64`,
`double`, and `bool`. This is a deliberate subset of OTel's `AnyValue` — it keeps
the internal model independent of OTLP wire types. Translation from OTLP
`AnyValue` happens only at the ingest boundary (see
[otel-mapping.md](otel-mapping.md)).

## Entity identity

Toise carries **two distinct identity concepts** that must not be confused (see
[ADR 0017](../architecture/adr/0017-entity-identity-and-stability.md)):

- **Logical entity ID** — the stable identifier of an entity across its whole
  life, even as its identifying attributes evolve. It is a surrogate **ULID**
  assigned by Toise on first sight of the entity, and it survives identity
  changes (an `entity.identity_changed` event keeps the same logical ID). This
  is what consumers reference, and what relation endpoints (`from`/`to`) point
  at.
- **Identity hash** — a deterministic fingerprint of the *current* set of
  identifying attributes: SHA-256 truncated to 128 bits, hex-encoded, prefixed
  by the entity type (for example `host:1a2b...`). The identifying set is
  canonicalized — keys sorted, each value type-tagged so that the string `"1"`
  and the integer `1` hash differently. The hash powers O(1) idempotent ingest
  and fast lookup, and it **changes** when an identifying attribute changes; the
  logical entity ID does not.

## Relations

A **Relation** is a typed, directed edge between two entities, identified by
their logical entity IDs (`from`, `to`). It carries optional attributes and a
**`structural`** boolean flag. The flag marks whether the relation's appearance
or disappearance is significant (structural) rather than merely descriptive (see
[ADR 0004](../architecture/adr/0004-data-model-aligned-with-otel-entities.md)).

## Bi-temporality

Every event carries **two timestamps**, and they are not interchangeable (see
[ADR 0005](../architecture/adr/0005-bi-temporal-event-model.md)):

- **`event_time`** — when the fact became true in the real world, supplied by
  the producer (e.g. senhub-agent or any OTel producer) from its source.
- **`recorded_at`** — when Toise recorded the event, stamped by toise-server at
  ingestion.

For a late or retroactively corrected event, `event_time` is significantly
earlier than `recorded_at`; that gap is the signal, not noise.

Queries default to **`event_time` space — the "reality view"** — Toise's best
current knowledge of what actually happened. Knowledge and audit queries opt in
via an **`asKnownAt`** parameter — the **"audit view"** — which constrains the
query to events whose `recorded_at <= t`, reconstructing what Toise knew at that
past moment. Every event always exposes both timestamps.

## Change taxonomy

The change-detection engine classifies every ingested event into **exactly one**
of nine change types (see
[ADR 0006](../architecture/adr/0006-change-taxonomy.md)):

| Change type                   | Meaning                                                                 |
| ----------------------------- | ----------------------------------------------------------------------- |
| `entity.created`              | The entity did not exist before this event.                             |
| `entity.deleted`              | The entity existed and is now soft-deleted; the log retains everything. |
| `entity.identity_changed`     | An identifying attribute mutated — anomalous, logged at high priority.  |
| `entity.attribute_updated`    | A descriptive (non-identifying) attribute changed.                      |
| `entity.state_changed`        | A state-flagged attribute (`oper_state`, `admin_state`, `status`) flipped. |
| `entity.unchanged`            | A heartbeat: the entity is alive but nothing changed.                   |
| `relation.added`              | A new edge appeared.                                                    |
| `relation.removed`            | An edge disappeared.                                                    |
| `relation.attribute_changed`  | Edge metadata mutated.                                                  |

## Phase-1 entity types

| Entity type         | Description                                                  |
| ------------------- | ------------------------------------------------------------ |
| `host`              | A compute host (physical server, VM, or hypervisor guest).   |
| `process`           | A running process on a host.                                 |
| `network.interface` | A network interface on a host.                               |
| `network.address`   | A **globally-unique** IP address, typically bound to an interface. Host-local / non-routable IPs (loopback, link-local, the Docker bridge gateway `172.17.0.1`) are **not** identities — never a shared entity (see otel-mapping). |
| `network.route`     | A routing-table entry / forwarding decision.                 |
| `service.listener`  | A service endpoint listening on an interface and port.       |

## Phase-1 relation types

| Relation type   | From → To                                | Structural | Notes                          |
| --------------- | ---------------------------------------- | ---------- | ------------------------------ |
| `runs_on`       | `process` → `host`                       | yes        |                                |
| `has_interface` | `host` → `network.interface`             | yes        |                                |
| `bound_to`      | `network.address` → `network.interface`  | yes        |                                |
| `next_hop_via`  | `network.route` → `network.address`      | yes        |                                |
| `listens_on`    | `service.listener` → `network.interface` | yes        | bare edge; the port lives on the `service.listener` entity |

All phase-1 relations are **structural**. For structural relation types, a
`relation.added` or `relation.removed` change emits a **high-priority signal**
suitable for alerting (see
[ADR 0006](../architecture/adr/0006-change-taxonomy.md)).

## The formal contract

The authoritative contract is Protocol Buffers in
[`proto/toise/v1/events.proto`](../../proto/toise/v1/events.proto). Its messages
are:

- **`Value`** — the typed attribute value (`string` | `int64` | `double` | `bool`).
- **`KeyValue`** — a key paired with a `Value`.
- **`ChangeType`** — the change-taxonomy enum (reserves `_UNSPECIFIED = 0`).
- **`Entity`** — `type`, identity, descriptive attributes, `schema_url`.
- **`Relation`** — `from`/`to` logical entity IDs, attributes, `structural`.
- **`EntityEvent`** — an observation of an entity.
- **`RelationEvent`** — an observation of a relation.
- **`Event`** — the envelope carrying `event_time`, `recorded_at`, identifiers,
  change type, and the entity or relation payload.

The Toise **schema version for phase 1 is `"1.0"`**. It is independent of the
OTel spec version (see [ADR 0015](../architecture/adr/0015-tracking-otel-entity-events-spec.md)).

Hand-written Go domain types in `internal/model` provide an ergonomic in-process
representation (using `time.Time`, methods, and validation), with
`ToProto`/`FromProto` converters to and from the proto contract above. A type
registry in `internal/model` enumerates the known entity and relation types,
their `structural` flags, and their endpoint-type constraints.

## Status

The model is **phase 1** and may evolve. The change taxonomy is closed for
phase 1, the typed `Value` is a deliberate subset of `AnyValue`, and the entity
and relation type sets are intentionally small. Additions — new entity types,
relation types, or change categories — are designed to be **non-breaking**: the
type registry admits new types without disturbing existing ones, and the
`ChangeType` enum reserves `_UNSPECIFIED = 0` so categories can be appended
without breaking consumers. Follow the ADR log and the `proto/toise/v1/`
definitions for the authoritative state.
