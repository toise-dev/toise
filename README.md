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
OpenTelemetry producer — for example
[senhub-agent](https://github.com/senhub-io/senhub-agent), an OpenTelemetry
Collector, or your own instrumentation. Toise itself runs no collectors.

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

Toise is pre-1.0 (alpha), but **production-capable as of 0.3.0**. It ingests OTLP
entity events, maintains a bi-temporal event log and an in-memory graph with
change classification, and serves that one read model — scoped **per tenant** —
through three surfaces:

- a **GraphQL** API (`/graphql`, with a playground at `/playground`),
- a native **MCP** server (`/mcp` and stdio) for LLM assistants, and
- a minimal **debug UI** (`/`) for operators —

all from a single Go binary with no external runtime dependencies. 0.3.0 adds the
operational surface for real deployments: native bearer-token auth and TLS, a
`--production` lockdown, `/healthz`·`/readyz`·Prometheus `/metrics`, retention
pruning, projection snapshots, packaged release artifacts, and multi-tenant
isolation.

Pre-1.0, the surfaces can still evolve — but since 0.7.0 the **public contracts**
(the OTLP wire contract, the MCP tools/resources/prompts, and the GraphQL schema)
are **pinned** by a byte-exact conformance fixture and a golden contract test, and
governed by a published [API stability policy](docs/user-guide/docs/api-stability.md):
changes are additive within a release series, and a breaking change ships only with
a deprecation notice in the preceding release plus a migration guide. After 1.0 the
surfaces follow semantic versioning.

## Quickstart

**No build needed — a live graph in under a minute.** Grab the release tarball for
your platform from the [releases page](https://github.com/toise-dev/toise/releases)
(it ships `toise-server` + `toise-probe`), then run the server and point a probe at
it — `toise-probe` is a real OTLP/gRPC producer that heartbeats an evolving
topology (process restarts, an interface flap, a container crash, multi-agent
reference counting):

```bash
./toise-server --data-dir ./toise-data &        # GraphQL + MCP + debug UI on :8080
./toise-probe  --producer agent-a               # in another terminal
./toise-probe  --producer agent-b               # a second agent sharing the host/db
# open http://127.0.0.1:8080/
```

Prefer a container? The server image is on GHCR — then point any OTLP entity-event
producer at `:4317`:

```bash
docker run --rm -p 8080:8080 -p 4317:4317 ghcr.io/toise-dev/toise:latest
```

**From source** (for contributors) also builds `toise-demo`, which seeds a
self-contained "day in the life of web-server-1" scenario — an instant graph with
no producer:

```bash
make build                                  # bin/toise-server, toise-demo, toise-probe
./bin/toise-demo   --data-dir ./demo-data   # seed the demo event log
./bin/toise-server --data-dir ./demo-data   # then open http://127.0.0.1:8080/
```

The demo scenario and a set of example LLM prompts (with the MCP tool calls they
map to) are in [`docs/demo/`](./docs/demo).

## Security

By default Toise binds to `127.0.0.1` and runs with **no authentication** — the
trusted-network posture: run it on a private segment or behind a VPN and exposing
it to other hosts is an explicit choice (ADR 0014).

For exposed deployments, 0.3.0 adds opt-in hardening (ADR 0024):

- **Bearer-token authentication** on the ingest and query surfaces, with tokens
  supplied via the environment (`TOISE_AUTH_TOKENS`). The operational probes and
  the metrics scrape stay public.
- **TLS** from a cert/key pair.
- **`--production`** to turn off GraphQL introspection, the playground, and the
  debug UI in one move, plus an `allowed_origins` WebSocket allowlist.

**Multi-tenancy:** a single instance serves multiple tenants with fully isolated
graphs, scoped by the `X-Scope-OrgID` request metadata (or a `tenant.id` resource
attribute). Authentication is not yet bound to a tenant — a valid token may set any
`X-Scope-OrgID` — so isolation relies on the upstream OTel Collector authenticating
each client and stamping its tenant. See
[Configuration → Multi-tenancy](./docs/operations/configuration.md#multi-tenancy).

## Documentation

Design notes, architecture decisions, and roadmap live in the [`docs/`](./docs)
directory. The public website is at [toise.dev](https://toise.dev).

`toise-server` is configured by a YAML file, environment variables, or flags —
see [Configuring toise-server](./docs/operations/configuration.md) and the
annotated [`examples/toise-server.yaml`](./examples/toise-server.yaml).

The query surfaces are documented in
[GraphQL API reference](./docs/reference/graphql.md) (schema, pagination,
bi-temporal queries, guardrails) and the MCP tools
([ADR 0011](./docs/architecture/adr/0011-mcp-server-design.md)).

To deploy, see [Deploying toise-server](./docs/operations/deployment.md) — prebuilt
binaries, the GHCR container image, and the [`deploy/`](./deploy) examples (systemd,
Docker Compose).

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) and
[CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md).

## License

Apache License 2.0. See [LICENSE](./LICENSE).

## Maintainers

Toise is initiated and primarily maintained by
[Sensor Factory](https://sensorfactory.fr). Contributions from the broader
community are welcome.
