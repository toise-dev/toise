# Roadmap

This roadmap describes the planned direction for Toise. It is intentionally
high-level and will shift as the design firms up and as we learn from early
deployments. Dates are targets, not commitments.

Toise ingests OpenTelemetry entity events over OTLP. It runs no collectors of
its own and speaks no device protocols directly: wherever this roadmap mentions
a new source of data, that data reaches Toise through an OpenTelemetry producer
— a host or network agent that emits entity events (for example
[senhub-agent](https://agent.senhub.io), one such producer among others).
Broadening that producer ecosystem is a shared, open effort, not something
Toise does by polling devices itself.

## Q3 2026 — Foundations

- Core graph store backed by an append-only event log.
- OpenTelemetry-aligned entity model (entities, relationships, attributes).
- OTLP ingestion of entity events from any OpenTelemetry producer.
- A GraphQL query API and a native MCP server, so AI assistants can read the
  graph on an operator's behalf.
- Real-time subscriptions over WebSocket.

## Q4 2026 — Breadth and first demo

- A broader ecosystem of OpenTelemetry producers emitting entity events,
  covering more infrastructure sources (network, virtualization, directory).
- A minimal debug UI for inspecting the graph.
- First public demo scenario.

## H1 2027 — Toward production

- Production deployment with a pilot partner.
- Wider producer coverage, including cloud-provider inventories.
- Public release candidate.

## Beyond

Longer-term directions — federation across sites, richer graph query
semantics, and a broader producer and integration ecosystem — will be captured
as [Architecture Decision Records](./architecture/adr/) once the foundations
are in place.
