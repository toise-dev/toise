# Demo scenario — a day in the life of `web-server-1`

This is the phase-1 demonstration fixture. It simulates **24 hours** of one
host's infrastructure evolving, applied through the change engine exactly as
live OTLP ingestion would be, so it exercises the whole pipeline and **every
change type** in the taxonomy (ADR 0006).

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
| **S+0** | **Discovery.** `web-server-1` and its initial topology appear. | host `web-server-1`; process `nginx` (pid 1001) `runs_on` host; interface `eth0` (up), `host has_interface eth0`; address `10.0.1.5` `bound_to eth0`; gateway address `10.0.1.1`; default route `0.0.0.0/0` (next_hop 10.0.1.1) `next_hop_via` gateway; service listener `:80` `listens_on eth0`. | `entity.created`, `relation.added` |
| **S+2h** | **New container daemon.** `dockerd` starts. | process `dockerd` (pid 2002) `runs_on` host. | `entity.created`, `relation.added` |
| **S+6h** | **`eth0` goes down.** Recorded **20 min late** (an observation that arrived after the fact became true) — so an `asKnownAt` audit before S+6h20 still sees `eth0` up. | `eth0` `oper_state` up→down. | `entity.state_changed` (late `recorded_at`) |
| **S+6h30** | **`eth0` comes back on a new subnet.** | `eth0` up again; `10.0.1.5` unbound and deleted; new address `10.0.2.7` `bound_to eth0`. | `entity.state_changed`, `relation.removed`, `entity.deleted`, `entity.created`, `relation.added` |
| **S+9h** | **postgres starts** and listens on `:5432`. | process `postgres` (pid 3003) `runs_on` host; listener `:5432` `listens_on eth0`. | `entity.created`, `relation.added` |
| **S+12h** | **Default gateway changes.** | new gateway `10.0.2.1`; route's `next_hop` attribute 10.0.1.1→10.0.2.1; `next_hop_via` edge moves to the new gateway; old gateway `10.0.1.1` deleted; `10.0.2.7`'s `bound_to` marked no-longer-`preferred`. | `entity.created`, `entity.attribute_updated`, `relation.removed`, `relation.added`, `entity.deleted`, `relation.attribute_changed` |
| **S+18h** | **nginx restarts.** Same logical process, new pid — tolerant identity matching keeps the logical id stable (ADR 0017). | `nginx` pid 1001→1010. | `entity.identity_changed` |
| **S+22h** | **The container crashes.** `dockerd` disappears; a host heartbeat confirms nothing else changed. | `dockerd` stops `runs_on` host and is deleted; host re-observed unchanged. | `relation.removed`, `entity.deleted`, `entity.unchanged` |

## Final state

After 24 hours the live graph holds **9 entities** and **7 relations**:

- host `web-server-1`
- processes `nginx` (pid 1010) and `postgres` (pid 3003) — both `runs_on` the host
- interface `eth0` (up), with the host's `has_interface` edge
- addresses `10.0.2.7` (`bound_to eth0`) and gateway `10.0.2.1`
- default route `0.0.0.0/0` → `next_hop_via` `10.0.2.1`
- listeners `:80` and `:5432`, each `listens_on eth0`

Gone (deleted): `dockerd`, address `10.0.1.5`, old gateway `10.0.1.1`. Their
history remains in the event log and is visible via `entity_history` and the
`asKnownAt` audit view.
