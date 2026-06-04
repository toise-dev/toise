# 22. The engine stores facts only — a faithful OTel entity-events store; derivation is a surcouche

- Status: Accepted
- Date: 2026-06-04

## Context

Toise is LLM-first: it ingests OpenTelemetry entity events and serves them to AI
assistants (MCP) and code (GraphQL), over a bi-temporal event log and an in-memory
projection. The OpenTelemetry entity-events specification is maturing — relationships
just landed as an embedded model (spec PR #4836, approved-not-merged), and the SIG
anticipates a future entity *signal* that "tracks changes over time". A review by an
OTel TC member on our blog (opentelemetry.io#10124) opened issues #61 (process
identity) and #65 (relationship model) and forced a deeper question than "which wire
shape": **what is Toise's engine actually allowed to store?**

The answer needed is a north star, not a one-off — because it decides where the line
sits between Toise-the-source-of-truth and everything built on top of it.

## Decision

**The Toise engine stores and serves only asserted facts, modeled as OpenTelemetry
entity-events. It never stores derived, inferred, or supposed information. Every
derivation, correlation, or interpretation is a *surcouche* (an over-layer) — in the
producer/observer, in the consumer (including the LLM), or in a clearly separated
optional layer — never in the source of truth.**

Concretely:

1. **State is fact.** Entities and relationships are stored exactly as a producer
   asserts them, following the OTel entity-events model. Relationships use the spec's
   **embedded** form (bare descriptors: type + target type + target id). Anything that
   would be an *edge attribute* is instead modeled as an **entity** where the fact
   actually lives — a network port is a `network.interface` entity, a route's metric
   is on the `network.route` entity — so edges stay bare and spec-compliant.
   **Provenance** (how an edge/fact was observed) lives at the **observer level** (OTLP
   instrumentation scope / resource), never on the edge.

2. **Temporal change is fact, and is core.** The change taxonomy
   (`entity.created` / `attribute_updated` / `state_changed` / `deleted`,
   `relation.added` / `removed`, …) and bi-temporality (`event_time` = reality,
   `recorded_at` = ingestion) are **part of the engine**. *What changed, when it
   happened, and when we learned it* is the **temporal truth** — a diff of asserted
   states, not speculation. (It is also precisely the layer the OTel spec has not yet
   specified; see *Upstream*.)

3. **Liveness is operational and fact-grounded, and stays core.** Interval-based
   expiry, endpoint cascade, and per-producer reference counting (ADR 0019) are
   grounded in producer heartbeats and explicit deletes (facts), not derivations.

4. **Everything derived/inferred/supposed is a surcouche.** Computed adjacency
   summaries, multi-source identity correlation (`same_as` / confidence / a canonical
   view — ADR 0020), provenance correlation, enrichment, and any interpretation live
   **outside** the engine: in consumers (the LLM, applications) or in clearly separated
   optional layers. The truth engine never materializes a supposition.

5. **Stick to the pure spec on the wire.** The engine ingests standard OTel
   entity-events (embedded relationships). The `entity.relation.*` separate-event
   extension and its strict-purity design are **retired** to a transitional
   compatibility shim (deprecated), not part of the core model. Relationship removal
   follows the spec (a re-emitted state without the descriptor — still a producer-
   asserted fact). Where the spec is silent — the temporal change layer — Toise leads
   and contributes its prior art upstream rather than inventing private wire shapes.

## Consequences

This recasts several earlier decisions:

- **ADR 0020 (weighted multi-source identity)** is reclassified as a **surcouche**, not
  an engine feature: `same_as` + confidence + a derived canonical view is *supposed*
  correlation. (A status note is added to ADR 0020.)
- The **`entity.relation.*` extension + strict purity** (the #18 decision) is
  **deprecated**; the wire moves to embedded `entity.relationships`.
- The **just-frozen Lot 5 relation shapes** change: `adjacent_to {local_port,
  remote_port}` becomes port entities (`network.interface`) plus a bare `connected_to`;
  `routes_via.source` (provenance) moves to the instrumentation scope. The **entity
  identity** work (`network.device.id` precedence `serial:<PEN>`/…, exact matching)
  **stays** — it is fact.
- The **conformance fixture** is rewritten (embedded relationships, no edge attributes).
- The **senhub-agent producer contract** is resynced (emit embedded relationships +
  port entities, not attributed edges); coordinated, not unilateral.

What stays unchanged: the event log, the projection, the **change taxonomy**, the
**bi-temporality**, liveness (interval / cascade / ref-counting), exact identity (ADR
0018), and the GraphQL/MCP read surfaces.

**Positioning sharpens.** Toise is the *source of truth* — facts and temporal truth;
all intelligence and derivation lives in consumers. This is the same worldview as
LLM-first and ADR 0021 (human interfaces at the edge): the engine serves facts, the
LLM and other consumers interpret them.

**Upstream.** Toise aligns with the spec for state and relationships, and contributes
its temporal-change prior art (the change taxonomy + bi-temporality, plus experience
on multiple observers, identifier selection, and liveness) to the OTel "Resources and
Entities" SIG — the central gap that is also our core. This is stewarded out-of-band
by a dedicated SIG-liaison process, not by changing the engine.

**Migration is non-trivial and staged**, and is captured in a separate migration plan.
The spec is approved-not-merged and the OTel libraries we pin (ADR 0015) do not carry
it yet, so the pivot is deliberate, not rushed.

## Relationship to other decisions

- **Foundational over** ADR 0015 (spec tracking), 0018 (exact identity), 0019
  (per-producer ref-counting), and 0021 (human-interface boundary) — it states the
  principle they all serve.
- **Supersedes the framing** of the `entity.relation.*` extension (#18) and
  **reclassifies ADR 0020** as a surcouche.
- Implements the resolution of issue #65 and is consistent with #61.
