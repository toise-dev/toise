# 14. No authentication in phase 1

- Status: Revised by [ADR 0024](./0024-native-auth-and-tls.md) — no-auth is now a
  configurable *default*, not a property of the system (native bearer auth + TLS
  are available).
- Date: 2026-05-29

## Context

Toise phase 1 exposes a GraphQL API, an MCP server, and a debug UI, all without
any authentication mechanism.

## Decision

We intentionally do not implement authentication in phase 1. Toise is to be
deployed only in trusted network environments (private datacenter networks,
VPN-protected segments). Operators are responsible for network-level isolation.

Rationale:

- A half-baked authentication (e.g. a shared bearer token) would give a false
  sense of security and may discourage proper network isolation.
- Proper authentication (mTLS, OAuth2/OIDC, fine-grained authorization) is a
  later-phase deliverable and will be designed thoroughly when production
  deployment approaches.
- Phase 1 is for validating the concept with controlled internal testing, not
  for exposure to untrusted networks.

## Consequences

- Documentation must clearly state the network-trust assumption. The README
  carries a phase 1 security disclaimer.
- The `--listen` flag defaults to `127.0.0.1`, not `0.0.0.0`, forcing operators
  to make an explicit choice to expose the service.
- A later phase will revisit this decision with a proper authentication and
  authorization design, superseding this ADR.
