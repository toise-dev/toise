# GraphQL API reference

Toise's GraphQL API is the **typed query surface** over the same read model the MCP
tools and debug UI use: the current-state projection plus the bi-temporal change
log. It is designed to be **introspected** — point any GraphQL client at it and the
schema is self-describing. This page is the human-readable companion; the
authoritative schema is [`internal/graphql/schema.graphql`](../../internal/graphql/schema.graphql)
and the design rationale is [ADR 0010](../architecture/adr/0010-graphql-as-primary-query-language.md).

## Endpoint and transports

| | |
| --- | --- |
| HTTP endpoint | `POST` (and `GET`) `http://<listen>/graphql` — default `127.0.0.1:8080` |
| Subscriptions | WebSocket (`graphql-ws`) on the same `/graphql` path |
| Playground | `http://<listen>/playground` (interactive, introspection-backed) |
| Introspection | **enabled** |
| Auth | **none in phase 1** (ADR 0014) — keep `listen` on loopback / a trusted network |

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

### Subscriptions

| Subscription | Stream |
| --- | --- |
| `entityChanged` | `ChangeEvent!` as entity changes are classified |
| `relationChanged` | `ChangeEvent!` as relation changes are classified |

There are **no mutations**: Toise is a read model. State enters only through the
OTLP ingestion boundary (see [the OTel mapping](../data-model/otel-mapping.md)).

### Core types (abridged)

```graphql
type Entity {
  id: ID!              # stable logical id (ULID), survives identity changes
  type: String!        # e.g. "host", "process", "network.interface"
  identity: [Attribute!]!
  attributes: [Attribute!]!
  schemaUrl: String!
  deleted: Boolean!    # soft-deleted (history retained)
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

type Attribute { key: String!, value: String!, type: ValueType! }  # ValueType: STRING|INT|DOUBLE|BOOL
```

`ChangeType` is the Toise change taxonomy: `ENTITY_CREATED`, `ENTITY_DELETED`,
`ENTITY_IDENTITY_CHANGED`, `ENTITY_ATTRIBUTE_UPDATED`, `ENTITY_STATE_CHANGED`,
`ENTITY_UNCHANGED`, `RELATION_ADDED`, `RELATION_REMOVED`,
`RELATION_ATTRIBUTE_CHANGED`.

> **Attribute values are stringly-typed on the wire.** Every `Attribute.value` is a
> string; read `Attribute.type` to interpret it (`"8"` with `type: INT` is the
> integer 8). This keeps the heterogeneous attribute map representable in GraphQL.

## Pagination (Relay-style)

All list queries use Relay cursor pagination. Pass `first` (page size) and `after`
(an opaque cursor); read `pageInfo` to continue:

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

`totalCount` is the count across all pages for the given filter. Default page sizes:
`50` for `entities`/`relations`, `100` for `entityHistory`/`recentChanges`.

## Bi-temporality — `eventTime` vs `recordedAt`

Every `ChangeEvent` carries two times (ADR 0005):

- **`eventTime`** — when the fact became true in the real world (from the producer).
- **`recordedAt`** — when Toise recorded it. The two differ for late or retroactive
  events.

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

## Guardrails and limits

The server is hardened against expensive or hostile queries (ADR 0010):

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

- [MCP tools](../architecture/adr/0011-mcp-server-design.md) — the LLM-facing surface
  over the same read model (`find_entities`, `get_entity`, `get_neighbors`,
  `entity_history`, `recent_changes`, `describe_schema`).
- [OTel entity-events mapping](../data-model/otel-mapping.md) — how state enters Toise.
- [Bi-temporal event model (ADR 0005)](../architecture/adr/0005-bi-temporal-event-model.md).
