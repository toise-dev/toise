# 25. Multi-tenancy — a stack per tenant, scoped by a generic tenant id

- Status: Accepted
- Date: 2026-06-08

## Context

Toise is fed by multi-tenant OTLP pipelines (an OTel Collector authenticates each
client and tags its stream). To serve isolated clients from one Toise instance, the
graph must be **scoped per tenant** — today it is a single global graph (#95). This
is the data-isolation counterpart to ADR 0024: auth says *who* connects; this says
*what graph* they see.

## Decision

**Scope every graph by a generic tenant id, and give each tenant its own isolated
stack — a separate Pebble store, projection, and change engine — under
`<data-dir>/<tenant>/`.** Physical separation (a distinct store per tenant) is
chosen over key-prefixing: it is far less invasive (the store, projection, and
engine are unchanged), and a bug cannot leak one tenant's data into another's
query — the stores never share a keyspace.

1. **Tenant id resolution** (generic, vendor-neutral, in order):
   1. the `X-Scope-OrgID` request metadata (the de-facto standard used by
      Mimir/Loki/Tempo/VictoriaMetrics) — HTTP header on the query surfaces, gRPC
      metadata on ingest;
   2. a `tenant.id` resource attribute on the OTLP request;
   3. otherwise the `default` tenant.
   The id is **sanitized** to a safe directory segment (lowercase alphanumerics,
   `-`, `_`, `.`, bounded length; no path separators or `..`) so it can name a
   store directory without traversal risk. An un-sanitizable id is rejected.

2. **A registry of lazy stacks** (`internal/registry`). The server holds
   `tenant -> stack`; a tenant's stack (store + projection + engine) is opened on
   first use and reused. Ingest resolves the tenant per `ResourceLogs` (gRPC
   metadata, overridable by the `tenant.id` resource attribute) and routes to that
   tenant's engine. The query surfaces (GraphQL, MCP, debug UI) are routed at the
   HTTP boundary: a per-tenant router builds one handler per tenant, bound to that
   tenant's stack, dispatched by the `X-Scope-OrgID` header — so those handlers stay
   tenant-agnostic and unchanged. The liveness sweep, compaction, and snapshotting
   iterate the open stacks; `/metrics` reports the sum across them (preserving the
   single-tenant metric shapes).

3. **Single-tenant by default.** No tenant supplied ⇒ everything lives under
   `default`. A self-hosted/OSS deployment that never sets a tenant id behaves
   exactly as before (one `default` store).

## Consequences

- **Hard isolation**: a query or token scoped to tenant A can never observe tenant
  B — the stores are different Pebble instances. This is the strongest, simplest
  guarantee.
- **Cost**: one Pebble instance (LSM, WAL) per active tenant. Fine for a bounded set
  of tenants; a key-prefixed single store would scale to very many tenants but is
  deferred (it would need careful, leak-proof prefixing).
- The store/projection/engine packages and the GraphQL/MCP/debug-UI handlers are
  **unchanged** — multi-tenancy is a composition concern handled at the server
  boundary: the `internal/tenant` package (resolution + sanitization), the
  `internal/registry` package (the stack registry + migration), and a per-tenant
  HTTP router plus the routed ingest receiver in `cmd/toise-server`.
- The on-disk layout changes: state moves from `<data-dir>/` to
  `<data-dir>/<tenant>/`. A pre-existing single-tenant data-dir (a Pebble store
  written directly under `<data-dir>/`) is **migrated automatically on first start**
  by relocating it under `<data-dir>/default/`.
- **Auth is not yet bound to a tenant.** A valid bearer token (ADR 0024) may set any
  `X-Scope-OrgID`; isolation relies on the upstream OTel Collector authenticating
  each client and stamping the tenant. Per-token tenant binding is future work.
- Implemented incrementally: (1) the `internal/tenant` package (resolution +
  sanitization, #100), then (2) the registry and the ingest/query wiring (this PR).
