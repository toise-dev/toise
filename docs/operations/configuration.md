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
| `snapshot_interval` | `TOISE_SNAPSHOT_INTERVAL` | `--snapshot-interval` | `5m` | projection snapshot cadence for fast restart and liveness survival across restarts (`0` = disabled) |
| `log_format` | `TOISE_LOG_FORMAT` | `--log-format` | `text` | log output format: `text` or `json` |
| `log_level` | `TOISE_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, or `error` |
| `production` | `TOISE_PRODUCTION` | `--production` | `false` | hardening profile — forces the three below off |
| `graphql_introspection` | `TOISE_GRAPHQL_INTROSPECTION` | `--graphql-introspection` | `true` | expose GraphQL introspection |
| `playground` | `TOISE_PLAYGROUND` | `--playground` | `true` | serve the GraphQL playground at `/playground` |
| `debug_ui` | `TOISE_DEBUG_UI` | `--debug-ui` | `true` | serve the debug UI at `/` |
| `allowed_origins` | `TOISE_ALLOWED_ORIGINS` | `--allowed-origins` | (empty) | comma-separated browser Origin allowlist (WebSocket/CORS); empty = same-origin only |
| `auth_tokens` | `TOISE_AUTH_TOKENS` | *(none — secret)* | (empty) | comma-separated bearer tokens; empty = auth disabled |
| `tls_cert_file` | `TOISE_TLS_CERT_FILE` | `--tls-cert-file` | (empty) | PEM certificate; with the key, serves HTTP + OTLP over TLS |
| `tls_key_file` | `TOISE_TLS_KEY_FILE` | `--tls-key-file` | (empty) | PEM private key (pairs with the cert) |

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

## Authentication & TLS

Auth and TLS are **off by default** (the trusted-network posture). Enable them for a
production deployment without a fronting proxy (ADR 0024, revising ADR 0014):

- **Bearer tokens** guard the *data* surfaces — GraphQL, MCP, the debug UI, and OTLP
  ingest. Set `TOISE_AUTH_TOKENS` to one or more comma-separated tokens; clients send
  `Authorization: Bearer <token>` (HTTP header or gRPC metadata). **Tokens are
  secrets**: source them from the environment, never a flag. `/healthz`, `/readyz`,
  and `/metrics` stay public so probes and the scraper need no token.
- **TLS**: set `tls_cert_file` and `tls_key_file` to serve the HTTP and OTLP
  listeners over TLS.

```sh
TOISE_AUTH_TOKENS="$(cat /run/secrets/toise-token)" \
  toise-server --production \
    --tls-cert-file /etc/toise/tls.crt --tls-key-file /etc/toise/tls.key \
    --listen 0.0.0.0:8443 --otlp-listen 0.0.0.0:4317
