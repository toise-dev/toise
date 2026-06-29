# 4. Data model aligned with the OpenTelemetry entity data model

- Status: Accepted
- Date: 2026-05-29

## Context

Toise needs a domain data model for infrastructure entities and the relations
between them. Rather than invent a parallel vocabulary, the model aligns with
the [OpenTelemetry Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
so that Toise slots into existing OTel deployments and interoperates with the
broader ecosystem.

The bi-temporal event model that records changes to these entities over time is
described in ADR 0005, and the taxonomy of change types in ADR 0006. This ADR
covers only the shape of the entities and relations themselves.

## Decision

The domain model mirrors the OTel entity concepts.

- An **Entity** has a `type` (a string id, for example `host` or `process`), an
  **identity** (a set of identifying key/value attributes whose values together
  uniquely identify the entity), a set of **descriptive attributes**
  (informational, non-identifying), and a `schema_url` that versions the entity
  definition. Identity and stability are treated in detail in ADR 0017.
- A **Relation** is a typed, directed edge between two entities (`from`, `to`),
  carrying optional attributes and a `structural` boolean flag. The flag marks
  whether the relation's appearance or disappearance is significant (structural)
  rather than merely descriptive.
- Attribute values use a typed **`Value`** — a oneof over string, int64, double,
  and bool. This is a deliberate subset of OTel's `AnyValue`. It keeps the
  internal model independent of OTLP wire types; translation from OTLP
  `AnyValue` happens only at the ingest boundary.
- We define **hand-written Go domain types** in `internal/model` (ergonomic:
  `time.Time`, methods, validation) alongside a **Protocol Buffers contract**
  (`proto/toise/v1`, package `toise.v1`) used for on-disk serialization and as
  the internal wire contract, with `ToProto`/`FromProto` converters between the
  two. The rationale is to keep the domain decoupled from the serialization
  format while making the proto the durable, interchange contract.
- A **type registry** in `internal/model` enumerates the known entity and
  relation types. Each relation records its `structural` flag and its
  endpoint-type constraints (for example `runs_on: process -> host`). The
  registry is designed so that new types are added without breaking existing
  ones.

Phase-1 entity types:

- `host`
- `process`
- `network.interface`
- `network.address`
- `network.route`
- `service.listener`

Phase-1 relation types:

- `runs_on` (process -> host)
- `has_interface` (host -> network.interface)
- `bound_to` (network.address -> network.interface)
- `next_hop_via` (network.route -> network.address)
- `listens_on` (service.listener -> network.interface, with a `port` attribute)

## Consequences

- Alignment with the OTel entity model eases interop with OTel deployments and
  avoids a competing vocabulary. We track the still-evolving entity-events spec
  per ADR 0015.
- The typed `Value` loses some of `AnyValue`'s generality — no nested kvlist,
  array, or bytes values in phase 1 — by design. The cost is revisited if a
  later phase needs richer values. **(Superseded for descriptive attributes by
  the 0.9.0 amendment below.)**
- The hand-written domain types add converter boilerplate (`ToProto` /
  `FromProto`), accepted in exchange for decoupling the domain from the wire
  format.
- The type registry centralizes validation of entity and relation types and
  their endpoint constraints, making additions safe and keeping type rules in
  one place.

## Amendment — 0.9.0: full AnyValue for descriptive attributes

- Date: 2026-06-29

The OTel entity-events spec (1.58.0) types `entity.description` as an `AnyValue`
that "can contain scalar values, arrays, or nested maps". To align strictly and
avoid silently losing producer facts, `Value` is widened from the four scalars
to the **full AnyValue**: it gains an **array** (ordered `[]Value`) and a
**kvlist** (ordered `[]KeyValue`) form, recursively, mirroring OTLP's
`ArrayValue` / `KeyValueList` wrapper messages on the proto side.

Scope and invariants:

- **Identity stays scalar.** Only descriptive attributes (`entity.description`)
  carry the full AnyValue. Identity (`entity.id`) and relationship endpoint ids
  remain scalar by contract (ADR 0018); a nested value in an identity map is
  dropped and surfaced, never hashed.
- **Lossless and backward compatible.** Scalar-only producers are byte-for-byte
  unchanged. Arrays and nested maps that were previously dropped (and surfaced
  on the dropped-keys path) are now ingested faithfully end to end (ingest →
  store → projection → GraphQL/MCP). An unsupported leaf (e.g. bytes) inside a
  composite still rejects that composite and is surfaced, never stored partial.
- **Rendering.** On read surfaces a composite value renders as compact JSON,
  tagged with a value type of `array` / `kvlist` (GraphQL `ValueType.ARRAY` /
  `KVLIST`, MCP `"array"` / `"kvlist"`); scalar rendering is unchanged.
- **Canonical encoding** for arrays/kvlists is length-prefixed and sorts kvlist
  keys, so identity hashing and attribute-change detection stay deterministic
  and collision-free across nesting shapes.
