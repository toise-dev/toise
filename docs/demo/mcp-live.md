# A live MCP session — operator ↔ assistant over the real graph

This is the LLM-first experience end to end: a real **MCP client** connects to a
running `toise-server` over Streamable HTTP, discovers the tools, and answers an
operator's questions about a **live, evolving** infrastructure graph — no bespoke
glue between the model and the backend.

It complements [`llm-prompts.md`](./llm-prompts.md) (which maps questions to the
tool calls they should make) by showing a session *running* against live data.

## Reproduce it

```bash
make build
./bin/toise-server --data-dir ./live-data &                 # GraphQL/MCP/UI on :8080
./bin/toise-probe --hosts 60 --interval 60s --heartbeat 6s  # a real OTLP producer:
                                                            # 60-machine fabric, heartbeating
```

Then point any MCP client at the server and ask questions:

- **Claude Desktop** (or any HTTP MCP client): `http://127.0.0.1:8080/mcp`.
- **stdio** (Claude Desktop subprocess): use [`claude-desktop-config.json`](./claude-desktop-config.json).

The six tools the assistant sees: `describe_schema`, `find_entities`,
`get_entity`, `get_neighbors`, `entity_history`, `recent_changes` (ADR 0011). They
read the **same in-memory projection + event log** as GraphQL and the debug UI, so
every surface reports the same world.

## The session

A real run against a live 60-host fabric (~318 entities, ~320 relations). Each
answer is the assistant's natural-language synthesis of a tool's **structured,
name-bearing** result — ids carry human labels and types, so the model reasons
without a second lookup.

> **👤 "What is this Toise instance tracking?"**
> *→ `describe_schema()`*
> **🤖** Your instance tracks **318 entities** across 8 types — **60 hosts** with
> their interfaces (60), addresses (63), routes (60) and service listeners (60),
> plus **8 PostgreSQL databases**, **6 network switches**, and the agent observing
> them — connected by **320 relations** (74 `monitors`, and a `has_interface` /
> `bound_to` / `listens_on` / `next_hop_via` mesh of 60 each, with 6 switch
> adjacencies). A small, well-wired 60-machine fleet.

> **👤 "List a few hosts."**
> *→ `find_entities(type: "host", limit: 5)`*
> **🤖** For example **host-0000** (`host.id=srv-0000`, rack-00, linux), host-0001,
> … Each host has a stable `host.id` identity, with its name and zone as
> descriptive attributes.

> **👤 "Which databases are there?"**
> *→ `find_entities(type: "db", limit: 5)`*
> **🤖** Several **PostgreSQL** instances, each keyed by a stable `db.instance.id`
> (e.g. `postgresql:7311168095704935000`), at `server.address=10.1.0.10:5432` with
> ~7 active connections. The address/port are descriptive — the identity stays put
> even if the IP moves.

> **👤 "What is connected to that host?"**
> *→ `get_neighbors(entity_id: "<host-0000 id>", max_depth: 1)`*
> **🤖** `host-0000` has **2 direct neighbours**: its **eth0** interface
> (`oper_state=up`) and **the agent** that `monitors` it. One hop further would
> surface its address, route, and listener.

> **👤 "What structural changes happened in the last 2 minutes?"**
> *→ `recent_changes(window: "2m", kind: "structural")`*
> **🤖** A wave of **`relation.added`** events — switch adjacencies, `has_interface`
> edges — the initial discovery building the topology, each stamped with both
> `event_time` (reality) and `recorded_at` (ingestion). Nothing broken; the map is
> assembling.

## Why it matters

- The model takes the **same path a real assistant takes** to reach Toise — the
  MCP server is part of the backend, not an integration bolted on (ADR 0011).
- Tool results are **name-bearing and structured**, so a single call answers the
  question.
- It runs on **live data** flowing through the real OTLP ingestion path, on the
  one read model shared with GraphQL and the debug UI.
