# Configuration

`toise-server` resolves its configuration from four layers. From **lowest to
highest precedence**:

1. **Built-in defaults** — loopback listeners, `toise-data` data dir, no
   retention cap. Running with nothing set behaves exactly as the historical
   flag defaults.
2. **YAML file** — `--config <path>` or the `TOISE_CONFIG` environment variable.
3. **Environment variables** — `TOISE_*`. Override the file. **Secrets belong
   here** (never on the command line).
4. **Command-line flags** — override everything; useful for ad-hoc overrides.

Each layer overrides only what it sets, so you can keep a committed base file and
override one value with an env var or a flag for a one-off run. See
[ADR 0023](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0023-layered-configuration.md)
for the rationale.

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
| `snapshot_interval` | `TOISE_SNAPSHOT_INTERVAL` | `--snapshot-interval` | `5m` | projection snapshot cadence for fast restart and liveness survival across restarts (`0` = disabled) |
| `log_format` | `TOISE_LOG_FORMAT` | `--log-format` | `text` | log output format: `text` or `json` |
| `log_level` | `TOISE_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, or `error` |
| `production` | `TOISE_PRODUCTION` | `--production` | `false` | hardening profile — forces the three below off |
| `graphql_introspection` | `TOISE_GRAPHQL_INTROSPECTION` | `--graphql-introspection` | `true` | expose GraphQL introspection |
| `playground` | `TOISE_PLAYGROUND` | `--playground` | `true` | serve the GraphQL playground at `/playground` |
| `debug_ui` | `TOISE_DEBUG_UI` | `--debug-ui` | `true` | serve the debug UI at `/` |
| `allowed_origins` | `TOISE_ALLOWED_ORIGINS` | `--allowed-origins` | (empty) | comma-separated browser Origin allowlist (WebSocket/CORS); empty = same-origin only |
| `auth_tokens` | `TOISE_AUTH_TOKENS` | *(none — secret)* | (empty) | comma-separated bearer tokens, valid for every tenant; empty = auth disabled |
| `tenant_tokens` | `TOISE_TENANT_TOKENS` | *(none — secret)* | (empty) | comma-separated `tenant:token` pairs — the token is authorized only for its tenant (HTTP 403 / gRPC PermissionDenied elsewhere) |
| `accept_unknown_types` | `TOISE_ACCEPT_UNKNOWN_TYPES` | `--accept-unknown-types` | `false` | accept entity/relation types outside the built-in registry (shape still validated; counted on /metrics) |
| `tenant_auto_create` | `TOISE_TENANT_AUTO_CREATE` | `--tenant-auto-create` | `true` | allow a first write to a new tenant id to create its stack; off = only pre-existing tenants (and `default`) are served |
| `tenant_allowlist` | `TOISE_TENANT_ALLOWLIST` | `--tenant-allowlist` | (empty) | comma-separated tenant ids allowed to be created; empty = any (subject to auto-create and the cap) |
| `max_tenants` | `TOISE_MAX_TENANTS` | `--max-tenants` | `0` | cap on open tenants; `0` = unbounded. Reading an unknown tenant never creates it (404) |
| `tls_cert_file` | `TOISE_TLS_CERT_FILE` | `--tls-cert-file` | (empty) | PEM certificate; with the key, serves HTTP + OTLP over TLS |
| `tls_key_file` | `TOISE_TLS_KEY_FILE` | `--tls-key-file` | (empty) | PEM private key (pairs with the cert) |

Durations are Go-duration strings (`"30s"`, `"5m"`, `"1h30m"`). **Unknown YAML
keys are rejected** — a typo fails at startup rather than being silently ignored.

`--mcp-stdio` (serve only the MCP server over stdio, for Claude Desktop) is a run
mode rather than steady-state config; pass it as a flag when needed — see
[MCP for AI assistants](querying/mcp.md).

## Examples

A complete annotated file lives at
[`examples/toise-server.yaml`](https://github.com/toise-dev/toise/blob/main/examples/toise-server.yaml).
A minimal one:

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

## Authentication & TLS

Both are **off by default** — the server binds to `127.0.0.1` and trusts the
network (private datacenter segment or VPN; ADR 0014). Exposing it to other hosts
(`0.0.0.0:...`) is an explicit choice; when you make it, turn these on.

- **Bearer tokens.** Set `auth_tokens` via the environment only — they are secrets
  and must never appear on the command line or in a committed file:

  ```sh
  TOISE_AUTH_TOKENS="tok-a,tok-b" toise-server --config /etc/toise/toise-server.yaml
  ```

  Clients then send `Authorization: Bearer <token>` on both HTTP and gRPC. The
  operational probes (`/healthz`, `/readyz`) and the metrics scrape (`/metrics`)
  stay public so a load balancer and Prometheus can reach them without a token.
- **TLS.** Point `tls_cert_file` and `tls_key_file` at a PEM cert/key pair to serve
  the HTTP surfaces and OTLP ingestion over TLS.

See [ADR 0024](https://github.com/toise-dev/toise/blob/main/docs/architecture/adr/0024-native-auth-and-tls.md).

## Hardening for production

The development surfaces — GraphQL introspection, the `/playground`, and the debug
UI — are on by default for a friendly local experience. For an exposed deployment,
turn them off with a single switch:

```sh
toise-server --production        # or production: true / TOISE_PRODUCTION=true
```

`production: true` forces `graphql_introspection`, `playground`, and `debug_ui`
off, winning over any individual setting. Set `allowed_origins` to the browser
Origins permitted for WebSocket subscriptions and CORS (empty = same-origin only).

## Multi-tenancy

One Toise instance can serve multiple tenants with **fully isolated** graphs — a
query scoped to tenant A never sees tenant B (ADR 0025). Each tenant gets its own
store + projection + change engine under `<data_dir>/<tenant>/`.

The tenant id is generic and vendor-neutral, resolved (in order) from:

1. the **`X-Scope-OrgID`** request metadata — the de-facto standard used by
   Mimir/Loki/Tempo/VictoriaMetrics (an HTTP header on the query surfaces, gRPC
   metadata on OTLP ingest);
2. a **`tenant.id`** resource attribute on the OTLP request (set per
   `ResourceLogs`, it overrides the request metadata — so one OTLP stream can carry
   several tenants);
3. otherwise the **`default`** tenant.

```sh
# Query tenant "acme" over HTTP
curl -H 'X-Scope-OrgID: acme' http://127.0.0.1:8080/graphql \
  -d '{"query":"{ entities { totalCount } }"}'
