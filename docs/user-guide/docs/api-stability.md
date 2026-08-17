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

The golden pins **shape, not prose**: field *descriptions* are deliberately outside it, so the wording an assistant reads can be improved after 1.0 without a contract break. Renaming or retyping a field cannot.
| **GraphQL schema** | types, fields and arguments in `schema.graphql` | the schema is the hand-maintained source of truth; changes are reviewed in the PR diff |

**Change rules for stable surfaces:**

- **Additive only** within a release series: new tools, new optional fields, new
  entity/relation types are fine and do not break clients.
- **Deprecate before removing:** a field/tool to be removed is first marked
  deprecated (GraphQL `@deprecated`, a note in the tool/field description) for at
  least one release.
- **No silent retyping or renaming** of an existing field — that is a breaking
  change and follows the deprecation path.

## Who owns what

A producer and Toise each hold one half of the contract, and the split follows a
single rule: **each side owns what it can verify.**

| Belongs to Toise | Belongs to the producer |
|------------------|-------------------------|
| The **vocabulary** — entity and relation types, attribute keys, identity rules | The **transport** — batching, backpressure, retries, tenant propagation |
| The **wire form** — the record shape, pinned by the conformance fixture | **Verifying its own emission** matches that form |

Toise holds the vocabulary because it is the only side that sees every producer
at once, and it publishes it in `pkg/emit/wire` — stdlib-only, so importing the
vocabulary never drags a protocol stack into a producer's module graph.

A producer holds the transport because that is where its operational guarantees
live. A producer whose own export pipeline already owns batching, backpressure,
retries and tenant headers should **not** adopt the SDK's client at runtime:
routing entity events through a second path would cost it those four properties
to gain nothing. Importing `pkg/emit/wire` for the vocabulary, or the SDK's
encoder in a **differential test** that fails the build when its own encoding
drifts from ours, gets the guarantee without the coupling.

The corollary matters for anyone pinning to `pkg/emit`: it is the supported Go
surface, so it follows the change rules above — additive within a series,
deprecate before removing — and, as the pre-1.0 note says, a break remains
possible before 1.0 but only announced in the preceding release and recorded in
the changelog.

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

## Two conventions the surfaces will keep

Both are consistently applied today and are stated here rather than per field, so
a consumer learns them once. Both are also **frozen at 1.0**, since changing
either is a breaking change for clients that assume the current shape.

**Absence is an empty string, not null.** Twenty `String!` fields in the GraphQL
schema can legitimately be empty — `Entity.schemaUrl`, `SameAsLink.basis`, a
`CanonicalMember`'s `type` and `label` when the alias is deleted. They are
non-nullable and empty rather than nullable, so a client never has to branch on
null for a scalar it asked for. Optional *objects* are still nullable
(`Entity.annotations`, the `canonical` query) — the convention covers scalars.

**`count` is scoped to the object carrying it, `total` is the population before
the limit.** On a paginated MCP result, `count` is what was returned and `total`
is what matched; inside a nested object — a relation participation, an endpoint
shape, a type breakdown — `count` is how many of *that* thing were observed. Every
occurrence carries its own description in the tool schema; the rule above is why
they differ.

## What parity between MCP and GraphQL means

The two read surfaces are **not** meant to be feature-equal, and the difference
is deliberate rather than a backlog:

- **GraphQL is the typed query surface** over the read model — entities,
  relations, history, subscriptions. A client picks its own fields.
- **MCP adds analysis**: traversal and interpretation an assistant would
  otherwise have to reimplement from many round trips — `impact_of`, `find_path`,
  `graph_diff`, `describe_type`, `telemetry_keys`.

What *is* promised is narrower and more useful: **where both surfaces answer the
same question, they answer it identically** — same walk, same thresholds, same
defaults. The `same_as` canonical overlay is computed once and called from both;
`recentChanges` and `recent_changes` take the same window default. A divergence
there is a bug, not a design choice.

The corollary, stated so it is not assumed away at 1.0: **a new MCP tool does not
oblige a matching GraphQL query**, and the reverse holds too.

## Deprecations in flight

| Deprecated | Replacement | Removal |
|------------|-------------|---------|
| `routes_via` relation type | `network.route` + `has_route` + `next_hop_via` | 1.0 |
| `forwards_to` relation type | `connected_to` to the learned port | 1.0 |
| `adjacent_to` relation type | port-to-port `connected_to` | 1.0 |

All three were superseded under topology-as-entities (ADR 0022) and have been
documented as "do not emit" since. They are **still accepted** and still listed by
`wire.RelationTypes()`, because that list states what the boundary accepts rather
than what a producer should emit. No known producer emits any of them.

`ENTITY_IDENTITY_CHANGED` is a different case and is **not** deprecated: nothing
emits it — under exact identity matching an identity change is a different entity
— but the value is retained to replay logs written before that rule, so it cannot
be removed. Do not write a handler waiting for it.

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
