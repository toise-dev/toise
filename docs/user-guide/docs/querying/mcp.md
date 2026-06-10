# MCP for AI assistants

Toise is **LLM-first**: its primary consumer is an AI assistant querying on an
operator's behalf. A native [Model Context Protocol](https://modelcontextprotocol.io)
(MCP) server is **part of the backend**, not an integration bolted on later — it
is the path the assistant actually takes to reach Toise. It reads the same
in-memory projection and event log as [GraphQL](graphql.md) and the
[debug UI](debug-ui.md), so every surface reports the same world
([ADR 0011](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0011-mcp-server-design.md)).

## Transports

| Transport | Address | Use |
| --- | --- | --- |
| **Streamable HTTP** | `http://<listen>/mcp` (default `127.0.0.1:8080/mcp`) | web-based MCP clients, remote assistants |
| **stdio** | `toise-server --mcp-stdio` | Claude Desktop and other local clients that launch a subprocess |

`--mcp-stdio` makes the process a **pure local MCP server**: it disables the HTTP
and OTLP servers and just reads the given data directory.

## The tools

The assistant sees nine typed tools. Each carries a rich description and examples
so the model picks the right one, and each returns **structured, name-bearing**
results — ids carry human labels and types — so a single call answers the
question without a second lookup.

| Tool | What it does |
| --- | --- |
| `describe_schema()` | a natural-language description of the entity and relation types currently in the graph, to bootstrap the model's understanding |
| `find_entities(type, match, limit)` | entities matching a type / attribute filter, with `total`/`truncated` |
| `get_entity(id)` | a full entity with its attributes |
| `get_neighbors(entity_id, relation_type, depth)` | traverse relations up to `depth` (**capped at 5**); each neighbor carries the relation type, direction, and hop distance that reached it |
| `find_path(from_id, to_id, relation_type, max_depth)` | the shortest relation path between two entities; `reachable: false` is a first-class answer, never an error |
| `entity_history(entity_id, since, until, ...)` | an entity's timeline from the event log (bi-temporal), heartbeats excluded by default, bounded by `limit`, with a per-type digest |
| `recent_changes(window, kind, change_type, ...)` | recent qualified changes across the graph — same budget and digest contract |
| `graph_diff(window | from/to, limit)` | the folded **net** difference between two instants: created / deleted / changed / **transient** (flapping) entities and relations, churn collapsed away |
| `telemetry_keys(entity_id)` | the OTel resource attributes that locate this entity's metrics and logs in observability backends — own and 1-hop-inherited keys, each with its flattened metric-label spelling and usage caveats |

**Budgets.** The timeline tools exclude `entity.unchanged` heartbeats unless
asked (`include_heartbeats`), bound their output (`limit`, default 50, max 200),
and report a digest — `total`, `truncated`, `heartbeats_excluded`, counts per
change type — so the model can narrow instead of paging blind. Every tool call
runs under a 30-second budget.

Errors are plain, user-friendly messages (e.g. "depth 7 exceeds the maximum of
5"), never stack traces.

## Connect Claude Desktop (stdio)

Add the `toise` entry to your `claude_desktop_config.json` under `mcpServers`,
replacing the absolute paths with yours:

```json
{
  "mcpServers": {
    "toise": {
      "command": "/usr/local/bin/toise-server",
      "args": [
        "--mcp-stdio",
        "--data-dir",
        "/var/lib/toise/toise-data"
      ]
    }
  }
}
```

Claude Desktop launches `toise-server` as a subprocess and speaks MCP over
stdio. A copy of this config ships at
[`docs/demo/claude-desktop-config.json`](https://github.com/toise-dev/toise/blob/main/docs/demo/claude-desktop-config.json).

## Connect an HTTP MCP client

Point any Streamable-HTTP MCP client at a running server:

```text
http://127.0.0.1:8080/mcp
```

The server must be running normally (not in `--mcp-stdio` mode) so the HTTP
listener is up.

## A live session

A real run against a live 60-host fabric (~318 entities, ~320 relations). Each
answer is the assistant's natural-language synthesis of a tool's structured
result:

> **"What is this Toise instance tracking?"** — `describe_schema()`
>
> *Your instance tracks 318 entities across 8 types — 60 hosts with their
> interfaces, addresses, routes and service listeners, plus 8 PostgreSQL
> databases, 6 network switches, and the agent observing them — connected by 320
> relations. A small, well-wired 60-machine fleet.*

> **"What is connected to host-0000?"** — `get_neighbors(entity_id, depth: 1)`
>
> *`host-0000` has 2 direct neighbours: its `eth0` interface (`oper_state=up`)
> and the agent that `monitors` it. One hop further would surface its address,
> route, and listener.*

> **"What structural changes happened in the last 2 minutes?"** —
> `recent_changes(window: "2m", kind: "structural")`
>
> *A wave of `relation.added` events — switch adjacencies, `has_interface` edges
> — each stamped with both `event_time` (reality) and `recorded_at` (ingestion).
> Nothing broken; the map is assembling.*

Reproduce it:

```bash
make build
./bin/toise-server --data-dir ./live-data &
./bin/toise-probe --hosts 60 --interval 60s --heartbeat 6s
```

More example operator questions, each mapped to the tool calls they should make,
are in
[`docs/demo/llm-prompts.md`](https://github.com/toise-dev/toise/blob/main/docs/demo/llm-prompts.md).

## Why it matters

- The model takes the **same path a real assistant takes** to reach Toise — MCP
  is part of the backend, not glue.
- Tool results are **name-bearing and structured**, so one call answers the
  question.
- It runs on **live data** flowing through the real OTLP path, on the one read
  model shared with GraphQL and the debug UI.
