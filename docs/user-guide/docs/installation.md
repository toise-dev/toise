# Installation

Toise ships as a single Apache-2.0 Go binary with **no external runtime
dependencies** — no cluster, no orchestrator, no database to provision. The
storage engine ([Pebble](https://github.com/cockroachdb/pebble)) is embedded.

Toise is distributed as **source** and as prebuilt release artifacts (static
binaries and an OCI image); the quickest path is to build from source with the Go
toolchain.

## Prerequisites

- **Go 1.26 or newer** ([go.dev/dl](https://go.dev/dl/)).
- `make` and `git`.
- A POSIX shell (Linux or macOS). Windows builds are produced in CI.

## Build from source

```bash
git clone https://github.com/toise-dev/toise.git
cd toise
make build
```

`make build` produces three binaries in `bin/`:

| Binary | What it is |
| --- | --- |
| `toise-server` | the backend — OTLP ingestion + GraphQL + MCP + debug UI |
| `toise-demo` | seeds a self-contained demo dataset (no producer needed) |
| `toise-probe` | a real OTLP/gRPC producer for live, end-to-end testing |

!!! note "Use `make`, not raw `go build`"
    The Makefile injects version metadata via `-ldflags` and sets the build
    environment. Prefer `make build` over calling `go build` directly.

### Alternative: `go install`

To install just the server into your `GOBIN`:

```bash
go install github.com/toise-dev/toise/cmd/toise-server@latest
```

This skips the version-stamping that `make build` performs, but is convenient
for a quick try.

## Run it

The fastest way to *see* something is the bundled demo — it seeds a
"day in the life of `web-server-1`" dataset, so you don't need a producer yet:

```bash
./bin/toise-demo   --data-dir ./demo-data     # seed the demo event log
./bin/toise-server --data-dir ./demo-data     # serve it
```

Then open **<http://127.0.0.1:8080/>** for the debug UI, or the GraphQL
playground at **<http://127.0.0.1:8080/playground>**.

### A live run over the real OTLP path

To exercise the real ingestion path, run a fresh server and point one or more
`toise-probe` agents at it — a real OTLP/gRPC producer that heartbeats an
evolving topology (process restarts, an interface flap, multi-agent reference
counting):

```bash
./bin/toise-server --data-dir ./live-data &
./bin/toise-probe --producer agent-a            # in another terminal
./bin/toise-probe --producer agent-b            # a second agent sharing a host/db
```

Or simulate a larger fabric in one process:

```bash
./bin/toise-probe --hosts 60 --interval 60s --heartbeat 6s   # a 60-machine fleet
```

## Default endpoints

| Path | Surface |
| --- | --- |
| `http://127.0.0.1:8080/` | Debug UI (operators) |
| `http://127.0.0.1:8080/graphql` | GraphQL API (and WebSocket subscriptions) |
| `http://127.0.0.1:8080/playground` | Interactive GraphQL playground |
| `http://127.0.0.1:8080/mcp` | MCP server (Streamable HTTP) |
| `127.0.0.1:4317` | OTLP/gRPC ingestion |

All listeners bind to **loopback by default**. Exposing them to other hosts is
an explicit choice — see [Configuration](configuration.md).

!!! warning "No authentication by default"
    Out of the box Toise has no authentication — keep it on loopback or a trusted,
    network-isolated segment. For an exposed deployment, enable bearer-token auth
    and TLS and run with `--production`. See
    [Authentication & TLS](configuration.md#authentication-tls).

## Next steps

- [Configure toise-server](configuration.md) — listeners, storage, retention.
- [Ingest OTLP entity events](ingestion.md) from your producers.
- [Query the graph](querying/index.md) via GraphQL, MCP, or the debug UI.
