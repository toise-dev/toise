# 21. Human interfaces live at the edge, not in the core

- Status: Accepted
- Date: 2026-06-03

## Context

Toise is **LLM-first**: the surfaces the product is built around are the **MCP
server** (ADR 0011) and the **GraphQL API** (ADR 0010), over the bi-temporal event
log and the in-memory projection. ADR 0012 already drew one line — the bundled
debug UI is a **minimal, zero-JS, server-rendered debug aid**, explicitly "a debug
aid, not a product surface [...] not a second front-end that drifts from the real
consumer paths."

As the product matures, pressure builds to add **rich human visualization** — most
acutely a topology map (the project is literally a "living map of infrastructure",
and Lot 5 brings SNMP topology). A graph is genuinely spatial, and a viz is
compelling for demos and trust. The question this ADR settles: **where does the
human-interface boundary sit** — what belongs in the Toise core, and what does not.

The risk of getting it wrong is concrete. A rich interactive viz pulls in a **JS
toolchain** (against the single-Go-binary, no-runtime-deps ethos), becomes a
**second front-end** that drifts from the API, **dilutes the LLM-first thesis**
(if a great clickable map exists, the LLM path is undercut, and Toise starts
competing with Grafana/NetBox on their turf where its edge disappears), and tempts
**presentation concerns into the read model**. At Lot 5 cardinality (thousands of
FDB/ARP rows) a naive viz also simply melts.

## Decision

**The Toise core ships only machine interfaces plus a minimal debug aid. Rich human
visualization lives at the edge, as a demo / example implementation.**

- **Core surfaces:** MCP (ADR 0011), GraphQL (ADR 0010), the event log, and the
  **minimal zero-JS debug aid** (ADR 0012). Nothing more. No JS toolchain is added
  to the Go binary; no interactive viz is compiled into `toise-server` or
  `internal/*`.
- **Human dataviz is not a core surface.** A topology map / dashboard lives **at the
  edge** — on the site or as a **separate consumer** — and is **fed by the public
  GraphQL/MCP API**, with no privileged access to internals. It thereby doubles as a
  **reference implementation** that proves a consumer can be built on Toise's
  contract (dogfooding the API, which is an adoption asset, not debt).
- **Mechanism in the core, presentation at the edge.** The core exposes everything
  needed to build *any* UI and ships *no* opinionated product UI.
- **The "living map" is read by the LLM.** The bet is that an operator asks the
  assistant ("what's connected to host X / what's the blast radius?") and the LLM
  answers via MCP. A viz is a **showcase** that complements that — showing structure
  — not the product's centre of gravity, and a demo viz may bound itself to a
  sub-graph rather than pretend to render the whole fabric.
- **Metrics charts / time-series dashboards are out of scope entirely** — that is a
  TSDB + Grafana's job. Toise is an **inventory / relationship graph**, not a metrics
  store.

## Consequences

- The core stays a lean single Go binary with no front-end build step; the public
  API remains the **only** privileged path, which forces it to be sufficient — a
  healthy constraint.
- The viz can evolve on its own (JS) stack without coupling to or destabilizing the
  backend; breaking the viz can never break ingestion or the API.
- The demo/example viz is living proof that "anyone can build on Toise", reinforcing
  the LLM-first + open-contract positioning (see also the public-positioning notes).
- The discipline this requires: resist linking a viz into the core "just for
  convenience". When in doubt, a new human surface is an edge consumer until proven
  it must be core (it rarely must).

## Relationship to other decisions

- **Extends ADR 0012** (debug UI as a minimal debug aid) from the debug UI to *all*
  human-facing visualization.
- **Complements ADR 0010** (GraphQL) and **ADR 0011** (MCP): those are the surfaces
  an edge viz consumes.
