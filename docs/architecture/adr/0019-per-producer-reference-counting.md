# 19. Per-producer reference counting for entity liveness

- Status: Accepted
- Date: 2026-06-02

## Context

Toise's identity is exact and **observer-independent** (ADR 0018): two agents that
observe the same entity (e.g. a database monitored redundantly) converge on one
entity, because the identity is a stable source id, not anything tied to the
observer. That is the right multi-producer behaviour for *creation* and *updates*.

Deletion, however, was keyed by **identity, not by producer**. So an explicit
`entity_delete` from **one** agent removed the entity **globally**, even while
another agent still observed it — which then re-created it on its next heartbeat,
a flap of roughly one heartbeat cadence. For a graph that is meant to be a source
of truth, that spurious delete-then-recreate is noise in the history and a
potential false alert. The interval backstop (ADR-less, the liveness sweeper)
already handled the *crash* case correctly (the entity survives while any agent
heartbeats); only an explicit per-producer delete flapped.

This was raised in the senhub-agent producer review (#185, "Q1").

## Decision

Entity liveness is **reference-counted per producer**. Toise records, per entity,
the set of producers currently asserting it, each with its own expiry deadline.
An entity is **live while any producer references it**, and is deleted only when
the **last** reference is released — by that producer's explicit `entity_delete`
or by its interval lapsing.

- **Producer identity = the OTLP Resource `service.instance.id`** — the producing
  agent's own instance id, which producers already set. No new wire attribute, no
  producer change: adding ref-counting was a Toise-only change.
- `ObserveEntity` records/refreshes the observing producer's reference (with its
  interval deadline; a zero deadline means explicit-only, no interval expiry).
- `DeleteEntity` releases **that producer's** reference; it emits `entity.deleted`
  (and cascades incident edges) only when no producer remains, otherwise it is a
  silent release.
- `Sweep` drops each producer reference whose interval has lapsed and expires the
  entity when its last reference is gone.
- **Backward compatible:** an observation with no producer (empty
  `service.instance.id`) is treated as a single anonymous producer, so a
  single-producer deployment — and the demo and existing tests — behave exactly as
  before.

Edges are **not** reference-counted: a `monitors` edge is naturally per-agent (its
source is the agent's own `service.instance`), and shared edges follow their
endpoints via the cascade (ADR-less Q2 decision: edge liveness derived from
endpoints).

## Consequences

- A shared entity no longer flaps when one of several producers deletes or lapses;
  it disappears only when genuinely unobserved by all.
- The change is internal to Toise; producers need only keep setting the Resource
  `service.instance.id` (already true).
- A **corollary** the producer must respect: because state is full and identity is
  shared, a shared entity carries only **observer-independent** attributes; any
  per-observer fact is a distinct `monitors` relation per agent, never an entity
  attribute (which would flap last-writer-wins).
- Two distinct producers that both omit `service.instance.id` would collapse into
  one anonymous reference — a misconfiguration, documented.

## Amendment — 0.21.0: the reference key is the SPEAKER, not the producer (#455)

The decision above is unchanged in substance and refined in its key. A reference
is held per **speaker**: a producer *and* the collection method it spoke through
(the OTLP instrumentation scope name, which the producer contract already makes
the carrier of provenance, one scope per collection method).

**What forced it.** A producer running several collection methods held one
reference per entity and per edge, so its methods overwrote each other. Two
consequences, both measured rather than foreseen:

- `collected_by` reported whichever method spoke last, flipping at the cadence of
  the two, where a consumer needs the set. An agent with six collectors — the
  shape senhub-agent 0.6.4 has — got a single rotating value.
- One method losing sight of an edge released the whole producer's reference and
  **deleted an edge another method was still asserting**, which re-created it on
  its next cycle. Scoping the reconciler's diff state per method (0.21.0, #457)
  had fixed the case where two methods see different things; this is the case
  where they see the same thing, and with collectors whose coverage overlaps by
  construction (host ARP and SNMP ARP, LLDP and CDP) it is the normal case.

**What it changes.** Nothing in the rule: an entity or edge is live while any
reference survives, and a speaker may release only its own. The grain of "its
own" is now the method. A method that falls silent expires its own references
through the interval backstop; a method that retracts by absence retracts what it
itself asserted (the reconciler's diff state is keyed the same way).

**Producers are unaffected.** No wire change: a producer naming no instrumentation
scope keys on its id alone and behaves exactly as before. The key encoding is
chosen so that a scopeless speaker's key is byte-identical to what the
per-producer form wrote, which is why there is no snapshot migration — and, as a
side effect, the collection method now survives a restart instead of being
rebuilt only when each producer next speaks.

**One property had to be built rather than assumed.** Both halves of the key are
strings the producer chooses, so joining them on a separator is injective only as
long as no producer sends that separator. A crafted `service.instance.id` could
otherwise key identically to another speaker and release a reference it never
declared — the single thing this ADR exists to prevent. The key therefore encodes
the producer's length, and the two forms are disjoint by their first byte. Both
holes were found by the test that pins the property, in two rounds; neither was
reasoned out first, which is the argument for pinning a constraint rather than
stating it.

See also: ADR 0018 (exact, observer-independent identity), `docs/data-model/`
(the producer contract), senhub-agent #185 (the review).
