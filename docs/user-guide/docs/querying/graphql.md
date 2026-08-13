# GraphQL API

Toise's GraphQL API is the **typed query surface** over the same read model the
MCP tools and debug UI use: the current-state projection plus the bi-temporal
change log. It is designed to be **introspected** — point any GraphQL client at
it and the schema is self-describing. This page is the human-readable companion;
the authoritative schema is
[`internal/graphql/schema.graphql`](https://github.com/toise-dev/toise/blob/main/internal/graphql/schema.graphql)
and the design rationale is
[ADR 0010](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0010-graphql-as-primary-query-language.md).

## Endpoint and transports

| | |
| --- | --- |
| HTTP endpoint | `POST` (and `GET`) `http://<listen>/graphql` — default `127.0.0.1:8080` |
| Subscriptions | WebSocket (`graphql-ws`) on the same `/graphql` path |
| Playground | `http://<listen>/playground` (interactive, introspection-backed) |
| Introspection | **enabled** |
| Auth | **off by default** — optional bearer token (`Authorization: Bearer <token>`); keep `listen` on loopback / a trusted network otherwise |

A first query with `curl`:

```sh
curl -s http://127.0.0.1:8080/graphql \
  -H 'content-type: application/json' \
  -d '{"query":"{ entities(first: 5){ totalCount edges{ node{ id type } } } }"}'
```

## Schema overview

### Queries

| Query | Returns | Purpose |
| --- | --- | --- |
| `entity(id: ID!)` | `Entity` | one entity by its logical id (null if unknown) |
| `entities(filter, first = 50, after)` | `EntityConnection!` | current entities, newest-first, paginated |
| `relations(filter, first = 50, after)` | `RelationConnection!` | current relations, paginated |
| `entityHistory(id!, since, until, asKnownAt, first = 100, after)` | `ChangeConnection!` | one entity's change timeline (bi-temporal) |
| `recentChanges(window!, first = 100, after)` | `ChangeConnection!` | changes across all entities within a window |
| `canonical(id!, asOf)` | `CanonicalGroup` | what is believed to be the same real thing as this entity (null if nothing is) |

### Subscriptions

| Subscription | Stream |
| --- | --- |
| `entityChanged` | `ChangeEvent!` as entity changes are classified |
| `relationChanged` | `ChangeEvent!` as relation changes are classified |

### Mutations

| Mutation | Returns | Purpose |
| --- | --- | --- |
| `annotateEntity(id!, annotations: [AnnotationInput!]!)` | `Annotation!` | merge operator notes onto an entity (an empty value removes a key) |

Toise stays a **read model for producer truth**: graph state enters only through
the OTLP ingestion boundary (see [Ingesting data](../ingestion.md)). The sole
mutation, `annotateEntity`, writes an *overlay* — out-of-band operator notes kept
in a per-tenant sidecar, never mixed into the event log or replay. It requires a
write-capable bearer token (full or tenant-scoped); a read-only token is refused.
Annotations surface on `Entity.annotations`.

### Core types (abridged)

```graphql
type Entity {
  id: ID!              # stable logical id (ULID), survives identity changes
  type: String!        # e.g. "host", "process", "network.interface"
  identity: [Attribute!]!
  attributes: [Attribute!]!
  schemaUrl: String!
  deleted: Boolean!    # soft-deleted (history retained)
  annotations: Annotation  # operator overlay, null if none
}

type Annotation {        # operator notes — an overlay, NOT producer truth
  values: [AnnotationEntry!]!   # entries sorted by key
  author: String
  updatedAt: String      # RFC 3339
}

type Relation {
  id: ID!
  type: String!        # e.g. "runs_on", "has_interface", "connected_to"
  fromId: ID!
  toId: ID!
  attributes: [Attribute!]!
  structural: Boolean! # add/remove is significant (alertable)
}

type ChangeEvent {
  id: ID!
  changeType: ChangeType!   # see the change taxonomy below
  eventTime: String!        # when it became true in reality (RFC 3339)
  recordedAt: String!       # when Toise recorded it (RFC 3339)
  changedKeys: [String!]!
  entity: Entity            # set for entity events
  relation: Relation        # set for relation events
}

type CanonicalGroup {    # read-time identity overlay — derived, never stored
  aliases: [CanonicalMember!]!  # sorted by id, never includes the entity queried
  links: [SameAsLink!]!         # the same_as edges that justify the group
}

type CanonicalMember { id: ID!, type: String!, label: String! }
type SameAsLink { from: ID!, to: ID!, confidence: Float!, basis: String! }

type Attribute { key: String!, value: String!, type: ValueType! }  # ValueType: STRING|INT|DOUBLE|BOOL
```

`ChangeType` is the Toise change taxonomy: `ENTITY_CREATED`, `ENTITY_DELETED`,
`ENTITY_IDENTITY_CHANGED`, `ENTITY_ATTRIBUTE_UPDATED`, `ENTITY_STATE_CHANGED`,
`ENTITY_UNCHANGED`, `RELATION_ADDED`, `RELATION_REMOVED`,
`RELATION_ATTRIBUTE_CHANGED`.

!!! note "Attribute values are stringly-typed on the wire"
    Every `Attribute.value` is a string; read `Attribute.type` to interpret it
    (`"8"` with `type: INT` is the integer 8). This keeps the heterogeneous
    attribute map representable in GraphQL.

## Pagination (Relay-style)

All list queries use Relay cursor pagination. Pass `first` (page size) and
`after` (an opaque cursor); read `pageInfo` to continue:

```graphql
query FirstPage {
  entities(filter: { type: "host" }, first: 50) {
    totalCount
    edges { cursor node { id type } }
    pageInfo { hasNextPage endCursor }
  }
}
```

Fetch the next page by passing the previous `pageInfo.endCursor` as `after`:

```graphql
query NextPage($cursor: String!) {
  entities(filter: { type: "host" }, first: 50, after: $cursor) {
    edges { node { id type } }
    pageInfo { hasNextPage endCursor }
  }
}
```

`totalCount` is the count across all pages for the given filter. Default page
sizes: `50` for `entities`/`relations`, `100` for
`entityHistory`/`recentChanges`.

## Bi-temporality — `eventTime` vs `recordedAt`

Every `ChangeEvent` carries two times:

- **`eventTime`** — when the fact became true in the real world (from the
  producer).
- **`recordedAt`** — when Toise recorded it. The two differ for late or
  retroactive events.

Query history in **eventTime space** (reality) by default; switch to the **audit
view** ("what did Toise know at instant T?") by passing `asKnownAt`:

```graphql
# Reality: the entity's timeline between two real-world instants.
query Timeline {
  entityHistory(id: "01JABC...", since: "2026-06-01T00:00:00Z", until: "2026-06-02T00:00:00Z", first: 100) {
    edges { node { changeType eventTime recordedAt changedKeys } }
    pageInfo { hasNextPage endCursor }
  }
}

# Audit: only events Toise had already recorded by 2026-06-01T12:00:00Z.
query AsKnownAt {
  entityHistory(id: "01JABC...", asKnownAt: "2026-06-01T12:00:00Z") {
    edges { node { changeType eventTime recordedAt } }
  }
}
```

## Example queries

**An entity and its incident relations** (Toise has no `Entity.relations` field;
query both directions via `relations`):

```graphql
query EntityWithEdges($id: ID!) {
  entity(id: $id) { id type attributes { key value type } }
  outgoing: relations(filter: { fromId: $id }) { edges { node { type toId structural } } }
  incoming: relations(filter: { toId: $id })   { edges { node { type fromId structural } } }
}
```

**Recent structural churn across the fleet:**

```graphql
query Recent {
  recentChanges(window: "24h", first: 50) {
    edges {
      node {
        changeType eventTime
        entity { type identity { key value } }
        relation { type fromId toId }
      }
    }
  }
}
```

`window` is a Go duration string (`"15m"`, `"2h"`, `"24h"`).

**Subscribe to live changes** (WebSocket, `graphql-ws` subprotocol, on
`ws://127.0.0.1:8080/graphql`):

```graphql
subscription {
  entityChanged { changeType eventTime entity { id type } }
}
```

## Aliases — when one machine is observed twice

Two producers can describe the same real machine from different vantage points:
a hypervisor reports a `compute.vm`, an in-guest agent reports a `host`. Neither
knows the other's identifier, so Toise stores **two entities** — and it keeps
storing two, because merging them would destroy the ability to say which
producer saw what.

When a producer *can* justify the link, it asserts it as a `same_as` edge
carrying a `confidence` and a `basis` (the evidence — `hyperv-kvp`,
`serial_match`). That edge is producer truth, stored like any other. What Toise
derives at read time is the grouping:

```graphql
query WhoElseIsThis($id: ID!) {
  entity(id: $id) { type identity { key value } }
  canonical(id: $id) {
    aliases { id type label }
    links { from to confidence basis }
  }
}
```

`canonical` is null when nothing qualifies — an entity that is only itself has
no group. Otherwise `aliases` holds every entity reachable over `same_as` edges
at or above the server's `identity_confidence_threshold` (default `0.9`),
**transitively**: if A is B and B is C, then C is in A's group even though no
producer asserted A = C. `links` shows the edges that justify it, so a consumer
can always see *why* two things were grouped and decide it disagrees.

Below-threshold evidence stays in the graph — query it with
`relations(filter: { type: "same_as" })` — but collapses nothing. A wrong merge
answers confidently about the wrong machine, which is worse than a visible gap.

Two properties worth knowing:

- **The group is symmetric.** `same_as` is stored as a directed edge, but asking
  from either end returns the same group.
- **`asOf` applies.** A group is derived from edges, and edges change; reading
  `canonical(id: $id, asOf: "…")` gives the belief as it stood then, not today's
  belief projected backwards.

The MCP surface exposes the same overlay on `get_entity`, computed by the same
walk against the same threshold, so both surfaces answer identically. See
[ADR 0020](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0020-weighted-multi-source-identity.md).

### Bridging a re-key — and why the edge then reads as absent

A producer changing how it spells an entity's identity can emit a `same_as`
between the old identity and the new one, so the two timelines stay joinable
without anything being merged. It is the honest way to survive a re-key: history
is never rewritten, and a dated read at the cutover shows both entities *and* the
link between them.

!!! warning "The bridging edge disappears from the current view, by design"
    Once the old entity is gone, the cascade removes every edge touching it —
    including the bridge. So after the cutover,
    `relations(filter: { type: "same_as" })` returns **nothing**, and
    `canonical` on the surviving entity returns null.

    That is correct behaviour, not a lost edge: an edge to a deleted entity is
    meaningless in the present. But someone verifying a migration with the
    obvious query will read zero and conclude nothing was ever emitted. **Verify
    with a dated read at the cutover instant instead:**

    ```graphql
    query BridgeAtCutover($t: String!) {
      relations(filter: { type: "same_as" }, asOf: $t) {
        edges { node { fromId toId attributes { key value } } }
      }
    }
    ```

## Guardrails and limits

The server is hardened against expensive or hostile queries:

| Guard | Default | Behaviour |
| --- | --- | --- |
| Query complexity | `1000` | requests above the analyzed-complexity cap are rejected |
| Per-request timeout | `10s` | `POST`/`GET` exceeding it get HTTP `503` with a plain-language message (subscriptions are exempt — they are long-lived) |
| WebSocket origin | same-origin | browser cross-origin upgrades are refused unless the Origin is allow-listed; non-browser clients (no `Origin`) are allowed |

The timeout message is deliberately actionable:

```json
{"errors":[{"message":"query timed out: narrow your selection, lower first:, or split it into smaller queries"}]}
```

To stay within limits: request only the fields you need, keep `first` modest and
page with `after`, and split very large traversals into successive queries.

## See also

- [MCP for AI assistants](mcp.md) — the LLM-facing surface over the same read
  model.
- [Ingesting data](../ingestion.md) — how state enters Toise.
- [Data model](../data-model.md) — entities, relations, and the change taxonomy.
