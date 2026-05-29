# 15. Tracking the OpenTelemetry entity-events specification

- Status: Accepted
- Date: 2026-05-29

## Context

Toise's ingestion contract is built on the OpenTelemetry Entity Data Model and
the Entity Events specification (see ADR 0004 for the data model and ADR 0009
for OTLP ingestion via entity events in Milestone 4). At the time of writing,
both specifications are still evolving — they carry experimental/development
status — and their shape can change between releases in ways that affect the
on-the-wire contract we consume.

We want to benefit from this alignment without letting an upstream spec change
destabilize our internal model. Two pressures pull in opposite directions:
staying close to the spec so we can adopt new entity-events capabilities, and
keeping the internal event model stable so that stored history and the
projection logic built on it are not at the mercy of upstream churn.

## Decision

We will track the OpenTelemetry entity-events specification actively, while
versioning our internal model independently of it.

- **Follow the spec at the source.** We track the OpenTelemetry "entities"
  Special Interest Group (SIG): its mailing list and the
  `open-telemetry/oteps` and entities working-group GitHub repositories, so we
  see proposed changes before they ship.
- **Pin the OTel dependencies.** The versions of the OpenTelemetry SDK and
  collector libraries we depend on are pinned. Bumping them requires an ADR
  addendum documenting any semantic changes. This is a deliberate exception to
  the general dependency-currency policy in ADR 0003, which otherwise keeps
  dependencies on their latest minor/patch versions — the OTel libraries are
  the one set we hold back on purpose.
- **Version our model independently.** The Toise `schema_version` field on
  events is independent of the OTel spec version. When the OTel spec changes,
  we add a migration layer at the **ingest boundary** rather than mutating the
  internal model. The internal model evolves on its own schedule, driven by our
  needs, not by upstream version numbers.
- **Document the mapping.** The mapping between the current OTel entity-events
  spec version and the Toise schema is recorded in
  `docs/data-model/otel-mapping.md` and kept up to date as either side changes.

## Consequences

- Some integration friction is accepted when new spec versions drop: a bump is
  a deliberate, documented act rather than an automatic one.
- In exchange, the internal model stays stable — stored history and the
  projections built on it (see ADR 0002) are insulated from upstream churn.
- New OTel entity-events features can be adopted incrementally rather than in a
  big-bang upgrade, gated by the pinned dependency versions.
- The ingest boundary becomes the single place where spec churn is absorbed,
  which concentrates the migration logic and keeps the rest of the system
  unaware of which OTel spec version produced a given event.
