# 28. Access security for multi-tenant / external SaaS (extends ADR 0024, 0025)

- Status: Accepted
- Date: 2026-06-17
- Extends: ADR 0024 (native auth + TLS), ADR 0025 (multi-tenancy)
- Governed by: ADR 0030 (deployment tiers — every decision here is tier-2, off by default)

## Context

ADR 0024 shipped optional bearer-token auth + TLS and explicitly deferred
"mTLS, per-token scopes/identities, token rotation/hashing at rest". ADR 0025
isolated each tenant's *data* but noted that **whether a caller may reach a given
tenant is a separate, auth-layer question**. Both were sized for a self-hosted,
trusted-network deployment.

The decision to serve **external clients (SaaS, multi-tenant)** raises those
deferred items from "future work" to "must-have": an external tenant must not be
able to read another's graph, an operator must be able to prove who did what, and
human users expect federated identity. None of this may compromise the
zero-config single-binary path that drives adoption (ADR 0030).

## Decision

**Add a SaaS-grade access layer. Every mechanism below is tier-2 (ADR 0030):
opt-in, off by default; a tier-0/1 deployment is unchanged.**

1. **Tenancy trust mode — the load-bearing anti-spoofing decision.** A config
   switch governs how the tenant id is trusted:
   - `trust-header` (default, self-hosted): the current behavior — the tenant comes
     from `X-Scope-OrgID` / `tenant.id`, trusted because the network is trusted.
   - `derive-only` (SaaS): Toise **ignores any client-supplied** `X-Scope-OrgID` /
     `tenant.id`. The tenant is taken only from an authenticated channel — a value
     an upstream **authenticating gateway** sets after deriving it from the client's
     identity (strip-and-set), or a claim on a validated token (below). This is the
     same posture Mimir/Loki/Cortex document (the backend trusts the header only
     behind an authenticating proxy); we make refusing client-set tenancy explicit.
   This formalizes the strip-and-rederive model tracked in senhub-agent#240.

2. **OIDC / JWT on the read surfaces.** Alongside static bearer tokens (machine
   producers), validate JWTs against configured OIDC providers on GraphQL/MCP/UI,
   mapping claims to a tenant and a role. Mirrors the Collector's `oidc` server
   authenticator; bearer remains the baseline for headless ingest.

3. **Token lifecycle.** Accepted tokens are stored as **hashes, never plaintext**;
   minted tokens carry a typed prefix + checksum (GitHub/Stripe convention) for
   offline detection; rotation supports an overlap window; revocation is immediate.
   Closes ADR 0024's deferred "rotation/hashing at rest".

4. **Per-tenant RBAC.** Extend the 0.7.0 coarse roles (read / ingest / full) to
   **role bindings per tenant** (e.g. admin / read / ingest × tenant). A token or
   OIDC identity is authorized for a tenant *and* a role, checked per resolved
   tenant (as the per-`ResourceLogs` tenant override already is, ADR 0025).

5. **Audit log.** An append-only, per-tenant audit stream records authenticated
   access and **every write** (today: `annotate_entity`; the one mutating surface).
   It is distinct from the entity event log (which is producer truth), exportable,
   and its durability follows ADR 0029.

6. **mTLS on ingest (optional).** For regulated producers, client-certificate auth
   on the OTLP listener; bearer + TLS stays the baseline. Closes ADR 0024's
   deferred mTLS.

## Amendment — 0.9.0: ingest auth decoupled from read auth (#262)

The auth-enabled decision was global: configuring any read token also armed the
gRPC ingest interceptor, so a deployment that wanted **scoped tokens on the read
surfaces** was forced to also put a bearer on ingest — even though mTLS already
authenticates the producer. That coupled two independent surfaces.

`ingest_mtls_only` (config / `TOISE_INGEST_MTLS_ONLY`, opt-in, requires
`tls_client_ca_file`) **decouples** them: with it set, OTLP ingest is
authenticated by **mutual TLS alone** (no bearer required or consulted on
ingest), while the **read surfaces (GraphQL, MCP) keep requiring their per-client
scoped tokens / OIDC**. Default off — the existing bearer-on-ingest posture is
unchanged. The read-side capability (per-client tokens with a read/full role,
individually revocable) is the pre-existing per-tenant RBAC of point 4; #262 adds
the surface decoupling, not a new token type.

## Consequences

- External tenants can be **authenticated, isolated, and audited**; the
  storage-layer isolation of ADR 0025 gains the missing access-control boundary.
- **Tier-0/1 unchanged**: no tokens, no OIDC, `trust-header`, no audit — the
  loopback single-binary trial and the self-hosted bearer+TLS posture are
  untouched (ADR 0030 invariant).
- New subsystems to build and maintain: an OIDC validator, a hashed token store
  with lifecycle, per-tenant RBAC, and an audit sink. These live behind the
  auth boundary; the query/ingest engines stay auth-agnostic (as in ADR 0024/0025).
- The `derive-only` mode makes the **authenticating gateway a required component**
  of a SaaS deployment — documented as such, not bundled into the binary.
- Interacts with ADR 0029 (audit-log durability) and ADR 0025 (per-tenant scaling
  at large external tenant counts, flagged in ADR 0029).
