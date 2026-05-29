# 5. Bi-temporal event model

- Status: Accepted
- Date: 2026-05-29

## Context

Events about infrastructure rarely arrive in clean, real-time order. An agent
that was offline reconnects and replays observations from while it was down; a
source corrects an earlier reading; collection batches arrive late. The
consequence is that events reach Toise out of order, late, and sometimes as
retroactive corrections of facts we already recorded.

A single timestamp cannot express this. "When the fact became true" and "when
we learned it" are different questions, and for a late or corrected event they
have different answers. Collapsing them loses exactly the information operators
and LLMs ask for, because the most valuable questions about infrastructure are
temporal: *what changed*, *why is this different from yesterday*, *what did we
know at 14:00*.

This builds on the event-sourced storage pattern (ADR 0002) and the data model
(ADR 0004): every observation is already an immutable event in the log. The
question here is what time each event carries.

## Decision

Every event carries **two timestamps**, and they are not interchangeable.

- **`event_time`** — when the fact became true in the real world. Supplied by
  the producer (e.g. senhub-agent, or any OTel producer) from its source.
- **`recorded_at`** — when Toise recorded the event. Stamped by toise-server at
  ingestion.

For a late or retroactively corrected event, `event_time` is significantly
earlier than `recorded_at`. That gap is the signal, not noise.

### Query semantics

The two timestamps define two ways to look at history, and the API makes the
default the one operators almost always mean.

- **Default queries operate in `event_time` space — the "reality view".** This
  is our best current knowledge of what actually happened. "What is the state
  now" and "what was the state at 14:00" both answer using `event_time`.
- **Knowledge and audit queries opt in via an `asKnownAt` parameter — the
  "audit view".** It constrains the query to events whose `recorded_at <= t`,
  reconstructing Toise's state of knowledge as it stood at that past moment
  (including not yet knowing about facts that had already become true).
- Time-bounded queries (`since` / `until`) operate on `event_time`, unless
  `asKnownAt` is also given.
- Every event always exposes **both** `eventTime` and `recordedAt`, so a
  consumer can always audit the reality view against what we knew when.
- The GraphQL and MCP field descriptions must teach an LLM which mode a question
  implies — a "what is true" question maps to the reality view, a "what did we
  know" question maps to `asKnownAt` — so the model picks the right query.

## Consequences

- **History, change, and time travel are first-class and reconstructible from
  the event log.** Late and out-of-order events have a correct home, and
  retroactive corrections are recorded rather than overwriting earlier facts.
- The reality view and the audit view are both available from the same log,
  distinguished only by which timestamp a query ranges over.
- **The API surface is slightly larger:** the `asKnownAt` opt-in and the dual
  timestamps on every event. This is accepted as the cost of making temporal
  questions answerable.
- **Producers must supply a meaningful `event_time`** from their source; Toise
  is responsible only for stamping `recorded_at` at ingestion. A producer that
  cannot determine `event_time` degrades the reality view for its own events.
- Temporal replay — projecting the log up to a chosen `event_time` or
  `recorded_at` bound — happens in the in-memory projection (ADR 0008), which is
  where both views are materialized for querying.
