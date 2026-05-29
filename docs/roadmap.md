# Roadmap

This roadmap describes the planned direction for Toise. It is intentionally
high-level and will shift as the design firms up and as we learn from early
deployments. Dates are targets, not commitments.

## Q3 2026 — Foundations

- Core graph store backed by an append-only event log.
- OpenTelemetry-aligned entity model (entities, relationships, attributes).
- First receivers: SNMP, vSphere, and a host agent extension.
- Initial query API for reading the graph.

## Q4 2026 — Breadth and real time

- gNMI receiver.
- Active Directory receiver.
- Real-time subscriptions over WebSocket.
- First public demo.

## H1 2027 — Toward production

- Production deployment with a pilot partner.
- Extended receivers: BMP and cloud provider APIs.
- Public release candidate.

## Beyond

Longer-term directions — federation across sites, richer graph query
semantics, and a broader receiver ecosystem — will be captured as
[Architecture Decision Records](./architecture/adr/) once the foundations are
in place.