```

> A valid token grants full access (read + ingest); fine-grained scopes and mTLS are
> future work. Producer identity for liveness stays keyed on the Resource
> `service.instance.id` (ADR 0019), independent of the auth token.

## Hardening for production

Three developer conveniences are on by default and should be locked down when the
server is reachable beyond a trusted proxy: **GraphQL introspection**, the
**playground** (`/playground`), and the **debug UI** (`/`). Turn them off
individually (`--graphql-introspection=false`, `--playground=false`,
`--debug-ui=false`) or all at once:

```sh
toise-server --production
```

`--production` is a **lockdown**: it forces all three off and **wins over the
individual toggles** (so "be safe" can't be silently re-opened). For fine-grained
control — say, keep the debug UI but drop introspection — set the toggles **without**
`--production`.

Cross-origin browser access (WebSocket subscriptions and CORS) is **same-origin
only** unless you allowlist origins explicitly:

```sh
toise-server --production --allowed-origins "https://graph.example.com"
```

## Operational endpoints

On the HTTP `listen` address, the server exposes probes for orchestrators and
uptime monitors:

| Path | Meaning |
| --- | --- |
| `/healthz` | **Liveness** — `200 ok` while the HTTP server is serving. |
| `/readyz` | **Readiness** — `200 ready` when the event store is reachable; `503` with the reason otherwise. |
| `/metrics` | **Prometheus** — Toise internals plus the standard Go runtime/process metrics. |

Wire the probes directly instead of probing a UI page. For structured logs into a
log backend, set `log_format: json` (and `log_level` as needed).

`/metrics` exposes (Toise-specific, sampled at scrape time): `toise_build_info`,
`toise_entities` (+ `toise_entities_by_type{type}`), `toise_relations`,
`toise_events_total` (events appended to the log), `toise_store_disk_bytes`, and
`toise_events_pruned_total` / `toise_bytes_pruned_total` (retention pruning) —
enough to build a Grafana dashboard of the graph's size and the store's growth.

## Retention — bounding on-disk growth

Two mechanisms keep the Pebble event log from growing without bound, both run on
the `retention_compaction_interval` cadence (default 1h):

- **Heartbeat coalescing** (always on) collapses runs of `entity.unchanged`
  heartbeats, keeping their first and last record.
- **Age pruning** (`retention_max_age`, off by default / `0` = unlimited) drops
  events recorded before `now − retention_max_age`, **except the latest event of
  every still-live entity and relation** — so a restart replays the same
  current-state graph. With it set (e.g. `720h` for 30 days), on-disk size
  stabilizes for a steady graph; the prune counters above track what was removed.

Pruning is by **`recorded_at`** (storage age), not `event_time`, so a retroactively
recorded old fact is not pruned the instant it lands.

## Fast restart — projection snapshots

On start, the in-memory projection is rebuilt by replaying the event log. With
`snapshot_interval` set (default `5m`), the server periodically writes a **projection
snapshot** into the store; on the next start it loads the snapshot and replays only
the events recorded **since** it — so **restart time is bounded by snapshot age, not
by total history** (#49). A final snapshot is also written at graceful shutdown. The
snapshot lives inside the Pebble store, so a backup includes it. The metrics
`toise_snapshot_seq` and `toise_snapshots_written_total` track it. Set `0` to disable
(full replay on start) — note the snapshot also carries the liveness bookkeeping
(producer references and their deadlines), so with sweeping on and snapshots off,
entities of producers that die while the server is down are never expired; the
server warns about that combination at startup.

> **`asKnownAt` over pruned windows.** Pruning removes historical events, so an
> `asKnownAt` audit query (or an `entityHistory` window) reaching **before the
> retention horizon** returns a *truncated* view: only the retained tail (the
> latest state per live entity) plus events inside the window survive. Keep
> `retention_max_age` longer than the deepest audit you need.

## Multi-tenancy

One Toise instance can serve multiple tenants with **fully isolated graphs** — a
query scoped to tenant A never sees tenant B's entities or relations (ADR 0025).
Each tenant gets its own store + projection + change engine under
`<data_dir>/<tenant>/`.

**Resolving the tenant** (generic and vendor-neutral, in order):

1. The **`X-Scope-OrgID`** request metadata — the de-facto standard used by
   Mimir/Loki/Tempo/VictoriaMetrics. It is an HTTP header on the query surfaces
   (`/graphql`, `/mcp`, the debug UI) and gRPC metadata on OTLP ingest.
2. A **`tenant.id`** resource attribute on the OTLP request. Set per `ResourceLogs`,
   it overrides the request metadata — so a single OTLP stream (e.g. one Collector
   exporter fanning in several clients) can carry several tenants at once.
3. Otherwise the **`default`** tenant.

The id is sanitized to a safe directory segment (alphanumerics, `-`, `_`, `.`;
bounded length; no path separators or `..`); an un-sanitizable value is rejected
(HTTP 400 / a gRPC error) rather than silently coerced.

```bash
# Query tenant "acme" over HTTP
curl -H 'X-Scope-OrgID: acme' http://127.0.0.1:8080/graphql -d '{"query":"{ entities { totalCount } }"}'
```

**Single-tenant deployments need no configuration.** With no tenant id ever
supplied, everything lives under `default` and behaves exactly as a single-graph
build. A pre-existing data directory (a Pebble store written directly under
`<data_dir>/` by an older build) is **migrated to `<data_dir>/default/`
automatically on first start** — no manual step, but take a backup first as with any
upgrade.

The liveness sweep, heartbeat coalescing, retention pruning, and snapshotting all
run **per tenant**. The `/metrics` endpoint reports the **sum across tenants**, so
existing metric names and dashboards are unchanged.

> **Authentication is not yet bound to a tenant.** A valid bearer token (see
> [Authentication & TLS](#authentication--tls)) may set any `X-Scope-OrgID`.
> Isolation therefore relies on the upstream OTel Collector authenticating each
> client and stamping its tenant; do not expose the ingest/query ports directly to
> untrusted multi-tenant clients. Per-token tenant binding is planned.
