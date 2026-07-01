# Data model

Toise tracks **entities** (things that exist), the **attributes** that describe
them, and the **relations** that connect them — plus the dimension OTel signals
don't carry on their own: how that topology changes over time. Its model aligns
with the
[OpenTelemetry entity data model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
so Toise entities slot into an existing OTel deployment rather than introducing a
parallel vocabulary.

The authoritative contract is the Protobuf definition
[`proto/toise/v1/events.proto`](https://github.com/toise-dev/toise/blob/main/proto/toise/v1/events.proto);
this page is the conceptual overview.

## Entities

An **entity** has four parts:

- a **`type`** — a string id such as `host` or `process`;
- an **identity** — a set of identifying key/value attributes whose values
  together uniquely identify the entity;
- a set of **descriptive attributes** — informational, non-identifying metadata;
- a **`schema_url`** — versions the entity definition.

Attribute values are a typed `Value` that mirrors OTel's `AnyValue`: the four
scalar kinds (`string`, `int64`, `double`, `bool`) **plus** `array` and `kvlist`
(nested map), recursively. **Descriptive attributes carry the full AnyValue** —
arrays and nested maps are kept, and render on read as compact JSON tagged `array`
/ `kvlist` (since 0.9.0). **Identity stays scalar**: `entity.id` and
relation-endpoint ids must be flat maps of scalars (ADR 0018), because exact-match
identity is over scalar strings. Translation happens only at the
[ingest boundary](ingestion.md).

## Two identities, not one

Toise carries two distinct identity concepts that must not be confused:

- **Logical entity ID** — the stable identifier of an entity across its whole
  life, even as its identifying attributes evolve. It is a surrogate **ULID**
  assigned by Toise on first sight, and it **survives identity changes**. This is
  what consumers reference and what relation endpoints (`from`/`to`) point at.
- **Identity hash** — a deterministic fingerprint of the *current* identifying
  attributes (SHA-256 truncated to 128 bits, type-prefixed, e.g. `host:1a2b...`).
  It powers O(1) idempotent ingest and **changes** when an identifying attribute
  changes; the logical ID does not.

!!! tip "Put volatile facts in descriptive attributes"
    Identity should be **stable for the entity's lifetime**. If a host's identity
    includes its current leased IP, a DHCP renewal forks it into a brand-new
    entity. Keep changing facts (current address, usage, last-seen state) as
    *descriptive* attributes so a re-address is an update on the **same** entity,
    not a silent split.

## Relations

A **relation** is a typed, directed edge between two entities, identified by
their logical entity IDs (`from`, `to`). It carries a **`structural`** flag
marking whether its appearance or disappearance is significant (alertable)
rather than merely descriptive.

Relations are **attribute-free on the wire**: anything that would describe *how*
two things relate becomes an **entity** instead (a port is a `network.interface`,
a route is a `network.route`). See [Ingesting data](ingestion.md#relationships-are-embedded).

## Bi-temporality

Every event carries **two timestamps**, and they are not interchangeable:

- **`event_time`** — when the fact became true in the real world, supplied by the
  producer.
- **`recorded_at`** — when Toise recorded the event, stamped at ingestion. Never
  taken from the producer.

For a late or retroactively corrected event, `event_time` is significantly
earlier than `recorded_at`; that gap is the signal, not noise. Queries default to
**`event_time` space (the reality view)**; the audit view ("what did we know at
instant T?") is an opt-in via `asKnownAt` — see
[GraphQL bi-temporality](querying/graphql.md#bi-temporality-eventtime-vs-recordedat).

## The change taxonomy

As events are classified, each change is tagged with one of:

`ENTITY_CREATED` · `ENTITY_DELETED` · `ENTITY_IDENTITY_CHANGED` ·
`ENTITY_ATTRIBUTE_UPDATED` · `ENTITY_STATE_CHANGED` · `ENTITY_UNCHANGED` ·
`RELATION_ADDED` · `RELATION_REMOVED` · `RELATION_ATTRIBUTE_CHANGED`.

This taxonomy — plus bi-temporality — is *Toise's* contribution on top of the
OTel facts: the engine stores only what producers assert and classifies how it
changed, never deriving or guessing state
([ADR 0022](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0022-engine-stores-facts-only.md)).

For the full OTel mapping, see
[`docs/data-model/otel-mapping.md`](https://github.com/toise-dev/toise/blob/main/docs/data-model/otel-mapping.md).
