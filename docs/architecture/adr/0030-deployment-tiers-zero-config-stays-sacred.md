# 30. Deployment tiers — the zero-config single binary stays sacred

- Status: Accepted
- Date: 2026-06-17
- Governs: ADR 0028 (access security), ADR 0029 (resilience)
- Relates to: ADR 0014/0024 (auth off by default), ADR 0021 (human interfaces at
  the edge, not the core), ADR 0025 (single-tenant default)

## Context

The path to external/SaaS readiness adds real weight: an authenticating gateway,
OIDC, per-tenant RBAC, audit, a durable-log backend, HA (ADR 0028, 0029). The risk
is that this weight buries the thing that actually drives adoption, forks, and
contribution: **"clone it, `go run`, point a producer at it, see a graph in 30
seconds."** Tools that require Kubernetes + object storage + an IdP merely to be
*tried* do not get tried.

The ecosystem resolves this with **progressive disclosure**: one binary, several
deployment modes, complexity opt-in. Prometheus runs zero-config (HA is external);
Loki and Mimir ship a **monolithic mode** and a microservices mode in the *same
binary*. Toise's existing ADRs already default to the easy end (ADR 0014/0024:
auth off; ADR 0025: single-tenant `default`). This ADR makes that posture a
**binding invariant** the SaaS work must not erode, and names the current
regressions to fix.

## Decision

1. **Three tiers, one binary.**
   - **Tier 0 — zero-config / try & contribute**: loopback, no auth, local Pebble,
     `default` tenant. The default, and the path for `go run`, examples, and tests.
   - **Tier 1 — self-hosted production**: bearer + TLS, `trust-header` tenancy,
     local disk. A few opt-in flags/env (ADR 0024/0025).
   - **Tier 2 — SaaS / external**: authenticating gateway, OIDC, `derive-only`
     tenancy, optional object-store log, HA, audit (ADR 0028/0029). Composed at the
     edge, every piece opt-in.

2. **The tier-0 path never regresses (invariant).** Every security/resilience
   feature is **additive and off by default**. `go run ./cmd/toise-server` with no
   configuration must always yield a working server on loopback. A change that
   makes the empty-config case fail, or requires an external dependency to start,
   is a defect.

3. **The core builds, runs, and tests with zero external dependencies.** No tier-2
   feature may gate `make build`, `make test`, or a stock `go run`. Object store,
   identity provider, and gateway are optional/pluggable backends behind
   interfaces — never required to develop, try, or test Toise.

4. **Fix the existing adoption regressions** (this ADR's concrete debt):
   - `go install github.com/toise-dev/toise/cmd/toise-server@latest` is **broken**
     by the local `replace ...pkg/emit => ./pkg/emit` in the main `go.mod`; the
     idiomatic Go install must work (drop/avoid the local replace, e.g. by not
     importing the nested module from the server build, or restructuring).
   - The **quickstart leads with `make build`**; lead instead with the
     lowest-friction path (prebuilt binary / `docker run`), keep the
     producer-less demo (`toise-demo`) as the 30-second first graph.
   - Soften the "expect breaking changes between minor releases" messaging once the
     API-stability policy (0.7.0) is the contract.

5. **A CI smoke asserts tier-0.** A test boots the server with no config and
   confirms it serves (the boot-smoke pattern), so a tier-2 feature can never
   silently break the zero-config start.

## Consequences

- The external/SaaS ambition (ADR 0028/0029) becomes **non-toxic to adoption**:
  the funnel keeps its entrance (try/fork/contribute) while gaining its monetizable
  end (external clients).
- Every new feature must **declare its tier and default off** — a small, permanent
  design discipline, and the optional backends must stay behind interfaces.
- Some convenience is traded for this discipline (a feature cannot assume the
  gateway/IdP/object store exists), but it is exactly the trade the ecosystem makes.
- Documentation gains a **"deployment tiers"** page so the climb from laptop to
  SaaS reads as a ladder, not a wall.
