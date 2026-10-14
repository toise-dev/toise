# Demo scenario — a day in the life of `web-server-1`

This is the phase-1 demonstration fixture. It simulates **24 hours** of one
host's infrastructure evolving, applied through the change engine exactly as
live OTLP ingestion would be, so it exercises the whole pipeline and **every
change type the engine emits** (eight of the nine taxonomy types, ADR 0006;
`entity.identity_changed` is retired under ADR 0018).

Seed it and explore:

```bash
make build
./bin/toise-demo --data-dir ./demo-data          # timeline ends ~now
./bin/toise-server --data-dir ./demo-data         # debug UI on http://127.0.0.1:8080/
```

By default the scenario starts 24 hours before now, so the timeline ends about
the moment you seed it — `recent_changes` over a few hours will show the latest
beats. Pass `--start 2026-06-01T00:00:00Z` for fixed, reproducible timestamps.

The fixture itself lives in `internal/demo`; the LLM example prompts are in
[`llm-prompts.md`](./llm-prompts.md).

## The timeline

All times are offsets from the scenario start `S`.

| When | What happens | Entities & relations | Change types |
|------|--------------|----------------------|--------------|
| **S+0** | **Discovery.** `web-server-1` and its initial topology appear. | host `web-server-1`; process `nginx` (pid 1001) `runs_on` host; interface `eth0` (up), `host has_interface eth0`; address `10.0.1.5` `bound_to eth0`; gateway address `10.0.1.1`; default route `0.0.0.0/0` via `10.0.1.1` (`metric` 100), `host has_route` it and it `next_hop_via` the gateway; service listener `:80` `listens_on eth0`. | `entity.created`, `relation.added` |
| **S+2h** | **New container daemon.** `dockerd` starts. | process `dockerd` (pid 2002) `runs_on` host. | `entity.created`, `relation.added` |
| **S+6h** | **`eth0` goes down.** Recorded **20 min late** (an observation that arrived after the fact became true) — so an `asKnownAt` audit before S+6h20 still sees `eth0` up. | `eth0` `oper_state` up→down. | `entity.state_changed` (late `recorded_at`) |
| **S+6h30** | **`eth0` comes back on a new subnet.** | `eth0` up again; `10.0.1.5` unbound and deleted; new address `10.0.2.7` `bound_to eth0`. | `entity.state_changed`, `relation.removed`, `entity.deleted`, `entity.created`, `relation.added` |
| **S+9h** | **postgres starts** and listens on `:5432`. | process `postgres` (pid 3003) `runs_on` host; listener `:5432` `listens_on eth0`. | `entity.created`, `relation.added` |
| **S+12h** | **Default gateway changes.** The next hop is part of a route's identity (`{host.id, route.destination, next_hop.ip}`), so this is **not** an attribute update: the old route dies and a new one appears. Both ride **one batch and share a `commit_seq`**, which is what lets a consumer read them as one observation of a changed route rather than two events that happened near each other. | new gateway `10.0.2.1`; route `0.0.0.0/0 via 10.0.1.1` **deleted** and `0.0.0.0/0 via 10.0.2.1` **created**, with its own `has_route` and `next_hop_via`; old gateway `10.0.1.1` deleted; `10.0.2.7`'s `bound_to` marked no-longer-`preferred`. | `entity.created`, `entity.deleted`, `relation.added`, `relation.removed`, `relation.attribute_changed` |
| **S+13h** | **The same route's metric changes.** The metric is descriptive, so the entity survives and its attribute moves. Beside S+12h this is the identity rule in two events: change what identifies a thing and you get a different thing; change what merely describes it and you get the same thing, updated. | route `0.0.0.0/0 via 10.0.2.1` `metric` 100→200. | `entity.attribute_updated` |
| **S+15h** | **nginx config reload.** The same process (`process.pid` + `process.creation.time` unchanged) flips a descriptive attribute. | `nginx` `status` running→reloading→running. | `entity.attribute_updated` |
| **S+18h** | **nginx restarts.** A real restart yields a new pid **and** a new `process.creation.time`, so per the OTel-semconv identity (`process.pid` + `process.creation.time`) it is a genuinely new process, not the same one (ADR 0018). | old `nginx` (pid 1001) deleted, new `nginx` (pid 1010) created; `runs_on` moves. | `entity.deleted`, `entity.created`, `relation.removed`, `relation.added` |
| **S+22h** | **The container crashes.** `dockerd` disappears; a host heartbeat confirms nothing else changed. The `runs_on` edge is **not** retracted separately — deleting the entity cascades it, so its removal carries **`delete_source=cascade`**, which a consumer must not read as someone having removed anything. | `dockerd` is deleted and its `runs_on` edge cascades; host re-observed unchanged. | `entity.deleted`, `relation.removed` (cascade), `entity.unchanged` |

## Final state

After 24 hours the live graph holds **9 entities** and **8 relations**:

- host `web-server-1`
- processes `nginx` (pid 1010) and `postgres` (pid 3003) — both `runs_on` the host
- interface `eth0` (up), with the host's `has_interface` edge
- addresses `10.0.2.7` (`bound_to eth0`) and gateway `10.0.2.1`
- default route `0.0.0.0/0 via 10.0.2.1` (`metric` 200), `has_route` from the host and `next_hop_via` the gateway
- listeners `:80` and `:5432`, each `listens_on eth0`

Gone (deleted): `dockerd`, address `10.0.1.5`, old gateway `10.0.1.1`. Their
history remains in the event log and is visible via `entity_history` and the
`asKnownAt` audit view.
