# 3. Keeping dependencies current

- Status: Accepted
- Date: 2026-05-29

## Context

Toise depends on a small set of third-party Go libraries (Pebble, gqlgen, the
OpenTelemetry collector/SDK types, the MCP Go SDK). Dependency rot — stale
versions accumulating until a painful big-bang upgrade — is a common failure
mode. Security fixes in dependencies must also reach us promptly. We want
currency to be a routine part of feature work, not a deferred chore.

## Decision

Dependencies are kept current as part of milestone work, under the following
rules.

- **Per milestone:** at the start of a milestone, run `go get -u ./...` and
  `go mod tidy` to bring dependencies to their latest minor/patch versions, and
  verify the build and tests still pass.
- **Major version bumps** are evaluated explicitly: read the changelog, check
  for breaking changes, decide deliberately. If bumped, the bump is a dedicated
  commit separate from feature code.
- **Breakage is investigated, never silently rolled back.** If a dependency
  update breaks tests, fix the breakage and document it in the commit message
  or an ADR. If the fix is too costly to do immediately, pin the previous
  version and open a GitHub issue describing the follow-up.
- **Per milestone checkpoint:** run `govulncheck ./...` and resolve any reported
  vulnerabilities before requesting validation. A security-relevant
  vulnerability in a dependency takes priority over feature work.
- **Go toolchain:** track the latest stable Go release (see the project brief).
  The `go` directive in `go.mod` tracks the latest minor; CI pulls the latest
  patch via `check-latest: true`.

## Consequences

- Upgrades stay small and frequent, avoiding large risky jumps.
- Security fixes are picked up quickly and gated by `govulncheck` at every
  checkpoint.
- A small, predictable amount of per-milestone overhead is accepted in exchange.
- The OTel libraries are an exception worth noting: because the entity-events
  spec is still evolving, those specific dependencies are pinned and bumped
  deliberately with an ADR addendum (see ADR 0015).
