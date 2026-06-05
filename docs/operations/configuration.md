# Configuring toise-server

`toise-server` resolves its configuration from four layers. From **lowest to
highest precedence**:

1. **Built-in defaults** — loopback listeners, `toise-data` data dir, no retention
   cap. Running with nothing set behaves exactly as the historical flag defaults.
2. **YAML file** — `--config <path>` or the `TOISE_CONFIG` environment variable.
3. **Environment variables** — `TOISE_*`. Override the file. **Secrets belong here**
   (never on the command line).
4. **Command-line flags** — override everything; useful for ad-hoc overrides.

Each layer overrides only what it sets, so you can keep a committed base file and
override one value with an env var or a flag for a one-off run. See
[ADR 0023](../architecture/adr/0023-layered-configuration.md) for the rationale.

## Settings

| YAML key | Env var | Flag | Default | Meaning |
| --- | --- | --- | --- | --- |
| `listen` | `TOISE_LISTEN` | `--listen` | `127.0.0.1:8080` | GraphQL/HTTP + MCP + debug UI address |
| `otlp_listen` | `TOISE_OTLP_LISTEN` | `--otlp-listen` | `127.0.0.1:4317` | OTLP/gRPC ingestion address |
| `data_dir` | `TOISE_DATA_DIR` | `--data-dir` | `toise-data` | Pebble event-log directory |
| `relation_buffer_ttl` | `TOISE_RELATION_BUFFER_TTL` | `--relation-buffer-ttl` | `30s` | hold an out-of-order edge waiting for its endpoints (`0` = disabled) |
| `liveness_sweep_interval` | `TOISE_LIVENESS_SWEEP_INTERVAL` | `--liveness-sweep-interval` | `30s` | how often to expire entities past their heartbeat interval (`0` = disabled) |
| `retention_max_age` | `TOISE_RETENTION_MAX_AGE` | `--retention-max-age` | `0` | max age of retained events (`0` = unlimited) |
| `retention_compaction_interval` | `TOISE_RETENTION_COMPACTION_INTERVAL` | `--retention-compaction-interval` | `1h` | heartbeat-coalescing compaction cadence |

Durations are Go-duration strings (`"30s"`, `"5m"`, `"1h30m"`). **Unknown YAML keys
are rejected** — a typo fails at startup rather than being silently ignored.

`--mcp-stdio` (serve only the MCP server over stdio, for Claude Desktop) is a run
mode rather than steady-state config; pass it as a flag when needed.

## Examples

A complete annotated file lives at
[`examples/toise-server.yaml`](../../examples/toise-server.yaml). A minimal one:

```yaml
listen: 0.0.0.0:8080
otlp_listen: 0.0.0.0:4317
data_dir: /var/lib/toise
retention_max_age: 720h   # 30 days
```

Run it:

```sh
toise-server --config /etc/toise/toise-server.yaml
# or
TOISE_CONFIG=/etc/toise/toise-server.yaml toise-server
```

Override one value for a single run without editing the file:

```sh
# env wins over the file...
TOISE_DATA_DIR=/tmp/scratch toise-server --config /etc/toise/toise-server.yaml
# ...and a flag wins over both
toise-server --config /etc/toise/toise-server.yaml --listen 127.0.0.1:9999
```

> **Security (phase 1).** There is no authentication yet (ADR 0014): keep `listen` /
> `otlp_listen` on loopback or a trusted network. When auth and TLS land (#43),
> their secrets will be sourced from the environment, never from flags.
