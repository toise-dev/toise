# Querying the graph

Toise exposes **one read model** — the current-state projection plus the
bi-temporal change log — through three surfaces. They all read the same in-memory
projection and event log, so they always report the same world.

<div class="grid cards" markdown>

-   :material-api: __GraphQL API__

    ---

    The typed, introspectable query surface for tools, dashboards, and scripts.
    Relay pagination, bi-temporal queries, live subscriptions.

    [:octicons-arrow-right-24: GraphQL API](graphql.md)

-   :material-robot-outline: __MCP for AI assistants__

    ---

    A native Model Context Protocol server so an AI assistant can query the
    graph in plain language — over stdio or Streamable HTTP.

    [:octicons-arrow-right-24: MCP server](mcp.md)

-   :material-monitor-dashboard: __Debug UI__

    ---

    A minimal, zero-dependency browser view for operators to eyeball the graph.

    [:octicons-arrow-right-24: Debug UI](debug-ui.md)

</div>

## Which one should I use?

| If you are… | Use |
| --- | --- |
| an AI assistant / LLM agent | **MCP** — typed tools, name-bearing results |
| a script, dashboard, or another service | **GraphQL** — introspectable, paginated |
| an operator wanting a quick look | **Debug UI** — open a browser, no setup |

There are **no mutations**. Toise is a read model; state enters only through the
[OTLP ingestion boundary](../ingestion.md).
