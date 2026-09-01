# What's new

Release notes for Toise. The full, canonical changelog lives in
[`CHANGELOG.md`](https://github.com/toise-dev/toise/blob/main/CHANGELOG.md);
these pages are the readable summary.

Toise is pre-1.0 (alpha), but since 0.7.0 the public contracts (the OTLP wire
contract, the MCP surface, and the GraphQL schema) are pinned and evolve
**additively** within a release series. A breaking change to them ships only with
a deprecation notice in the preceding release and a migration path — called out
below. See the [API stability policy](../api-stability.md).

## Releases

- [**0.16.0**](0.16.0.md) — 2026-09-01 — **the release that keeps its promises
  at scale**: the incident-window reads answer in milliseconds on a
  10,000-host estate (7.5 s → 21 ms, by classifying the time index so a read
  skips what it excludes); every MCP answer carries a `graph` block declaring
  its own scope and freshness — coverage, newest event, prune horizon, `as_of`;
  operator annotations key on identity and travel through the shared object
  store, so a cluster's nodes finally agree on them; and the resurrection
  grace window, per-tenant retention, and the OTLP export size cap become
  configuration instead of constants.

- [**0.15.0**](0.15.0.md) — 2026-08-20 — **the release that answers**: the MCP
  server now carries `instructions`, the one guidance channel every client
  receives at initialize and which had been empty; every disappearance is glossed
  in plain language, stating what it is *not* (none of `producer`,
  `liveness_expiry`, `cascade` means an operator removed anything);
  `recent_changes` accepts `from`/`to` so a past incident window is reachable, and
  a truncated answer now names the slice it actually covered instead of reading as
  a complete one; GraphQL history excludes heartbeats by default, matching MCP.

- [**0.14.0**](0.14.0.md) — 2026-08-17 — **reachability, and the first
  deprecation**: a fourteenth entity type, `network.segment`, makes *why can't A
  reach B* a graph question (`has_segment` from the declaring cluster,
  `attached_to` from the container or pod), with only the `swarm:` identity
  subtype frozen; and the 1.0 freeze audit opens by deprecating the three legacy
  relation types for removal at 1.0.
- [**0.13.0**](0.13.0.md) — 2026-08-13 — **one answer per question**: the ADR 0020
  `same_as` identity overlay reaches GraphQL as `canonical(id, asOf)`, computed by
  the same walk and threshold as MCP so the two surfaces cannot disagree; the
  `host.id` rendering is pinned and the conformance kit **fails** on a raw
  machine-id, which silently duplicates every host; and the `db.instance.id`
  fallback is specified as `<db.system.name>:<port>@<host.id>`.
- [**0.12.0**](0.12.0.md) — 2026-08-11 — **the pod**: a thirteenth entity type for
  the Kubernetes unit of scheduling and of failure, identified by the UID
  Kubernetes assigns, composing from the existing vocabulary as
  `container runs_on pod runs_on host`. Ships to producers as `wire.TypePod` in
  `pkg/emit/v0.7.0`.
- [**0.11.0**](0.11.0.md) — 2026-08-11 — **vocabulary & correlation**:
  `telemetry_keys` stops inheriting join keys through observation and peer
  relations, so an entity no longer answers with its observer's identity, and an
  empty answer now means *no key exists*; the entity and relation type vocabulary
  is exported from `pkg/emit/wire` (`pkg/emit/v0.6.0`), with Toise's own registry
  derived from it; typed descriptive attributes become the documented normal path.
- [**0.10.0**](0.10.0.md) — 2026-07-30 — **delete provenance & host-scoped
  endpoints**: `delete_source` says who authored every disappearance (producer /
  liveness expiry / cascade) on MCP and GraphQL; loopback/link-local endpoints gain
  a fourth identity key (`host.id`) with scope-honoring resolution; `telemetry.relay.*`
  contract keys and the effective-cadence interval sizing rule; grpc/Go security
  bumps. *(No wire-contract break, no data migration; all additive.)*
- [**0.9.0**](0.9.0.md) — 2026-07-01 — **strict spec alignment & read-surface
  security**: full `AnyValue` in `entity.description`, `entity.delete.reason`,
  `entity.report.interval == 0` = no cadence, and `ingest_mtls_only` decoupling ingest
  mTLS from read tokens — plus an out-of-order relation-buffer fix and a Go 1.26.4
  security bump. *(No wire-contract break, no data migration; all additive.)*
- [**0.8.0**](0.8.0.md) — 2026-06-22 — **the SaaS-readiness release**: access
  security (derive-only tenancy, hash-at-rest, per-tenant RBAC, OIDC, mTLS ingest,
  audit log), resilience/HA (backups, log shipping incl. S3, `restore-log`, read
  replicas, tenant sharding), a multi-source identity overlay (`same_as` + canonical
  view), and an attribute-enrichment pass — all opt-in; the zero-config path is
  unchanged.
- [**0.7.0**](0.7.0.md) — 2026-06-15 — **the integration release**: operator
  annotations (MCP tool + first GraphQL mutation, an overlay), MCP resources and
  prompts, read-only / ingest-only token roles, verbosity tiers, the
  `toise-conformance` CLI + producer directory, identity-stable resurrection,
  and the audit P1/P2 lot.
- [**0.6.0**](0.6.0.md) — 2026-06-12 — **the corrective release**: the entire
  P0 lot of the 0.5.0 audit in one pass — `emit.PartialError`, exact
  conformance claim, read-only `checkpoint`, maintenance-safe reads, liveness
  memento on by default, standard Go versioning (`v`-tags, independent
  `pkg/emit` module).
- [**0.5.0**](0.5.0.md) — 2026-06-11 — **time travel and the producer SDK**:
  `as_of` on every read surface, `impact_of` blast radius, `describe_type`,
  subscription filters with an honest gap signal, restart-surviving liveness,
  bounded tombstones, opt-in open vocabulary, and the `toise-emit` SDK with a
  byte-pinned published contract. *(No wire-contract change, no data
  migration; sharper defaults — see the migration guide.)*
- [**0.4.0**](0.4.0.md) — 2026-06-10 — **correctness and LLM querying**: the
  audit-driven integrity lot (log-is-truth under failure, clean restarts),
  three new MCP tools (`graph_diff`, `find_path`, `telemetry_keys`), result
  budgets with digests, ingest observability, and tenant security (scoped
  tokens, bounded creation). *(No wire-contract change, no data migration;
  sharper defaults — see the migration guide.)*
- [**0.3.0**](0.3.0.md) — 2026-06-08 — production-readiness and **multi-tenancy**:
  per-tenant isolated graphs, native auth + TLS, operational endpoints, retention,
  snapshots, and packaged release artifacts. *(No wire-contract change; on-disk
  layout auto-migrated.)*
- [**0.2.0**](0.2.0.md) — 2026-06-08 — embedded relationships, realignment onto
  the **merged** OpenTelemetry entity-events spec, and layered configuration.
  *(Breaking wire contract.)*
- **0.1.1** — 2026-06-02 — accept gzip-compressed OTLP exports (the OTel SDK
  default); first real-world validation against the senhub-agent producer.
- **0.1.0** — 2026-06-02 — first tagged release: the phase-1 backend (OTLP
  ingestion, bi-temporal event log, in-memory graph, GraphQL + MCP + debug UI).

## Versioned docs

This documentation is versioned with the project. Use the version selector in the
header to switch between the **latest** release and the in-progress **dev** docs.
