# 11. MCP server design

- Status: Accepted
- Date: 2026-05-29

## Context

Toise is LLM-first: its primary consumer is an AI assistant querying on an
operator's behalf (ADR 0010). For that consumer, a native Model Context Protocol
(MCP) server is part of the backend, not an external integration bolted on later
— it is the path the LLM actually takes to reach Toise.

The Milestone 0 check confirmed that an **official Go SDK for MCP exists and is
GA**: `github.com/modelcontextprotocol/go-sdk` v1.6.1. With a maintained,
protocol-conformant SDK available, hand-implementing the wire protocol would add
maintenance burden and drift risk for no benefit.

## Decision

We will build an MCP server in package `internal/mcp` using the official SDK,
exposing Toise as a set of **tools**. The server is served over two transports:
**stdio** (for Claude Desktop and other local clients) and **Streamable HTTP**
(for web-based LLM clients).

- **Typed tools.** Tools are registered with `mcp.AddTool[In, Out]`, which infers
  and validates the JSON schema from typed Go input and output structs. Input
  validation is therefore a property of the type, not hand-written checks.
- **Tools exposed.** Each carries a rich description and examples so the LLM picks
  the right one:
  - `find_entities(type, attribute filter, limit)` — entities matching a filter.
  - `get_entity(id)` — a full entity with its attributes.
  - `get_neighbors(entity_id, relation_type, depth)` — traverses relations up to
    `depth`, **capped at maxDepth 5**; beyond that it returns a friendly error
    inviting a smaller query.
  - `entity_history(entity_id, since, until)` — an entity's timeline from the log.
  - `recent_changes(window, filter)` — recent qualified changes.
  - `describe_schema()` — a natural-language description of the entity and
    relation types currently in the graph, to help the LLM bootstrap its
    understanding.
- **Outputs structured for LLM reasoning.** Tool results carry human-readable
  names alongside ids, types alongside instances, and brief context strings, so
  the model can reason without a second lookup.
- **Errors are plain, user-friendly messages** (e.g. "depth 7 exceeds the maximum
  of 5"), never stack traces.
- **One read model.** The tools read from the same in-memory projection (current
  state, ADR 0008) and event log (history, ADR 0007) as the GraphQL API
  (ADR 0010), so the two surfaces stay consistent.
- A sample Claude Desktop configuration is provided in
  `docs/demo/claude-desktop-config.json`.

## Consequences

- The LLM consumes Toise directly via MCP, with no bespoke glue between model and
  backend.
- Reusing the official SDK keeps us aligned with the protocol as it evolves and
  reduces maintenance.
- GraphQL and MCP share one read model, so the two consumer paths report the same
  state.
- The typed tool structs give input validation for free.
- Structured, name-bearing outputs reduce the LLM's need for follow-up lookups.

See also: ADR 0005 (bi-temporal history), ADR 0007 (the event log, source for
history), ADR 0008 (the in-memory projection, source for current state),
ADR 0010 (GraphQL, the sibling API on the same read model).
