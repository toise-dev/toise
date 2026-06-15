# Examples

Worked examples of integrating with Toise. None of this is part of
`toise-server` — each example talks to Toise only over its public surfaces (OTLP
ingest, GraphQL, MCP), the same way a third-party integration would. Nothing here
is compiled into the server binary.

## Consumers

Read from Toise over GraphQL / MCP.

| Example | What it shows |
|---------|---------------|
| [graph-viz](./graph-viz/) | A dependency-free browser page that draws the entity/relation graph and streams live changes over GraphQL subscriptions. |

## Producers

Feed your own data into Toise. They are built on the [`pkg/emit`](../pkg/emit)
SDK; the wire contract and identity rules they follow are documented in
[`docs/data-model/otel-mapping.md`](../docs/data-model/otel-mapping.md). Start
with **producer-minimal** — it uses only the built-in vocabulary and runs against
a stock server.

| Example | Source it maps | Notes |
|---------|----------------|-------|
| [producer-minimal](./producer-minimal/) | A host and a service listener (static) | The smallest useful producer; built-in types, no flags. |
| [producer-docker](./producer-docker/) | Local Docker containers | Needs `docker`; `container` is a built-in type. |
| [producer-uptime](./producer-uptime/) | HTTP/website uptime of a URL list | Needs `--accept-unknown-types` (`service.endpoint`). |
| [producer-systemd](./producer-systemd/) | systemd service units (Linux) | Needs `systemctl` + `--accept-unknown-types` (`service`). |

The uptime and systemd examples emit entity types outside the built-in registry
(`service.endpoint`, `service`), so they run against a server started with
`--accept-unknown-types` (the open-vocabulary posture) — each README spells this
out. The minimal and docker examples use only built-in types.

To validate any producer's output against the wire contract without a running
server, use the [`toise-conformance`](../pkg/emit/cmd/toise-conformance) CLI. The
full catalog of known producers (these examples, the SDK, and external producers)
lives in the [producer directory](../docs/user-guide/docs/producers.md).
