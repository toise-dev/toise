<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-full-dark.png" />
    <img alt="Toise" src="docs/assets/logo-full-light.png" width="260" />
  </picture>
</p>

<p align="center"><em>The living map of your infrastructure.</em></p>

Toise is an open-source backend that maintains a live, queryable graph of your
infrastructure — hosts, processes, containers, pods, network interfaces,
addresses, routes, network devices, services, and the relationships between them.

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
is this different from yesterday", and "show me the timeline" — including reading
the graph as it stood at any past instant.

Toise ships as a single Apache-2.0 Go binary with no external runtime
dependencies — no cluster, no orchestrator, and no configuration to start. It is
the missing inventory-and-topology brick of the modern open-source observability
stack (OpenTelemetry, VictoriaMetrics, Grafana, Loki, Tempo).

## Why Toise

Modern observability stacks have closed the visibility gap for applications,
hosts, containers, and services. The living inventory of the underlying
infrastructure — what exists, how it connects, and how it changes over time —
remains a blind spot. Toise fills it, designed so an AI assistant can answer an
operator's questions about it directly.

## Status

Toise is pre-1.0 and **production-capable**: it runs the authors' own
infrastructure and a shared multi-tenant lab. It ingests OTLP entity events,
maintains a bi-temporal event log and an in-memory graph with change
classification, and serves that one read model — scoped **per tenant** — through
three surfaces:

- a **GraphQL** API (`/graphql`, with a playground at `/playground`),
- a native **MCP** server (`/mcp` and stdio) for LLM assistants, and
- a minimal **debug UI** (`/`) for operators.

What the releases since 0.3.0 added, in one line each:

| | |
|---|---|
| 0.8.0 | access security and resilience — the two pillars for exposing Toise as a service (ADR 0028, ADR 0029) |
| 0.9.x | strict alignment with the OpenTelemetry entity-events spec, read-surface security, and a full review pass |
| 0.10.0–0.14.0 | delete provenance, host-scoped endpoints, vocabulary hygiene, pods, one-answer-per-question identity, reachability |
| 0.15.0–0.16.0 | answers that admit what they did not cover, and promises kept at scale |
| 0.17.x | saying what an answer is worth — durable entity handles, and a week of field fixes |
| 0.18.0 | making the graph legible — a readable name of its own, and a viz that folds a hairball into a shape |

Pre-1.0, the surfaces can still evolve — but since 0.7.0 the **public contracts**
(the OTLP wire contract, the MCP tools/resources/prompts, and the GraphQL schema)
are **pinned** by a byte-exact conformance fixture and a golden contract test, and
governed by a published [API stability policy](docs/user-guide/docs/api-stability.md):
changes are additive within a release series, and a breaking change ships only with
a deprecation notice in the preceding release plus a migration guide. After 1.0 the
surfaces follow semantic versioning.

**One known limitation worth knowing before you rely on it.** The event log is
complete and reading the graph at a past instant is exact, but on a tenant
ingesting thousands of events per minute the *change-feed* read (`recentChanges`,
`entityHistory` bounded by time) under-reports and can answer `0` for a period
that was busy, without saying so —
[#397](https://github.com/toise-dev/toise/issues/397). Below a few hundred events
per minute it is accurate. If you plan to investigate incidents from a
high-volume tenant, follow that issue.

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

`toise-probe --hosts 200 --devices 20` generates a multi-machine fabric instead of
the narrative scenario, which is the quickest way to see what the graph looks like
at size.

Prefer a container? The server image is on GHCR — then point any OTLP entity-event
producer at `:4317`:

```bash
docker run --rm -p 8080:8080 -p 4317:4317 ghcr.io/toise-dev/toise:latest
```

> **The image binds to `0.0.0.0`, the binary to `127.0.0.1`.** The container has to,
> to be reachable through a published port. So the loopback default described under
> [Security](#security) protects the binary, not the image: a container with
> published ports and no authentication is reachable by anything that can reach the
> host.

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
[`examples/graph-viz/`](./examples/graph-viz) is a zero-dependency single-page
graph viewer that reads the live GraphQL subscription — an example, not a product
surface, and the fastest way to *look* at a graph.

## Producing data

Any OTLP producer that emits entity events works. Two things make writing one
easier:

- **[`pkg/emit`](./pkg/emit)** — a small Go SDK for emitting entity events and
  relationships, versioned as a nested module (`pkg/emit/vX.Y.Z`) so it can move
  independently of the server. The worked examples under
  [`examples/`](./examples) — minimal, systemd, docker, uptime — each build on it.
- **`toise-conformance`** — validates a producer's OTLP output against the wire
  contract **without a running Toise**. Feed it the bytes your producer would
  export and it reports every violation; exit 0 means Toise will accept the
  records. Useful in a producer's own CI.

The contract a producer must satisfy is written down in
[`docs/data-model/`](./docs/data-model), with the OpenTelemetry mapping alongside
it.

## Security

By default `toise-server` binds to `127.0.0.1` and runs with **no
authentication** — the trusted-network posture: run it on a private segment or
behind a VPN, and exposing it to other hosts is an explicit choice (ADR 0014).
Zero-config staying usable is a design commitment, not an oversight (ADR 0030) —
so everything below is **opt-in** and off by default.

For exposed and multi-tenant deployments (ADR 0028):

- **Bearer tokens**, global or **scoped to one tenant**, with per-tenant roles
  (read / ingest / full). Tokens come from the environment and are **hashed at
  rest**, so a leaked config or memory dump exposes no usable credential.
- **`tenant_trust_mode: derive-only`** — the tenant is derived from the token and
  the client's `X-Scope-OrgID` header is ignored, so a caller can never claim
  another tenant.
- **OIDC / JWT** verification on the read surfaces, with tenant and role read
  from claims.
- **mTLS on ingest**, to authenticate producers by client certificate.
- **TLS** from a cert/key pair, and an append-only **audit log** of operator
  writes.
- **`--production`** to turn off GraphQL introspection, the playground and the
  debug UI in one move, plus an `allowed_origins` WebSocket allowlist.

**Multi-tenancy:** a single instance serves multiple tenants with fully isolated
graphs, scoped by the `X-Scope-OrgID` request metadata or a `tenant.id` resource
attribute. With tenant-scoped tokens and `derive-only`, isolation is enforced by
Toise itself and no longer depends on an upstream collector stamping the tenant.
See [Configuration → Multi-tenancy](./docs/operations/configuration.md#multi-tenancy).

**Resilience** (ADR 0029): clustered read replicas, snapshot and event-log
offload to S3-compatible object storage, and recovery from a snapshot plus the
log tail.

## Documentation

The user guide is published at [toise.dev/docs](https://toise.dev/docs), and the
public website at [toise.dev](https://toise.dev). Design notes, architecture
decisions and the roadmap live in the [`docs/`](./docs) directory.

`toise-server` is configured by a YAML file, environment variables, or flags —
see [Configuring toise-server](./docs/operations/configuration.md) and the
annotated [`examples/toise-server.yaml`](./examples/toise-server.yaml).

The query surfaces are documented in
[GraphQL API reference](./docs/reference/graphql.md) (schema, pagination,
bi-temporal queries, guardrails) and the MCP tools
([ADR 0011](./docs/architecture/adr/0011-mcp-server-design.md)). The
[API stability policy](docs/user-guide/docs/api-stability.md) says what may change
and when.

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
