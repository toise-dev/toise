---
hide:
  - navigation
  - toc
---

# Toise documentation

**The living map of your infrastructure.** Toise is an open-source backend that
maintains a live, queryable graph of your infrastructure — hosts, processes,
network interfaces, addresses, routes, services, and the relationships between
them — built entirely from OpenTelemetry entity events and designed so an AI
assistant can answer an operator's questions about it directly.

[Get started](installation.md){ .md-button .cta }
[What is Toise?](overview.md){ .md-button }

<div class="grid cards" markdown>

-   :material-rocket-launch: __Installation__

    ---

    Build the single Go binary and bring up `toise-server` in a couple of
    minutes — no cluster, no orchestrator, no external runtime.

    [:octicons-arrow-right-24: Install and run](installation.md)

-   :material-cog: __Configuration__

    ---

    Configure listeners, storage, liveness and retention from a YAML file,
    environment variables, or flags — in that order of precedence.

    [:octicons-arrow-right-24: Configure toise-server](configuration.md)

-   :material-import: __Ingesting data__

    ---

    Feed the graph with OTLP entity events from any OpenTelemetry producer.
    Toise runs no collectors of its own.

    [:octicons-arrow-right-24: OTLP ingestion](ingestion.md)

-   :material-graph-outline: __Querying the graph__

    ---

    Three surfaces over one read model: GraphQL for tools, MCP for AI
    assistants, and a zero-dependency debug UI for operators.

    [:octicons-arrow-right-24: Query surfaces](querying/index.md)

-   :material-database-outline: __Data model__

    ---

    Entities, attributes, relations, and the bi-temporal change log — and how
    they map onto the OpenTelemetry entity data model.

    [:octicons-arrow-right-24: The data model](data-model.md)

-   :material-history: __What's new__

    ---

    Release notes, breaking changes, and migration guides.

    [:octicons-arrow-right-24: Release notes](whats-new/index.md)

</div>

!!! warning "Pre-1.0 (alpha)"

    Toise binds to loopback with **no authentication by default**. For an exposed
    deployment, turn on bearer-token auth, TLS, and `--production` — see
    [Authentication & TLS](configuration.md#authentication--tls). Expect breaking
    changes between releases (each with a migration guide).
