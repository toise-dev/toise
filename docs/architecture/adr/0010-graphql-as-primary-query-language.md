# 10. GraphQL as the primary query language

- Status: Accepted
- Date: 2026-05-29

## Context

Toise is LLM-first: its primary consumer is an AI assistant querying on an
operator's behalf, not a human writing queries by hand. That shapes what the
query API must be.

For an LLM to use the API without bespoke, hand-maintained integration, the API
has to be **introspectable** — the model must be able to discover the available
queries, types, and arguments at runtime — and **self-describing** — the model
must be able to read what each thing *means*, not just its shape. A custom
query DSL with out-of-band documentation fails both: it cannot be discovered at
runtime and its semantics live somewhere the model never sees.

## Decision

We will expose a **GraphQL** API as the primary query surface, built with
gqlgen (`github.com/99designs/gqlgen`) — schema-first, code-generated, pure Go
— and served over HTTP with WebSocket subscriptions.

- **Introspection is the discovery mechanism.** The LLM reads the schema at
  runtime to learn what it can ask, instead of relying on a custom DSL or
  out-of-band docs.
- **Rich descriptions are mandatory** on every type, field, and argument. They
  are not optional polish: they carry the semantics the LLM reads via
  introspection, and they teach the model which question maps to which query.
- **Relay cursor pagination on every list-returning query** (`first` / `after`,
  with `Connection` / `edges` / `pageInfo` / `totalCount`). No query returns an
  unbounded list (patch 5).
- **Guardrails** (patch 5): a configurable query-complexity limit (default
  1000), a per-query timeout (default 10s), and a bounded traversal depth where
  applicable. Limit errors are written in plain language explaining what
  happened and how to narrow the query — not cryptic complexity scores —
  because an LLM (or a human) reads them.
- **Where data comes from.** Current-state queries (`entity`, `entities`,
  `relations`) read the in-memory projection (ADR 0008). History and change
  queries (`entityHistory`, `recentChanges`) read the event log (ADR 0007).
  Temporal semantics follow ADR 0005: `since` / `until` operate in `event_time`
  space by default, with an `asKnownAt` opt-in for the audit view.
  Subscriptions (`entityChanged`, `relationChanged`) stream qualified events
  from the change engine.
- **Direct human use of GraphQL is supported but secondary.** The LLM via MCP
  (ADR 0011, Milestone 6) is the primary path; it is built on the same
  projection and store, so the two surfaces share one read model.

## Consequences

- A standard, introspectable, self-describing API the LLM can use without
  bespoke integration — discovery and semantics come from the schema itself.
- gqlgen's code generation keeps resolvers type-safe and the schema the single
  source of truth.
- The description discipline and pagination discipline are enforced by review:
  a new field without a description, or a list query without cursor pagination,
  does not merge.
- The complexity and timeout limits protect the server from expensive
  LLM-generated queries, and report violations in language the caller can act
  on.
- GraphQL and MCP share the same underlying read model, so the two consumer
  paths stay consistent.

See also: ADR 0005 (bi-temporal semantics, `asKnownAt`), ADR 0007 (the event
log, source for history), ADR 0008 (the in-memory projection, source for
current state), ADR 0011 (MCP, the primary consumer path).
