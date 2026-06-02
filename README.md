<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-full-dark.png" />
    <img alt="Toise" src="docs/assets/logo-full-light.png" width="260" />
  </picture>
</p>

<p align="center"><em>The living map of your infrastructure.</em></p>

Toise is an open-source backend that maintains a live, queryable graph of your
infrastructure — hosts, processes, network interfaces, addresses, routes,
services, and the relationships between them.

**LLM-first.** A native [Model Context Protocol](https://modelcontextprotocol.io)
server lets an AI assistant query the graph on an operator's behalf, in plain
language — inventory, topology, dependencies, and *what changed*. Humans can
also query it directly via GraphQL or a built-in debug UI.

**OpenTelemetry-native.** Toise ingests OTLP entity events from any
OpenTelemetry producer — for example senhub-agent, an OpenTelemetry Collector,
or your own instrumentation. Toise itself runs no collectors.

**Temporal by construction.** An event-sourced, bi-temporal log makes history
and change first-class: not just "what is the state", but "what changed", "why
is this different from yesterday", and "show me the timeline".

Toise ships as a single Apache-2.0 Go binary with no external runtime
dependencies — no cluster, no orchestrator. It is the missing
inventory-and-topology brick of the modern open-source observability stack
(OpenTelemetry, VictoriaMetrics, Grafana, Loki, Tempo).

## Why Toise

Modern observability stacks have closed the visibility gap for applications,
hosts, containers, and services. The living inventory of the underlying
infrastructure — what exists, how it connects, and how it changes over time —
remains a blind spot. Toise fills it, designed so an AI assistant can answer an
operator's questions about it directly.

## Status

Toise is in early development. **Phase 1 is feature-complete**: it ingests OTLP
entity events, maintains a bi-temporal event log and an in-memory graph with
change classification, and serves that one read model through three surfaces —

- a **GraphQL** API (`/graphql`, with a playground at `/playground`),
- a native **MCP** server (`/mcp` and stdio) for LLM assistants, and
- a minimal **debug UI** (`/`) for operators —

all from a single Go binary with no external runtime dependencies. We are not yet
ready for production use, and there is no authentication yet (see below). Expect
breaking changes.

## Quickstart

```bash
make build                                  # builds bin/toise-server, toise-demo, toise-probe
./bin/toise-demo --data-dir ./demo-data     # seed the "day in the life of web-server-1" demo
./bin/toise-server --data-dir ./demo-data   # then open http://127.0.0.1:8080/
```

For a **live** demo over the real OTLP path, run a fresh server and point one or
more `toise-probe` agents at it — a real OTLP/gRPC producer that heartbeats an
evolving infrastructure topology (process restarts, an interface flap, a
container crash, multi-agent reference counting):

```bash
./bin/toise-server --data-dir ./live-data &
./bin/toise-probe --producer agent-a            # in another terminal
./bin/toise-probe --producer agent-b            # a second agent sharing the host/db
```

The demo scenario and a set of example LLM prompts (with the MCP tool calls they
map to) are in [`docs/demo/`](./docs/demo).

## Security (phase 1)

Phase 1 has **no authentication**. Toise is intended to run only on trusted
networks (private datacenter segments, VPN-protected networks); operators are
responsible for network-level isolation. The server binds to `127.0.0.1` by
default — exposing it to other hosts requires an explicit choice. Proper
authentication (mTLS, OIDC, fine-grained authorization) is planned for a later
phase.

## Documentation

Design notes, architecture decisions, and roadmap live in the [`docs/`](./docs)
directory. The public website is at [toise.dev](https://toise.dev).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) and
[CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md).

## License

Apache License 2.0. See [LICENSE](./LICENSE).

## Maintainers

Toise is initiated and primarily maintained by
[Sensor Factory](https://sensorfactory.fr). Contributions from the broader
community are welcome.
