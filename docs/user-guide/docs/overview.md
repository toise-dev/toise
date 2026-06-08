# What is Toise?

Metrics, logs, and traces tell you how your systems *behave*. They are much
quieter about what actually *exists*: which hosts, interfaces, switches,
services, and volumes are out there right now, how they connect, and how that
picture changed over the last hour, day, or quarter. Modern observability stacks
have closed the visibility gap for applications, hosts, and services; the living
inventory and topology of the underlying infrastructure has stayed a blind spot.

**Toise fills that gap.** It is an open-source backend that maintains a live,
queryable graph of your infrastructure and how it changes over time. It is the
missing inventory-and-topology brick of the modern open-source observability
stack (OpenTelemetry, VictoriaMetrics, Grafana, Loki, Tempo).

## Three properties that define it

### LLM-first

Toise's primary consumer is an AI assistant. A native
[Model Context Protocol](https://modelcontextprotocol.io) server lets an
assistant query the graph on an operator's behalf, in plain language —
inventory, topology, dependencies, and *what changed* — with no bespoke glue
between the model and the backend. Humans can query the same model directly via
GraphQL or a built-in debug UI.

### OpenTelemetry-native

Toise ingests OTLP **entity events** from any OpenTelemetry producer — for
example [senhub-agent](https://agent.senhub.io), an OpenTelemetry Collector, or
your own instrumentation. **Toise itself runs no collectors and polls no
devices.** Its data model aligns with the
[OpenTelemetry entity data model](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
so it slots into an existing OTel deployment rather than introducing a parallel
vocabulary.

### Temporal by construction

An event-sourced, **bi-temporal** log makes history and change first-class: not
just "what is the state", but "what changed", "why is this different from
yesterday", and "show me the timeline". Every fact carries both when it became
true in reality (`event_time`) and when Toise learned it (`recorded_at`).

## How it fits together

```text
 OpenTelemetry producers              Toise                       Consumers
 (senhub-agent, OTel        ┌──────────────────────────┐
  Collector, your own  ───▶ │  OTLP entity events in   │
  instrumentation)     OTLP │            │             │
                            │            ▼             │   GraphQL  ──▶ tools, dashboards
                            │   durable event log      │   MCP      ──▶ AI assistant
                            │            │             │   Debug UI ──▶ operators
                            │            ▼             │
                            │  live bi-temporal graph  │ ──────────▶
                            └──────────────────────────┘
```

Producers emit entity events over OTLP. Toise appends every event to a durable,
ordered log (the system of record) and projects it into a live, in-memory graph
of entities and relations with change classification. That one read model is
exposed through three surfaces — GraphQL, MCP, and a debug UI — which therefore
always report the same world.

## What Toise is *not*

- **Not a collector.** It does not scrape, poll, or run agents on your hosts.
  Emitting entity events is the producers' job; keeping the producer side
  generic is what keeps the ecosystem open.
- **Not a metrics/traces store.** It tracks *entities and their relationships*,
  not time series or spans — it is the join key *across* your other signals, not
  a replacement for them.
- **Not a guesser.** The engine stores only the facts producers assert, never
  derived or inferred state. Correlation and interpretation belong at the edges
  (the producer or the consuming LLM), never in the source of truth
  ([ADR 0022](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0022-engine-stores-facts-only.md)).

## Status

Toise is in early development. **Phase 1 is feature-complete**: it ingests OTLP
entity events, maintains a bi-temporal event log and an in-memory graph with
change classification, and serves that one read model through GraphQL, MCP, and a
debug UI — all from a single Apache-2.0 Go binary with no external runtime
dependencies. It is **not yet production-ready**, and there is **no
authentication yet**. Expect breaking changes.

Next: [install and run toise-server](installation.md).
