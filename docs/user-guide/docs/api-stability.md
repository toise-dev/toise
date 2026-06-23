# API stability

Toise has three public contracts. This page states what is stable, how changes
are made, and what is explicitly not covered — so an integration knows what it
can build on.

!!! note "Pre-1.0"
    Before 1.0, a minor release *may* make a breaking change to a stable surface,
    but only with a deprecation notice in the preceding release and an entry in
    the changelog. After 1.0 the surfaces below follow semantic versioning.

## Stable surfaces

| Surface | What is covered | Pinned by |
|---------|-----------------|-----------|
| **Producer wire contract** (OTLP entity events) | `entity.state` / `entity.delete` records, the `entity.*` attributes, embedded `entity.relationships`, identity rules | byte-exact conformance fixture (`pkg/emit/conformance`, `fixture_v1.bin`) |
| **MCP surface** | the set of tools (name + input/output fields), resources and resource templates (name, URI, MIME), and prompts (name, arguments) | a golden contract test (`internal/mcp`, `tool_contract.golden`) — any change fails the build until the golden is deliberately regenerated |
| **GraphQL schema** | types, fields and arguments in `schema.graphql` | the schema is the hand-maintained source of truth; changes are reviewed in the PR diff |

**Change rules for stable surfaces:**

- **Additive only** within a release series: new tools, new optional fields, new
  entity/relation types are fine and do not break clients.
- **Deprecate before removing:** a field/tool to be removed is first marked
  deprecated (GraphQL `@deprecated`, a note in the tool/field description) for at
  least one release.
- **No silent retyping or renaming** of an existing field — that is a breaking
  change and follows the deprecation path.

## Following the upstream entity-events spec

The producer wire contract follows the OpenTelemetry **entity-events**
specification, which is still at **Development** status upstream. Toise's
stability guarantee is its **own** — the byte-exact conformance fixture plus the
change rules above — not a mirror of the upstream lifecycle label. In practice:

- **Compatible upstream additions** (new optional fields, new entity/relation
  types) are adopted additively within the current series, never breaking existing
  producers.
- A **breaking upstream change** (a renamed key, a changed identification
  mechanism) is absorbed through the deprecation path: Toise accepts both the old
  and the new form for at least one release (dual-read) with a changelog notice,
  and only becomes a Toise major bump if coexistence is genuinely impossible. The
  conformance fixture is the tripwire — any wire drift fails the build until it is
  deliberately addressed.

This is the posture ratified in
[ADR 0031](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0031-one-zero-stability-decoupled-from-upstream-spec.md):
we do not gate Toise's stability on an externally-controlled "Stable" date.

## Not covered (may change anytime)

- The **debug UI** (`/`), the GraphQL **playground** and **introspection** — development aids, off under `--production`.
- **Internal Go packages** (`internal/...`) — not an importable API. The producer SDK `pkg/emit` is the supported Go surface.
- **Prometheus metric** names and labels — best-effort stability; dashboards should tolerate additions.
- **Log line** wording and the on-disk store format (a format change is gated by the store's `format_version` marker, not by this policy).
- The **operator-annotations sidecar** on-disk format. Annotations (`annotate_entity` / the `annotateEntity` mutation) are an overlay a human or assistant attaches to an entity — never producer truth, never part of the event log, and not replayed. The *tool and mutation shapes* are covered above; the sidecar storage is not.

## The open vocabulary

Producers may emit entity and relation **types beyond the built-in registry**
when the server runs with `--accept-unknown-types`. Those types are part of *your*
contract with your own consumers, not Toise's — Toise stores and serves them
faithfully but makes no stability promise about types it does not define.
