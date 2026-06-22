# Deployment tiers & SaaS operations

Toise runs the same single binary from a laptop to a hardened multi-tenant SaaS. Everything beyond the zero-config default is **opt-in and additive** — you turn on what a tier needs, the core never changes ([ADR 0030](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0030-deployment-tiers-zero-config-stays-sacred.md)). This page is the operator's path through that progression; the exhaustive knobs live in the [configuration reference](../configuration.md).

## The three tiers

| Tier | For | Posture |
|---|---|---|
| **0 — zero-config** | local, dev, forks, trials | one binary, loopback, **no auth**, Pebble on local disk, the `default` tenant, dev surfaces on. Just run it. |
| **1 — hardened single node** | internal production | `--production` (dev surfaces off), TLS, auth tokens, scheduled backups |
| **2 — multi-tenant SaaS** | external customers | tier 1 **plus** per-tenant isolation + RBAC, OIDC / mTLS, off-node durable log + read HA, tenant sharding |

A tier is just a set of options on the same binary — moving up never re-architects anything.

## Tier 0 — zero-config (the default)

`toise-server` with no flags binds loopback, ingests OTLP and serves GraphQL/MCP with **no authentication**, stores events in `./toise-data`, and routes everything to the `default` tenant. The playground, GraphQL introspection and debug UI are on. This path has **no external dependency** and is guarded by a CI smoke test so it never regresses. It is the right tier for trying Toise, a fork's test suite, or a single-user local graph.

## Tier 1 — hardening a single node

For an internal production node, layer on:

- **Lock down the surfaces:** `--production` turns off the playground, introspection, and debug UI in one switch.
- **TLS:** `tls_cert_file` + `tls_key_file` serve HTTP and OTLP over TLS.
- **Authentication:** `auth_tokens` (full access) or the role-scoped `read_tokens` / `ingest_tokens`. Tokens are **hashed at rest** — a leaked config or memory dump never exposes a usable credential ([ADR 0028](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0028-access-security-for-multi-tenant-saas.md)).
- **Backups:** `backup_dir` + `backup_interval` checkpoint every tenant on a cadence; restore by pointing a server at a checkpoint. See [Backups](backups.md). (Restart speed is already covered by projection snapshots, `snapshot_interval`, default 5m.)

## Tier 2 — multi-tenant SaaS

Serving external customers adds isolation, stronger auth, and real durability/HA. All of it is opt-in on top of tier 1.

### Tenancy & isolation

- Each tenant is its **own stack** — a separate event log and in-memory projection ([ADR 0025](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0025-multi-tenancy.md)); one tenant never sees another's graph.
- **Trust mode** decides how a request's tenant is set (`tenant_trust_mode`):
  - `trust-header` (default) — the `X-Scope-OrgID` header is trusted; correct **behind an authenticating gateway** that sets it.
  - `derive-only` — the tenant is derived from the request's scoped token and the client header is **ignored**; the strict mode for direct external exposure (a client cannot claim another tenant).
- Bound tenant creation: `tenant_auto_create`, `tenant_allowlist`, `max_tenants`.
- **Per-tenant scoped tokens:** `tenant_read_tokens` / `tenant_ingest_tokens` give a customer a token that is both role-scoped *and* tenant-scoped.

### Authentication

- **Roles:** every token is read / ingest / full; the write tools and ingest are gated accordingly.
- **OIDC / JWT** on the read surfaces: `oidc_issuer`, `oidc_audience`, `oidc_tenant_claim`, `oidc_role_claim` — verify customer SSO tokens, with the tenant and role read from claims.
- **mTLS on ingest:** `tls_client_ca_file` (requires server TLS) authenticates producers by client certificate.
- **Audit log:** `audit_log` records operator writes (e.g. `annotate_entity`) as an append-only JSON line trail.

### Resilience & HA

- **Read HA, no clustering:** run **N identical replicas** behind a load balancer; each re-ingests the same OTLP fan-out and rebuilds its own projection (the "run two Prometheus" pattern). No Raft, no ring ([ADR 0029](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0029-resilience-and-high-availability.md)).
- **Durable log off-node:** continuous **log shipping** to a directory or an **S3-compatible store** (`log_shipping_s3_*` — AWS S3, MinIO, Ceph, R2…), plus scheduled backups. Recover with `restore-log` (segments) or by opening a `checkpoint`. See [Backups](backups.md).
- **Tenant scaling:** the in-memory projection bounds tenants per node, so scale with a **per-node cap** (`max_tenants`) **plus horizontal sharding** — give each node a disjoint `tenant_allowlist` and route by `X-Scope-OrgID` at the gateway. Watch the `toise_tenants_open` gauge against the cap.

### Identity overlay (optional)

When producers assert cross-facet identity (`same_as` belief edges), Toise derives a read-time **canonical view** above `identity_confidence_threshold` (default 0.9) — `get_entity`, `impact_of` and `find_path` treat a machine's facets as one, without ever merging storage ([ADR 0020](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0020-weighted-multi-source-identity.md)).

## From zero-config to SaaS, in order

1. **Run it** — tier 0, nothing to configure.
2. **Harden** — `--production`, TLS, `auth_tokens`, `backup_dir`/`backup_interval`.
3. **Isolate** — `tenant_trust_mode: derive-only`, per-tenant scoped tokens, `tenant_allowlist` / `max_tenants`.
4. **Federate identity** — OIDC on reads, mTLS on ingest, `audit_log`.
5. **Make it durable & HA** — log shipping (S3) + read replicas; shard tenants across nodes as you grow.

Each step is independent — adopt only what your deployment needs.
