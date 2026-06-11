# 27. SDK packaging: nested module, v-prefixed tags, one wire vocabulary

- Status: Accepted
- Date: 2026-06-11

## Context

0.5.0 shipped `pkg/emit` as "the first public Go package", but the 2026-06-11
audit (#160) found the packaging contradicts that claim on three counts:

1. **No tagged release is installable as a Go module version.** The repository
   deliberately tags releases without a `v` prefix (`0.1.0`–`0.5.0`), and Go
   module tooling only accepts `vX.Y.Z` tags as versions:
   `go get github.com/toise-dev/toise/pkg/emit@0.5.0` cannot resolve, so every
   consumer lands on a pseudo-version off `main` — no reproducible builds
   against a release, effectively an unreleased SDK.
2. **Module coupling.** The SDK lived inside the root module, so any producer
   importing it inherited the entire server stack — pebble, gqlgen,
   gorilla/websocket, prometheus, the MCP SDK — into its module graph and
   `go.sum`, and dependency scanners flagged the lot.
3. **The wire vocabulary was spelled three times** (`pkg/emit`,
   `pkg/emit/conformance`, `internal/ingest`). The frozen fixture pins most
   keys, but two had no effective tie and would drift silently with all tests
   green: `entity.report.interval` (drift disarms the liveness backstop for
   every producer) and `service.instance.id` (drift collapses per-producer
   reference counting, ADR 0019).

These are decisions, not bugs: every option becomes a breaking change once the
SDK has adopters, so they had to be settled before the first SDK release.

## Decision

**1. `pkg/emit` becomes its own Go module** (`github.com/toise-dev/toise/pkg/emit`),
containing the SDK, the conformance kit, the new `wire` package, and the frozen
testdata. Its dependency graph is the OTel pdata types and gRPC — nothing of the
server. The root module consumes it through a `replace` directive pointing at
the in-tree copy, so the server always builds against the SDK as it exists in
the same commit, and the cross-module parity tests in `internal/ingest` keep
pinning SDK-vs-ingest. The import path does not change for adopters.

**2. Server tags gain the `v` prefix from `v0.6.0`; the SDK is tagged
`pkg/emit/vX.Y.Z` from `pkg/emit/v0.1.0`, on its own cadence.** The `v` prefix
is not a style choice — it is what makes a tag a Go module version, and the SDK
is now the thing being versioned for consumers. Go resolves the nested module
path automatically: once `pkg/emit/v0.1.0` exists,
`go get github.com/toise-dev/toise/pkg/emit@v0.1.0` installs that exact
version. **`0.1.0`–`0.5.0` are not retro-tagged** with `v` twins: a tag push
re-triggers `release.yml`, which would mint duplicate releases and duplicate
GHCR images for artifacts that already exist.

**3. `pkg/emit/wire` is the single in-repo spelling of the entity-events wire
vocabulary** — event names, attribute keys, relationship-descriptor keys, and
the producer-identity resource attribute — imported by the SDK, the conformance
kit, and `internal/ingest`. It is stdlib-only, so importing the vocabulary never
pulls a protocol stack into a producer. The frozen fixtures stay: shared
constants pin code-vs-code (the repo can no longer disagree with itself), the
fixture pins contract-vs-world (the repo can no longer drift from the published
bytes while agreeing with itself). The two previously untied constants
additionally get end-to-end behavioral pins (an SDK-set interval arms the
sweep; two producers hold independent references).

**4. Release tooling follows the tags.** `release.yml` triggers on `v*.*.*`;
GitHub's `*` glob does not cross `/`, so SDK tags never trigger a server
release. Asset and image names carry the ref name verbatim
(`toise_v0.6.0_linux_amd64.tar.gz`, GHCR tag `v0.6.0`), matching what
`git describe` stamps into the binary. `deploy-docs.yml` triggers on the
v-prefixed patterns and strips the `v` for the published docs version, so docs
URLs stay `/docs/0.6.0` style, consistent with the existing `0.2.0`–`0.5.0`
entries.

## Consequences

- The SDK is releasable: `pkg/emit/v0.1.0` makes
  `go get …/pkg/emit@v0.1.0` work, with reproducible builds against a tag.
  Until that tag is cut, consumers use pseudo-versions off `main` (documented
  as such).
- A producer importing the SDK gets a module graph of pdata + grpc, nothing
  else; the server's storage and query dependencies stop leaking into producer
  builds and their security scanners.
- SDK and server release cadences are decoupled: a wire-contract addition can
  ship as `pkg/emit/v0.2.0` without a server release, and vice versa.
- `./...` from the repository root no longer reaches `pkg/emit`: `make test`,
  `make lint`, `make tidy`, CI, and lint each run the nested module explicitly.
  Forgetting the sub-invocation in a future target would silently skip the SDK
  — the Makefile and workflows carry a comment to that effect.
- The first server release under this scheme is `v0.6.0`; version-sorting
  tooling must treat `0.5.0 < v0.6.0` by stripping the prefix. The docs
  version sequence on toise.dev is unaffected (the `v` never reaches mike).
- Producers and Toise can still drift from the *published* contract only
  together — that remains the fixtures' job; `wire` removes the ability to
  drift apart one literal at a time.
