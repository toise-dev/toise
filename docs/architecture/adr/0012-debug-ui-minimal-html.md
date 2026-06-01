# 12. Debug UI as minimal server-rendered HTML

- Status: Accepted
- Date: 2026-06-01

## Context

Toise is LLM-first: the GraphQL API (ADR 0010) and the MCP server (ADR 0011) are
the surfaces the product is built around. But an operator developing, debugging,
or demoing Toise needs to *see* the live graph directly — without composing a
GraphQL query or driving an LLM — to answer "did my events land?", "what does
this entity look like now?", and "what just changed?".

That need is a debug aid, not a product surface. It should cost almost nothing to
build and maintain, add no runtime dependencies to the single Go binary, and not
become a second front-end that drifts from the real consumer paths.

## Decision

We will ship a **minimal, server-rendered HTML debug UI** in package
`internal/debugui`, mounted at `/` on the existing HTTP server.

- **Server-rendered `html/template`, no client framework.** Templates are
  embedded in the binary with `//go:embed`; there are no external assets, fonts,
  or build step, and no JavaScript beyond one progressive-enhancement
  `onchange` submit (with a `<noscript>` button fallback). This keeps the single
  self-contained binary self-contained.
- **One read model.** The UI reads the same in-memory projection (current state,
  ADR 0008) and event log (history, ADR 0007) as GraphQL and MCP, through narrow
  `Graph`/`EventReader` interfaces. It never gets its own data path, so it cannot
  show a different world than the other surfaces.
- **Four read-only pages:** a dashboard (entity/relation type counts, totals, and
  recent changes), an entity list (filterable by type), an entity detail page
  (identity, attributes, directly-connected neighbors with the relation that
  links them, and the full change history oldest-first), and a recent-changes
  view (duration window + entity/relation/structural filter).
- **Read-only and safe by construction.** No mutation endpoints. All dynamic
  values render through `html/template`, whose contextual auto-escaping prevents
  an attribute value from injecting markup.
- **Routing.** GraphQL stays at `/graphql`, its playground moves to
  `/playground`, MCP stays at `/mcp`, and the debug UI takes `/`. The Go 1.22+
  `ServeMux` routes the specific API paths first and everything else to the UI.

## Consequences

- Operators get an immediate window into the graph with no query language and no
  external tooling.
- Because the UI shares the read model, it stays consistent with the API surfaces
  for free, and it doubles as a smoke test that the projection and log are wired
  correctly.
- The UI carries **no authentication**, like the rest of phase 1 (ADR 0014); it
  binds to loopback by default and is for trusted networks only.
- It is deliberately not a dashboard product: richer visualisation, search, and
  topology graphics are out of scope for phase 1.

See also: ADR 0007 (event log, history source), ADR 0008 (projection, current
state source), ADR 0010 (GraphQL), ADR 0011 (MCP), ADR 0014 (no auth in phase 1).
