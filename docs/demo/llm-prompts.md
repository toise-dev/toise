# LLM example prompts

These are example operator questions for the [demo scenario](./scenario.md),
each with the **MCP tool call(s)** an assistant is expected to make (the tools
are defined in ADR 0011) and the **shape of the answer** it should be able to
give from the result. They double as a manual acceptance checklist for the MCP
surface.

Seed and serve the scenario first:

```bash
./bin/toise-demo --data-dir ./demo-data
./bin/toise-server --data-dir ./demo-data
```

Logical entity ids are ULIDs assigned at runtime, so most flows start by
**finding** an entity, then passing its `id` to a follow-up tool. The expected
calls below use names like `<nginx-id>` for those resolved ids.

---

## 1. Bootstrap — what does this instance know? (schema)

> *"What kind of infrastructure does this Toise instance track?"*

- **Tool:** `describe_schema()`
- **Answer shape:** a natural-language summary plus per-type counts — ~9 entities
  across `host`, `process`, `network.interface`, `network.address`,
  `network.route`, `service.listener`, connected by `runs_on`, `has_interface`,
  `bound_to`, `next_hop_via`, `listens_on` relations.

## 2. Current inventory (current state)

> *"List the processes running on web-server-1."*

- **Tool:** `find_entities(type: "process")`
- **Answer shape:** two live processes — `nginx` (pid 1010) and `postgres`
  (pid 3003). `dockerd` is **not** listed (it crashed). Each result carries a
  human-readable label so the model can name them without another call.

## 3. Entity detail

> *"Show me everything about the nginx process."*

- **Tools:** `find_entities(type: "process", match: {"process.executable.name": "nginx"})`
  → `get_entity(entity_id: "<nginx-id>")`
- **Answer shape:** nginx's identity (`process.pid=1010` +
  `process.creation.time`) and descriptive attributes
  (`process.executable.name=nginx`, `status=running`), with its stable logical id.

## 4. Topology — direct neighbors (traversal)

> *"What is attached to eth0?"*

- **Tools:** `find_entities(type: "network.interface")` → `get_neighbors(entity_id: "<eth0-id>", depth: 1)`
- **Answer shape:** the host (`has_interface`), the bound address `10.0.2.7`
  (`bound_to`), and the two service listeners `:80` and `:5432` (`listens_on`).

## 5. Topology — two hops out (traversal, depth cap)

> *"Starting from web-server-1, what does it run and what is it connected to, two hops out?"*

- **Tools:** `find_entities(type: "host")` → `get_neighbors(entity_id: "<host-id>", depth: 2)`
- **Answer shape:** hop 1 = its processes (`nginx`, `postgres`) and `eth0`; hop 2
  via `eth0` = the address and listeners. Asking for `depth: 7` instead returns a
  friendly "depth 7 exceeds the maximum of 5" error rather than a huge result.

## 6. What changed recently? (recent changes)

> *"What changed in the last 6 hours?"*

- **Tool:** `recent_changes(window: "6h")`
- **Answer shape:** newest-first list of qualified changes in the window —
  depending on when you seeded, the nginx reload (`entity.attribute_updated`, the
  `status` attribute flipping) and restart (`entity.deleted` + `entity.created`, a
  new process per the semconv identity), the dockerd crash (`entity.deleted` +
  `relation.removed`), and the host heartbeat (`entity.unchanged`).

## 7. Structural alerts only (anomaly)

> *"Have any structural topology changes happened today? I only care about things appearing or disappearing."*

- **Tool:** `recent_changes(window: "24h", kind: "structural")`
- **Answer shape:** only the structural relation add/removes — e.g. dockerd's
  `runs_on` removal, the `bound_to`/`next_hop_via` moves during the subnet and
  gateway changes. Routine attribute updates are filtered out.

## 8. Entity history (history)

> *"Show me the full history of eth0."*

- **Tools:** `find_entities(type: "network.interface")` → `entity_history(entity_id: "<eth0-id>")`
- **Answer shape:** oldest-first timeline — created (up) at S+0, went down at
  S+6h, back up at S+6h30 — each with `event_time` and `recorded_at`.

## 9. Causal — explain a change (causal)

> *"Why is the default route pointing at 10.0.2.1 now? What happened?"*

- **Tools:** `find_entities(type: "network.route")` → `entity_history(entity_id: "<route-id>")`,
  optionally `recent_changes(window: "24h")` to see the surrounding events.
- **Answer shape:** the route's `next_hop` attribute was updated from `10.0.1.1`
  to `10.0.2.1` at S+12h (`entity.attribute_updated`), alongside the new gateway
  address appearing and the old one being deleted — the gateway-change beat.

## 10. Anomaly — did anything disappear? (anomaly)

> *"Did any process crash or disappear in the last day?"*

- **Tool:** `recent_changes(window: "24h", kind: "entity")`
- **Answer shape:** `dockerd` was deleted (`entity.deleted`) at S+22h and **not
  replaced** — the container crash. nginx also shows an `entity.deleted` at S+18h,
  but immediately followed by an `entity.created` (a new process, new
  pid + `process.creation.time`) — a restart, not a crash. The delete-with-no-create
  vs delete-then-create pairing is the signal.

## 11. Audit — what did we know then? (asKnownAt)

> *"As of 10 minutes after eth0 went down, what did Toise believe eth0's state was?"*

- **Tools:** `find_entities(type: "network.interface")` →
  `entity_history(entity_id: "<eth0-id>", as_known_at: "<S+6h10>")`
- **Answer shape:** because the down observation was **recorded 20 minutes late**,
  an `as_known_at` cut-off at S+6h10 still shows `eth0` as **up** — the audit view
  reflects what Toise had recorded by that instant, not what was already true
  (ADR 0005). Without `as_known_at`, the same history shows it down.

## 12. Identity continuity (history / identity)

> *"Did nginx restart? I want to confirm it's the same service, not a new one."*

- **Tools:** `recent_changes(window: "24h", kind: "entity")` →
  `find_entities(type: "process", match: {"process.executable.name": "nginx"})`
- **Answer shape:** the changes show an `entity.deleted` (pid 1001) immediately
  followed by an `entity.created` (pid 1010) at S+18h — a **new process entity**,
  because the OTel-semconv process identity is `process.pid` + `process.creation.time`
  and a restart changes both. So it is the *same service* (same
  `process.executable.name=nginx`, same host, same `:80` listener) but a genuinely
  *new process* — Toise models the restart honestly rather than papering over it
  (ADR 0018). The earlier config *reload* at S+15h, by contrast, kept the same
  identity and is an `entity.attribute_updated`.
