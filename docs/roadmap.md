# Roadmap

High-level direction for Toise. The project has moved faster than its first
calendar-based plan, so this roadmap is framed by **progress, not dates**: what
has shipped, what is in flight now, and where we are heading. It will keep
shifting as we learn from real deployments.

Toise ingests OpenTelemetry entity events over OTLP. It runs no collectors of
its own and speaks no device protocols directly: wherever this roadmap mentions
a new source of data, that data reaches Toise through an OpenTelemetry producer
— a host or network agent that emits entity events (for example
[senhub-agent](https://agent.senhub.io), one such producer among others).
Broadening that producer ecosystem is a shared, open effort, not something
Toise does by polling devices itself.

## Shipped

Through **0.7.0** (mid-2026), ahead of the original schedule:

- **Foundations** — an event-sourced graph store on an append-only log, an
  OpenTelemetry-aligned entity model, OTLP ingestion from any producer, a
  GraphQL API and a native MCP server, real-time subscriptions, a debug UI, and
  a public demo scenario.
- **Production-readiness & multi-tenancy** — bearer-token auth with per-tenant
  authorization, TLS, isolated per-tenant graphs, operational endpoints and
  Prometheus metrics, retention and snapshots, release binaries and a multi-arch
  container image. **Running in production.**
- **Time travel & the producer SDK** — as-of event-time queries across MCP and
  GraphQL, impact analysis, and the first public producer SDK (`pkg/emit`) with
  a byte-pinned conformance kit.
- **Integration & stability** — operator annotations (a `get_entity` overlay and
  the first GraphQL mutation), MCP resources and prompts, read-only and
  ingest-only token roles, response verbosity tiers, a conformance CLI and a
  producer directory, and a pinned, documented API surface.
- **Connection topology** — "who depends on whom": a producer asserts a durable
  `depends_on` edge to an observable network endpoint, and the consumer resolves
  that endpoint to the canonical remote listener/host at read time (a derived
  overlay, never written back), so the graph answers both "what does this service
  depend on?" and "who connects to it?". See
  [the design note](./design/netstat-connection-topology.md).

## Now

- **A broader producer ecosystem** — more OpenTelemetry producers emitting
  entity events across host, network, virtualization, directory and cloud
  inventory sources. This is the shared, open effort that widens what the graph
  can see.
- **Stabilising the API and data model** toward 1.0, informed by what real
  deployments exercise.

## Next — toward 1.0

- A **release candidate** once the public surfaces (the wire contract, GraphQL,
  the MCP tools/resources/prompts) have settled across real deployments.
- Hardening and operability work surfaced by production use.

## Beyond

Longer-term directions — federation across sites, richer graph-query semantics,
and a wider producer and integration ecosystem — are captured as
[Architecture Decision Records](./architecture/adr/) as the foundations settle.
