---
title: "What can you do with OpenTelemetry entity events?"
linkTitle: "Doing something with entity events"
date: 2026-06-15
author: "[Matthieu Noirbusson](https://github.com/<your-handle>)"
# cSpell:ignore Noirbusson Toise Pebble gqlgen bitemporal semconv kvlist
---

Metrics, logs, and traces tell you how your systems *behave*. They are much
quieter about what actually *exists*: which hosts, interfaces, switches,
services, and volumes are out there right now, how they connect, and — crucially
— how that picture changed over the last hour, day, or quarter. That living
inventory and topology has stayed a blind spot in the open observability stack.

OpenTelemetry's **entity events**, coming out of the Entities SIG and described
in the [Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/),
are the piece that starts to close it. But entity events are a *stream*. The
interesting question isn't only "how do I emit them?" — it's **"what do I do once
they arrive?"** This post walks through one answer, using an open-source consumer
as a worked example.

> **Disclosure:** I work on [Toise](https://toise.dev), an Apache-2.0 project used
> below as a concrete example. Everything here is about the general shape of
> consuming entity events; the lessons apply to any consumer. The entity data
> model and its conventions are **still in development** (not yet stable) and
> evolving — treat the exact attribute names below as illustrative and check them
> against the current spec.

## A 60-second primer on entity events

An entity is a thing worth tracking on its own: a host, a process, a network
interface, a database. OpenTelemetry carries *entity events* as **OTLP log
records** annotated with the entity semantic conventions. Each event carries the
entity's type, its identifying attributes, its descriptive attributes, and an
event type describing its lifecycle. A consumer can classify a record purely by
the presence of `otel.entity.event.type`:

```
# An entity-event log record (illustrative)
LogRecord
  Timestamp: 2026-05-26T08:00:00Z              # the producer-side time
  attributes:
    otel.entity.event.type: entity_state       # observed; or entity_delete
    otel.entity.type:       host
    otel.entity.id:         { host.name: web-server-1 }            # identity (a map)
    otel.entity.attributes: { os.type: linux, host.arch: amd64 }   # descriptive (a map)
```

Producers emit these — a host agent, a network agent, anything that speaks OTLP.
The consumer's job is to turn a sequence of such observations into something you
can *ask questions of*.

## Step 1 — Don't store state, store the stream

The instinct is to keep a table of "current entities" and update rows in place.
That throws away exactly what makes infrastructure hard: **time**. The moment you
overwrite `web-server-1`'s address, you lose the fact that it changed and when.

A better default is **event sourcing**: append every entity event to a durable,
ordered log and treat that log as the system of record. The current graph is then
a *projection* — replay the log into an in-memory model of entities and
relationships. Toise does this with an append-only log on Pebble and an in-memory
projection; rebuilding the whole graph from scratch is just a replay.

The payoff: current state is one read away, and **history is never lost**.

## Step 2 — Be bi-temporal on purpose

Two timestamps matter, and they are not the same:

- **Event time** — when something happened in reality. Take it from the
  `LogRecord` timestamp.
- **Recorded time** — when *you* learned about it. Stamp it yourself at ingest;
  never take it from the producer.

Keeping both lets you answer two genuinely different questions:

- *Reality view:* "How was `db-07` wired last Tuesday?"
- *Audit view:* "What did we **know** about `db-07` at 09:00 — not what we know
  now?"

That second question is the one that matters during an incident review, and you
can only answer it if you never collapsed the two timelines. Designing for
bi-temporality from day one is far cheaper than retrofitting it.

## Step 3 — Give entities an immutable identity

OpenTelemetry treats an entity's **Id as immutable**, and that turns out to be the
right discipline for a graph that wants to be a source of truth. Match identity
**exactly**: an observation is either a known entity (same Id) or a different one.

The trap is putting a volatile value in the Id with nothing to anchor it: fold a
leased IP into a host's identity and a DHCP renewal forks it in two. Identify by
something **immutable** and push everything that legitimately changes — current
address, last-seen state, resource usage — into *descriptive* attributes, so a
re-address is an attribute update on the *same* entity, not a silent fork. A reused
value can still anchor an identity when it is paired with a discriminator stable for
the resource's lifetime: the OpenTelemetry `process` entity is keyed by
`process.pid` **+** `process.creation.time`, so a real restart — a new pid *and* a
new creation time — is correctly a *new* process, while a config reload at the same
identity is an attribute update. A genuine identity change is a *new* entity, never
a silent merge of two different things.

Toise learned this the hard way. It first tried *tolerant* matching — treat an
observation differing by a single identifying value as the same entity that
"changed identity." It quietly merged distinct entities: two databases on a host
differing only by port collapsed into one. Exact matching — plus a stable internal
id and a content hash for idempotent lookup — is both safer and closer to the OTel
model. For a source of truth, a silent collision is a worse failure than a lost
heuristic.

## Step 4 — Make it queryable, including by an LLM

A temporal graph is only useful if people (and machines) can ask it things.
Exposing it twice covers both audiences:

- A **GraphQL API** for humans, dashboards, and tools (`entity`, `entities`,
  `relations`, `entityHistory`, `recentChanges`, plus subscriptions).
- A **Model Context Protocol (MCP) server** so an AI assistant can query the graph
  on an operator's behalf.

The MCP angle is where entity events get genuinely fun. Because every
type/field/argument carries a rich description, an LLM can *introspect* the schema
and call typed tools — `find_entities`, `get_entity`, `get_neighbors`,
`entity_history`, `recent_changes`, `describe_schema` — to answer questions in
plain language:

```
$ ask "which switches did db-07 depend on last Tuesday — and what changed since?"

→ db-07 dependency path @ 2026-05-26
    core ← leaf-sw-3, spine-sw-1
  Δ since: leaf-sw-3 → leaf-sw-9 (2026-05-28 14:12 UTC)
    spine path unchanged
```

No dashboard pivoting; the assistant reasons over a live, time-aware graph that
came entirely from OTLP entity events.

## Relationships: the edge of the spec — literally

Inventory is half the story; **topology** is the other half — "this process *runs
on* that host," "this interface *connects to* that switch." Here's the catch: the
OpenTelemetry Entity Data Model **does not model entity-to-entity relationships
yet**. [OTEP 0256](https://github.com/open-telemetry/oteps/blob/main/text/entities/0256-entities-data-model.md)
lists relationships as explicit *Future Work*, citing exactly cases like "Process
runs on Host."

A graph consumer needs edges today, so Toise ingests them through a
**vendor-neutral, non-standard extension** that never pretends to be standard
OTel. It rides the same log-record convention with an `entity.relation.*`
namespace:

```
# A relationship — a vendor-neutral entity.relation.* extension
LogRecord
  attributes:
    entity.relation.event.type: state                   # or delete
    entity.relation.type:       runs_on
    entity.relation.from.type:  process
    entity.relation.from.id:    { process.executable.name: nginx, host.name: web-server-1 }
    entity.relation.to.type:    host
    entity.relation.to.id:      { host.name: web-server-1 }
```

The namespace is a deliberate choice: *not* `otel.entity.relationship.*` (that
would squat a reserved OTel namespace before the spec exists), and *not* a
`toise.*` or producer-specific prefix (a neutral name lets any producer and any
consumer speak it). It is shaped to map 1:1 onto the eventual relationships
standard, and the producer↔consumer contract commits both sides to migrating once
that lands. A relation record also carries **no `otel.entity.*` attribute at
all** — its lifecycle rides the neutral `entity.relation.event.type`, so a
standard OTel entity-events consumer cleanly ignores it instead of choking on a
malformed-looking entity event.

This is exactly the kind of thing that belongs upstream. If you care about
inventory and topology semantics in OpenTelemetry, relationships are an open design
area — and reference consumers exercising real queries are a good way to
pressure-test proposals.

## Keep the producer side generic

A consumer should ingest from **any** OpenTelemetry producer and speak the
standard, not a proprietary protocol — it runs no collectors of its own and polls
no devices directly. Emitting entity events from hosts, network gear, or cloud APIs
is the producers' job; [senhub-agent](https://agent.senhub.io) is one such
producer, among others. (Where a real producer isn't available yet, a synthetic
OpenTelemetry SDK client makes a perfectly good spec-conformant reference.) Keeping
the producer side generic is what keeps the ecosystem open.

## Takeaways

- Entity events turn "what exists and how it connects" into first-class
  OpenTelemetry data.
- Consume them as an **event-sourced, bi-temporal** stream, not a mutable table.
- Treat the entity **Id as immutable** and match it exactly — put volatile facts in
  descriptive attributes so history survives change.
- Expose the graph for both humans (GraphQL) and assistants (MCP); the
  natural-language query story is a strong reason to care.
- Relationships aren't in the spec yet — a great place to contribute.

## Get involved

- Read the [Entity Data Model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
  and [OTEP 0256](https://github.com/open-telemetry/oteps/blob/main/text/entities/0256-entities-data-model.md),
  then join the conversation in the OpenTelemetry **Entities SIG** and **Semantic
  Conventions** — relationships are open design space.
- Browse a worked open-source consumer at
  [github.com/toise-dev/toise](https://github.com/toise-dev/toise) (Apache-2.0,
  early development — feedback and contributions welcome).

*Thanks to the Entities SIG for the spec work this builds on.*

<!--
DRAFT NOTES — not part of the article, delete before submitting.

Submission target: open-telemetry/opentelemetry.io, content/en/blog/YYYY/<slug>.md
Process: PR + community review. Front matter above follows their Hugo format;
replace <your-handle> and extend cSpell:ignore as needed.

Review gate #1 is VENDOR NEUTRALITY — this draft keeps Toise as a worked example
(not the subject), adds a disclosure line, names no competitors, and has no
commercial CTA. Preserve that framing on edits.

Technical accuracy — verified 2026-06-02 against repo @ main (M0–M8 + B1–B4 merged):
- Entity wire keys: otel.entity.event.type / .type / .id (map) / .attributes (map);
  event types entity_state, entity_delete. Source: docs/data-model/otel-mapping.md,
  internal/ingest/convert.go.
- Relations: discriminator is entity.relation.event.type with values state / delete
  (NOT relation_state on otel.entity.event.type — changed by #18 "strict relation
  discriminator purity"); keys entity.relation.{type,from.type,from.id,to.type,
  to.id,attributes}. A relation record carries NO otel.entity.* attribute at all.
  Vendor-neutral extension; OTel does not model relationships yet (OTEP 0256 Future
  Work, confirmed live). Source: otel-mapping.md, convert.go.
- Identity: EXACT matching, immutable Id (ADR 0018, supersedes ADR 0017's tolerant
  matching). Volatile values must be descriptive attributes. Source: ADR 0018,
  otel-mapping.md §"Identity matching".
- Bi-temporal: event_time from LogRecord timestamp; recorded_at stamped at ingest
  (ADR 0005).
- MCP tools (6): find_entities, get_entity, get_neighbors, entity_history,
  recent_changes, describe_schema. Transports: Streamable HTTP at /mcp + --mcp-stdio.
  Source: internal/mcp.
- GraphQL: entity, entities, relations, entityHistory, recentChanges + subscriptions
  entityChanged, relationChanged. Source: internal/graphql.

Web validation (2026-06-02):
- Entity Data Model page resolves; status is "Development" (not stable / not
  "experimental") — wording in the disclosure updated accordingly.
- Relationships confirmed Future Work verbatim ("relationship modelling will be
  refined in future specification work").
- Data model defines Type / ID (map, >=1) / Description (map) — matches Toise.
- Links validated: entities data-model, OTEP 0256. Note the exact otel.entity.*
  event attribute KEYS are not pinned on the data-model page itself (they live in
  the evolving entity-events/semconv work) — hence the "illustrative" caveat.
  Other real links if useful: /docs/specs/otel/entities/ ,
  /docs/specs/semconv/registry/entities/ .
- NOT verified: specific Slack channel names / SIG meeting cadence — left generic
  on purpose; fill in from the live community page before submitting.
-->
