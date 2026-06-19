# 29. Resilience and high availability

- Status: Accepted
- Date: 2026-06-17 (both forks resolved 2026-06-19: log backend point 4, per-tenant scaling point 6)
- Governed by: ADR 0030 (deployment tiers — HA is opt-in; tier-0/1 stay single-node)
- Relates to: ADR 0002 (event sourcing), ADR 0007 (Pebble), ADR 0008 (in-memory
  projection), ADR 0019 (per-producer liveness), ADR 0025 (multi-tenancy)

## Context

Toise runs as a single node today: one Pebble append-log per tenant plus an
in-memory projection. That is a single point of failure — felt acutely on the
production deployment, where the *ingress* (collector + nginx behind a load
balancer) is already HA but Toise itself is not. Serving external clients with an
SLA (the SaaS direction) needs a resilience story.

A benchmark of the OTel ecosystem and comparable systems (2026-06) is decisive:
**no observability backend uses Raft/consensus on the data path**; the OTel
Collector has no native clustering by design (stateless replicas behind a load
balancer). The dominant patterns are *stateless compute + durability delegated to
object storage* (Mimir/Loki/Tempo/Thanos), *run N identical + dedup* (Prometheus
HA), and *active-passive WAL shipping* (PostgreSQL). Consensus is reserved for
control-plane metadata (etcd; Kafka KRaft) or strongly-consistent multi-writer
graphs (Neo4j core).

Toise has a property that reshapes this: **the live graph is derivable.**
Producers re-assert their state every heartbeat (ADR 0019), so a cold node
rebuilds the live graph within one heartbeat window. The only thing that is *not*
reconstructible is **history / time-travel — which lives in the event log.**

## Decision

1. **No native clustering, no consensus on the data path. Reject Raft and hash
   rings.** Toise is a single-writer, append-only store with no multi-writer
   strong-consistency requirement; the benchmark shows the whole category avoids
   consensus for data. A ring/replication-factor only earns its cost when sharding
   a dataset too large for one node — not Toise's shape (in-memory projection,
   one binary).

2. **Resilience axiom: the event log is the only durable asset; the live graph is
   derivable.** Every decision below follows from protecting the log, not the
   projection.

3. **Read HA = N stateless replicas rebuilt from producers (all tiers, no infra).**
   Several Toise instances re-ingest the same OTLP fan-out and independently
   rebuild their projection (the "run two Prometheus" pattern). Reads are served
   from any. Caveat: history/time-travel is correct only on nodes backed by the
   same log — point history queries at a log-backed node, live queries anywhere.
   This is itself an adoption argument: HA with zero extra infrastructure.

4. **Log durability / write HA = an object-store-backed durable log (tier-2, opt-in).**
   The default stays **local Pebble** (tier-0/1, ADR 0030). For tier-2 the same
   single-writer log interface gains a second implementation whose segments flush
   to object storage (S3-class). **Decided 2026-06-19: the object-store-backed log
   is the canonical first-SaaS-release backend, not active-passive log shipping.**
   - Single-writer is preserved and durability is delegated to an 11-nines service
     — the cloud-native pattern the benchmark already endorses
     (Mimir/Loki/Tempo/Thanos) and the one the resilience axiom (point 2) points to.
   - It makes the stateless read replicas (point 3) trivially correct: every
     replica opens the *same* object-store log; there is no per-standby shipping
     channel to operate.
   - It is the continuous limit of the scheduled off-node backup (point 5) — ship
     WAL segments instead of periodic full checkpoints, so RPO collapses from
     `backup_interval` to a segment-flush window. Write latency is hidden the way
     Loki/Tempo do it: the local Pebble WAL gives immediate durability while
     segments upload asynchronously.
   - **Active-passive is the rejected alternative.** A divergent double-writer
     corrupts the log, so it would need fencing/promotion orchestration
     (consensus-adjacent, exactly what point 1 avoids) plus a second always-on
     node — for an RPO no better than object-store shipping.
   The interface still *admits* an active-passive implementation for an on-prem
   tier-2 deployment without S3-class storage, but that is not a first-release
   target and carries the fencing cost above.

5. **Backup/restore, elevated.** The existing cold `checkpoint` (per-tenant Pebble
   copy) gains a scheduled off-node backup + a documented restore-by-replay
   runbook. Works at every tier.

6. **Per-tenant scaling = a per-node cap plus horizontal tenant sharding; keep
   stack-per-tenant.** ADR 0025 chose a Pebble stack *per tenant* and deferred a
   key-prefixed single store. **Decided 2026-06-19: cap tenants per node
   (`max_tenants`) and shard tenants across nodes — reject the partitioned single
   store as the 1.0 path** (ADR 0025 stands).
   - The dominant per-tenant cost is the **in-memory projection** (a graph per
     tenant in RAM), not the Pebble instance. A partitioned single store collapses
     the per-instance overhead but leaves the projection cost — the real ceiling —
     untouched, while discarding the clean per-tenant isolation ADR 0025 chose.
   - RAM therefore bounds the tenants a node holds → a **per-node cap** is the
     natural primitive (`max_tenants`, already enforced). Beyond it, **shard**:
     each node owns a disjoint subset via `tenant_allowlist`, routed by
     `X-Scope-OrgID` at the gateway — the Mimir/Cortex/Loki pattern, and consistent
     with point 1 (no ring on the data path).
   - Sharding composes with the durable log (point 4): any node's tenants are
     reconstructible elsewhere, so re-sharding or node loss is a reassignment +
     replay, not a data migration. An `toise_tenants_open` gauge makes the cap
     observable for capacity planning.
   - **Escape hatch:** a partitioned store is revisited only if a future workload is
     genuinely *very many tiny tenants* (projection memory negligible, per-instance
     overhead dominant) — not the LLM-first infra-graph shape (tens of clients, rich
     graphs), so not the 1.0 path.

7. **State SLA targets for tier-2 (object-store-backed).** RPO = the unshipped-
   segment window (local-WAL flush + async-upload cadence, seconds-scale and
   configurable); a hard node loss forfeits at most that last window of *history*,
   never live state (producers re-assert within one heartbeat). RTO = node start +
   open the object-store log + projection rebuild (snapshot + tail), bounded by one
   heartbeat window for the live graph; a pre-warmed read replica is effectively
   zero-RTO for live queries and gains full history the moment it opens the shared
   log.

## Consequences

- HA at tier-0/1 needs **no extra infrastructure** (read replicas reconstruct
  themselves); the single-node default is unchanged (ADR 0030).
- Tier-2 gets real durability and failover **without Raft, without a ring** —
  matching how the ecosystem actually builds HA.
- The storage layer gains a **durable-log backend interface** (Pebble-local being
  one implementation). This is the main new engineering surface; it must not leak
  into tier-0/1 (no object store or external dependency to develop, build, or test
  — ADR 0030).
- Audit-log durability (ADR 0028) rides the same backend choice.
- The first-release durable-log backend is **object-store-backed** (point 4,
  resolved 2026-06-19) and per-tenant scaling is **a per-node cap plus horizontal
  sharding, keeping stack-per-tenant** (point 6, resolved 2026-06-19). No open
  items remain; the ADR's forks are all closed.
