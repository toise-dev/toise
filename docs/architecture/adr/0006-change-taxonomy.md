# 6. Change taxonomy

- Status: Accepted
- Date: 2026-05-29

## Context

Toise stores an append-only event log and projects it into a live graph (see
ADR 0002). Storing raw observed state is not enough: the graph projection,
alerting, and the LLM all need to reason about *what kind* of change a given
event represents — a created entity, a removed relationship, an anomalous
identity mutation — not merely the new state.

For that reasoning to be reliable, the kind of change must be machine-readable.
An ad-hoc or implicit notion of "what changed" would force every consumer to
re-derive it, inconsistently. A fixed, documented taxonomy gives a single,
stable vocabulary that the projection, alerting rules, and downstream tooling
can all agree on.

## Decision

The change-detection engine (Milestone 3, see ADR 0008) classifies every
ingested event into **exactly one** of the following categories.

Entity changes:

- `entity.created` — the entity did not exist before this event.
- `entity.deleted` — the entity existed and is now soft-deleted; the log
  retains everything.
- `entity.identity_changed` — an *identifying* attribute mutated. This is
  anomalous and is logged at high priority; it usually signals a data-quality
  issue or a misconfigured producer. The logical entity ID stays stable (see
  ADR 0017).
- `entity.attribute_updated` — a *descriptive* (non-identifying) attribute
  changed.
- `entity.state_changed` — a state-flagged attribute (`oper_state`,
  `admin_state`, `status`) flipped value.
- `entity.unchanged` — a heartbeat: the entity is alive but nothing changed.

Relation changes:

- `relation.added` — a new edge appeared.
- `relation.removed` — an edge disappeared.
- `relation.attribute_changed` — edge metadata mutated.

Additional rules:

- For relation types declared `structural: true` (see ADR 0004),
  `relation.added` and `relation.removed` emit **high-priority signals**
  suitable for alerting.
- `entity.unchanged` heartbeats are coalesced rather than retained
  individually (see ADR 0013 on retention).

## Consequences

- Classification gives a stable, queryable vocabulary for "what changed" and
  powers structural-change alerting.
- It adds a diffing step at ingest, performed by the change-detection engine
  in Milestone 3 (see ADR 0008).
- The taxonomy is closed for phase 1, but new categories can be appended
  without breaking consumers: the enum reserves `_UNSPECIFIED = 0`.
