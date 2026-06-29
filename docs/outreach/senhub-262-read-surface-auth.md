# Note to Toise consumers — read-surface auth (0.9.0, #262)

This note covers a 0.9.0 access-security change. It is **backward compatible**;
read it to confirm whether anything on your side needs to change.

## TL;DR

- **Producers (OTLP ingest): nothing changes.** How you emit entity events is
  unaffected. If a deployment uses mutual TLS for ingest, it can now be
  configured so ingest needs **only** the client certificate and **no bearer
  token** — but that is the operator's deployment choice, not a contract change.
  Your `service.instance.id` liveness keying is untouched.
- **Read consumers (GraphQL / MCP): may need a scoped token.** If you read the
  graph over GraphQL or MCP, a hardened deployment now expects a **per-client
  scoped token** (role `read` or `full`, individually revocable) instead of a
  single shared password. Existing tokens keep working; the shared password
  remains a transitional fallback.

## What actually changed

Previously, Toise's "auth enabled" decision was global: turning on tokens to
protect the read surfaces also forced a bearer token onto the ingest surface,
even when mutual TLS already authenticated the producer. 0.9.0 **decouples** the
two surfaces:

- **Ingest** can be authenticated by **mutual TLS alone** (opt-in
  `ingest_mtls_only`); no bearer is required or consulted there.
- **Read surfaces** keep requiring their per-client scoped tokens (or OIDC).

## Do I need to do anything?

- You only **produce** (emit OTLP)? **No action.** Keep emitting as today.
- You also **read** via GraphQL/MCP against a token-protected deployment? Ask the
  operator for a **scoped read token** for your client and send it as
  `Authorization: Bearer <token>` on the HTTP read surfaces. Each client gets its
  own token with its own role, revocable without affecting anyone else.

## Compatibility

Additive and opt-in end to end: deployments that do nothing keep their current
behavior; the shared-password path still works during transition. No producer
field, identity, or liveness semantics change.

References: ADR 0028 (0.9.0 amendment), `docs/operations/configuration.md`
("Decoupling ingest auth from read auth").
