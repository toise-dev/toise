# 24. Native bearer-token auth and TLS (revises ADR 0014)

- Status: Accepted
- Date: 2026-06-08
- Revises: ADR 0014 (no authentication in phase 1)

## Context

ADR 0014 shipped Toise with **no authentication and no TLS**: every surface bound
to loopback and trusted the network. The first real deployment (recette) confirmed
this works only when Toise is fronted by someone else's TLS+auth — the host's OTel
Collector for ingest, an nginx vhost for the read surfaces. That does not scale to
multi-tenant ingestion or to agents running on client sites (#43), where there is
no trusted front door to lean on.

We need auth and TLS that work **out of the box**, without forcing a proxy, while
keeping the simple trusted-network default for local and single-host use.

## Decision

**Add optional bearer-token authentication and optional native TLS to the data
surfaces. Both are off by default — the trusted-network posture of ADR 0014 remains
the default, but a production posture is now available natively.**

1. **Bearer tokens.** A set of accepted static tokens guards the *data* surfaces:
   the HTTP query surfaces (GraphQL, MCP, debug UI, playground) and the OTLP/gRPC
   ingest. A request must carry `Authorization: Bearer <token>` (HTTP header or gRPC
   metadata); tokens are compared in constant time. **No tokens configured ⇒ auth
   disabled.** Tokens are **secrets**: sourced from `TOISE_AUTH_TOKENS` (env), never
   a flag (so they never appear in `ps`); a YAML key exists but env is preferred
   (ADR 0023).
2. **Operational endpoints stay public.** `/healthz`, `/readyz`, and `/metrics` are
   never gated — probes and the metrics scraper must reach them without a secret;
   the network protects them.
3. **Authorization is binary.** A valid token grants full access (read + ingest).
   Fine-grained RBAC is out of scope here; producer *identity* for liveness stays
   keyed on the Resource `service.instance.id` (ADR 0019), orthogonal to who is
   allowed to connect.
4. **Native TLS.** When `tls_cert_file` and `tls_key_file` are both set, the HTTP
   and OTLP/gRPC listeners serve over TLS. Paths are not secrets, so they may come
   from a file/env/flag. **mTLS (client certificates) is deferred** — bearer tokens
   cover the authentication need; the issue's "and/or mTLS" is satisfied by the
   bearer path.

## Consequences

- A deployment can run `toise-server` with `TOISE_AUTH_TOKENS=…` and
  `--tls-cert-file/--tls-key-file` and be production-safe without a fronting proxy;
  agents on client sites can authenticate to a public ingest endpoint.
- Local and single-host use is unchanged: no tokens, no TLS, loopback.
- The auth check is one small `internal/auth` package (an HTTP middleware + gRPC
  interceptors); the receiver gained variadic `grpc.ServerOption`s and the HTTP
  surfaces are wrapped at the mux. No surface-specific auth code.
- ADR 0014's "no auth in phase 1" is **revised**: no-auth is now a *default*, not a
  property of the system. The trusted-network disclaimer in the README/docs stays,
  qualified with "or enable auth+TLS".
- Future work: mTLS, per-token scopes/identities, token rotation/hashing at rest.