```

**Single-tenant deployments need no configuration:** with no tenant id ever set,
everything lives under `default` and behaves as a single-graph build. A
pre-existing data directory is migrated to `<data_dir>/default/` automatically on
first start (take a backup first, as with any upgrade). `/metrics` reports the sum
across tenants, so existing dashboards are unchanged.

**Boot quarantine:** if a tenant's store cannot be opened at startup (a corrupt
or half-written store), the server **does not abort** — it logs a warning, skips
that tenant, counts it in the `toise_tenants_quarantined` gauge, and serves the
healthy tenants. The `default` tenant is the exception: it is required, so its
failure is fatal. A quarantined tenant's directory is left on disk under
`<data_dir>/<tenant>/` for recovery; restore or remove it and restart.

**Deleting a tenant** is a cold, destructive operation — run it with the server
stopped (it holds the pebble lock while serving):

```sh
toise-server delete-tenant --data-dir /var/lib/toise/data acme
```

It removes `<data_dir>/acme/` (event log and snapshot) entirely and frees a slot
under `max_tenants`. The `default` tenant cannot be deleted.

**Metrics and tenants:** the Toise gauges on `/metrics` (`toise_entities`,
`toise_events_total`, `toise_store_disk_bytes`, …) are **aggregated across
tenants** — a sum, except `toise_snapshot_seq` and `toise_store_prune_horizon_seconds`
which report the high-water mark. They carry no per-tenant label, so the endpoint
stays single-series and dashboards are unchanged. The maintenance metrics
(`toise_maintenance_*`) are the exception: they label by `tenant` (and `op`,
`outcome`) so a stuck sweep/prune can be traced to its tenant.

!!! warning "Authentication is not yet bound to a tenant"
    A valid bearer token may set any `X-Scope-OrgID`. Isolation therefore relies on
    the upstream OTel Collector authenticating each client and stamping its tenant;
    do not expose the ports directly to untrusted multi-tenant clients. Per-token
    tenant binding is planned.

## Operational endpoints

Always on, on the `listen` address: `/healthz` (liveness), `/readyz` (readiness —
green only when every tenant store is healthy), and Prometheus `/metrics` (sampled
at scrape time). Wire these into your orchestrator and scrape config.

See also [Storage sizing](operations/storage-sizing.md) for choosing
`retention_max_age`, and [Performance](operations/performance.md) for the
sweep/compaction cadences.
