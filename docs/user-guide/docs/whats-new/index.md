# What's new

Release notes for Toise. The full, canonical changelog lives in
[`CHANGELOG.md`](https://github.com/toise-dev/toise/blob/main/CHANGELOG.md);
these pages are the readable summary.

Toise is pre-1.0 (alpha). Expect breaking changes between minor releases; each is
called out below with a migration path.

## Releases

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
