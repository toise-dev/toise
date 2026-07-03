# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

<!-- Add new changes here under Added / Changed / Deprecated / Removed / Fixed / Security as the project evolves. -->

## [0.9.1] - 2026-07-03

**Correctness and hardening from a full engineering review.** A patch release that
closes a silent time-travel data bug, an audit-attribution bug, and a cluster of
durability, tenant-lifecycle, and security residuals, and completes the `same_as`
identity-belief path end to end. Every change is additive and backward compatible:
existing producers, consumers, and deployments keep working unchanged; no wire-contract
break, no data migration. The producer SDK moves to `pkg/emit/v0.4.0`.

### Added

- **`same_as` confidence and basis, end to end** (SDK `pkg/emit/v0.4.0`). A producer
  can now assert an identity belief — "these two nodes are the same real thing,
  confidence 0.95, basis `ifPhysAddress`" — via `Relationship.Confidence`/`.Basis` on
  the SDK and `confidence`/`basis` on the `same_as` descriptor at ingest. The read-time
  canonical overlay (ADR 0020) collapses those beliefs; until now it had no way to be
  fed and was inert. Belief attributes ride only on `same_as` edges; other embedded
  edges stay attribute-free. The conformance kit advises on a `same_as` edge whose
  confidence is missing or out of `[0,1]`.

### Fixed

- **As-of queries silently dropped live entities past the retention horizon.** After
  pruning, an as-of fold for an instant between the retention horizon and now could omit
  a still-live entity whose only surviving event was a recent heartbeat — returning a
  graph missing real infrastructure, with no error, to LLM and GraphQL callers alike.
  Retention now re-materializes a baseline event at the horizon for such entities
  (self-cleaning, no unbounded growth). ADR 0013 amended.
- **Operator writes were audited under the wrong tenant.** Every `annotate_entity` was
  recorded as tenant `default` (and a derive-only scoped token's write was misattributed)
  because the resolved tenant was never stamped into the request context. Fixed at the
  tenant router, so the audit trail attributes every write to the tenant it targeted.
- **A gapped or overlapping shipped log was replayed silently.** Log restore now
  validates segment contiguity and hard-errors on a gap or overlap instead of rebuilding
  a wrong graph; a marshal error on ship is surfaced, and the cursor cache is invalidated
  on a failed put.
- **Tenant lifecycle.** `MaxTenants` is enforced atomically under concurrent first-use
  (no over-mint past the cap), and `delete-tenant` refuses to run against a live server
  (it takes the pebble lock) instead of removing an open store's files.
- **A mass expiry no longer stalls ingestion.** When a producer dies and its whole
  subtree lapses at once, the sweep commits every expiry in one durable batch (one
  fsync) instead of one per entity and edge.
- **The conformance kit rejected the AnyValue `entity.description` Toise itself emits**
  since 0.9.0, and a producer-guide snippet told producers to retry a permanent
  `PartialError`. Both corrected.

### Security

- **OIDC/JWT role claim.** A configured-but-absent, empty, or unrecognized role claim is
  now a hard reject instead of a silent grant of the full role.
- **`ingest_mtls_only`.** A configuration that would leave the read surface open (no read
  authenticator, or `derive-only` tenancy a client certificate cannot carry) is refused
  at startup rather than silently serving reads unauthenticated.

## [0.9.0] - 2026-07-01

**Strict OpenTelemetry entity-events (1.58.0) alignment and read-surface security.**
0.9.0 promotes the 0.9.0-beta line to stable and adds an out-of-order relation fix and
a Go toolchain security bump. Every change is additive and backward compatible: existing
producers, consumers, and deployments keep working unchanged, and the zero-config single
binary is untouched. No wire-contract break, no data migration.

### Added

- **Full AnyValue fidelity for `entity.description`** (#259). Descriptions carrying
  arrays and nested maps are ingested faithfully end to end (ingest → store →
  projection → GraphQL/MCP) instead of dropping the non-scalar parts; composite values
  render as compact JSON tagged `array` / `kvlist`. Identity stays scalar by contract
  (ADR 0018). ADR 0004 amended.
- **`entity.delete.reason`** (#260). A producer's motive on a delete (open enum —
  `terminated`, `expired`, `evicted`, … — never validated against a closed set) is
  captured, persisted, and exposed on MCP `recent_changes` / `graph_diff` and GraphQL
  `ChangeEvent.deleteReason`.
- **Decoupled ingest and read authentication** (#262). New opt-in `ingest_mtls_only`
  (`TOISE_INGEST_MTLS_ONLY`, requires `tls_client_ca_file`): OTLP ingest is
  authenticated by mutual TLS alone — no bearer — while GraphQL/MCP keep requiring their
  per-client scoped tokens (role read / full, individually revocable) or OIDC. Default
  off; the bearer-on-ingest posture is unchanged. ADR 0028 amended.
- **Producer SDK `pkg/emit` v0.3.0.** `Entity.RichAttributes` (`map[string]any`) emits
  the full AnyValue (arrays, nested maps); `Entity.DeleteReason` emits
  `entity.delete.reason`. Additive: scalar-only producers are byte-for-byte unchanged
  and the published conformance fixture still holds.

### Changed

- **`entity.report.interval == 0` (or absent) = no cadence** (#261), locked by a
  conformance test: such an entity is only ever removed by an explicit `entity.delete`,
  never expired by the liveness sweep.
- **ADR 0031 ratified** — 1.0 stability is decoupled from the upstream entity-events
  spec status (#257); the pre-1.0 versioning message aligned with the pinned contracts
  (#258).

### Fixed

- **Relation buffer no longer drops a parked edge whose target reappears periodically**
  (#269). An out-of-order edge (e.g. `runs_on -> host` where the host heartbeats on a
  slower cadence than the buffer TTL) is now held for at least one source re-emit cycle,
  so the endpoint's next heartbeat attaches it instead of the sweep dropping it. The
  hold auto-scales from the source's `entity.report.interval`; edges without a reported
  interval keep the previous behaviour.

### Security

- **Go toolchain bumped to 1.26.4** (#271), resolving three reachable standard-library
  advisories flagged by `govulncheck`: GO-2026-5038 (quadratic `mime.WordDecoder`,
  reachable via the S3 log sink), GO-2026-5037 (inefficient `crypto/x509` hostname
  parsing, reachable via the S3 sink and ingest), and one further stdlib advisory. No
  application code changed.

## [0.8.0] - 2026-06-22

**The SaaS-readiness release.** 0.8.0 lands the two pillars a multi-tenant,
externally-exposed Toise needs — **access security** and **resilience/HA** — plus
a **multi-source identity** overlay and a major **attribute-enrichment** pass.
Everything is **additive and opt-in**: the zero-config single-binary path is
unchanged (and now guarded by a CI smoke test). **Not a wire-contract break, no
data migration** — see the
[0.7 → 0.8 migration guide](docs/migration/0.7-to-0.8.md).

### Added

- **Resilience & HA (ADR 0029).** Scheduled online backups (`backup_dir`);
  continuous event-log shipping to a directory or an **S3-compatible store**
  (`log_shipping_*` — AWS S3 / MinIO / Ceph / R2); `restore-log` to rebuild a data
  dir from shipped segments (a directory or S3); read HA via N stateless replicas;
  per-tenant scaling by a per-node cap (`max_tenants`) plus horizontal sharding,
  with a `toise_tenants_open` gauge.
- **Multi-source identity (ADR 0020).** Producer-asserted `same_as` belief edges
  (confidence + basis); a read-time **canonical view** on `get_entity` above a
  configurable confidence threshold; alias-aware `impact_of` and `find_path`.
- **Attribute enrichment.** A cross-cutting governance vocabulary (ownership,
  criticality, location, lifecycle) advertised on `describe_schema`; attribute
  filtering on the GraphQL `entities` query (parity with `find_entities`); pinned
  descriptive vocabularies for `host` / `network.device` / `network.interface` /
  `compute.vm` / `service.listener` and the remote-probe entities (AT8–AT13).
- **Deployment & operability.** ADRs 0028 / 0029 / 0030 ratified; a consolidated
  "Deployment tiers & SaaS operations" guide; a tier-0 zero-config CI smoke test.

### Security

- **Access security for multi-tenant SaaS (ADR 0028).** `derive-only` tenant trust
  mode (anti-spoofing — the tenant is derived from the token, the client header is
  ignored); bearer tokens **hashed at rest** (SHA-256); **per-tenant RBAC**
  (role-scoped tenant tokens); **OIDC/JWT** verification on the read surfaces;
  optional **mTLS** (client-cert auth) on OTLP ingest; an append-only **audit log**
  for operator writes. All off by default; absence never degrades the zero-config
  path (ADR 0030).

### Changed

- The network-interface state key is canonicalized on `oper_state` (the engine's
  recognized spelling); `listens_on` is documented as a **bare edge** — the port
  lives on the `service.listener` entity.
- `restore-log --from` is now optional: with no `--from` it restores from the
  configured shipping target (S3 or `log_shipping_dir`).

### Fixed

- `go install …/cmd/toise-server@latest` works again (require the published
  `pkg/emit` tag instead of a local `replace`).
- The `/viz` graph no longer truncates at the 200-item page cap.
- The cold subcommands (`checkpoint`, `restore-log`, …) no longer error when
  shipping environment variables are present.
- A wall-clock-racy registry boot-grace test now uses an injectable clock.

## [0.7.0] - 2026-06-15

**The integration release.** 0.7.0 widens what an integrator can do with Toise:
an AI assistant gets pinnable context and ready-made workflows, operators can
annotate the graph, a dashboard can hold a token that can never write, and a
producer in any language can prove it is on-spec before it ships. It also lands
the identity hardening (resurrection, connection topology) and the audit P1/P2
lot. **Not a wire-contract break**, no data migration; one MCP argument was
renamed — see the
[0.6 → 0.7 migration guide](docs/migration/0.6-to-0.7.md).

### Added

- **Operator annotations** — `annotate_entity` (MCP) and the **first GraphQL
  mutation** `annotateEntity` attach free-form key/value notes (owner, runbook,
  ticket) to an entity as an **overlay**: kept in a per-tenant sidecar, surfaced
  on `get_entity` and `Entity.annotations`, never mixed into producer truth or
  the event log, never replayed. Merge semantics; an empty value removes a key.
- **MCP resources and prompts.** Resources expose pinnable context under
  `toise://` — `toise://schema` (the live graph schema as JSON), `toise://guide`
  (a markdown orientation), and the template `toise://entity/{id}`. Prompts ship
  ready-made operator workflows: `investigate_incident`, `blast_radius`,
  `explain_entity`, `whats_changed`.
- **Token roles.** Bearer tokens can be **read-only** (`TOISE_READ_TOKENS`, the
  query surfaces only — a dashboard or assistant that must never write) or
  **ingest-only** (`TOISE_INGEST_TOKENS`, OTLP only — a producer that must never
  read), alongside full tokens (`TOISE_AUTH_TOKENS`, both). A read-only token is
  refused on `annotate_entity`.
- **Verbosity tiers.** `find_entities`, `get_entity` and `get_neighbors` take an
  optional `verbosity`: `compact` returns just id/type/label (cheap to scan a
  large set), `full` (default) adds identity and attributes.
- **`toise-conformance` CLI** — validate a producer's OTLP entity-event output
  against the wire contract **without a running Toise**, in any language: pipe
  it the `ExportLogsServiceRequest` bytes (protobuf or JSON). Exit 0 conformant,
  1 on rejections (`-strict` also fails on advisories). Plus a **producer
  directory** documenting the SDK, the example producers, and external ones.
- **API stability policy** (`docs/api-stability`) and a golden contract test
  that pins the entire MCP surface — tools (name + input/output fields),
  resources, and prompts — so any change is deliberate and fails the build until
  the golden is regenerated.
- **Identity-stable resurrection.** An entity re-asserted within a bounded grace
  window after deletion keeps its logical id instead of forking a new one.
- **Connection-topology endpoints** with read-time peer resolution.
- **New entity types** `compute.vm` and `container` in the built-in registry.
- **`get_neighbors` digest** (`total`/`truncated`) and unified traversal-depth
  naming across `get_neighbors`/`find_path`/`impact_of` (`max_depth`).
- **`toise-server delete-tenant` and `drop-snapshot`** subcommands; a
  `prune_horizon` metric; per-tool MCP query observability via a shared Observer
  seam.
- **Producer examples** (`examples/producer-{minimal,docker,uptime,systemd}`), a
  "Write a producer" guide, and contributor on-ramps.

### Changed

- **MCP `get_neighbors` renamed `depth` → `max_depth`**, matching `find_path`
  and `impact_of`. Breaking for clients that passed `depth`.
- **One streaming as-of fold** now backs both MCP and GraphQL time-travel reads
  (shared, horizon-checked, concurrency-bounded), replacing duplicated
  materialize-then-fold paths.
- **Graph traversal goes through the adjacency index** instead of scanning all
  relations; the embedded-relation buffer is indexed by waited-on endpoint hash.

### Fixed

- **`Sweep` returns its commit errors** so the liveness-sweep error metric is
  actually reachable.
- **A tenant whose store fails to open is quarantined at boot** instead of
  taking down the whole server.
- **Live entities are counted by membership** (phantom-delete: a deleted entity
  no longer skews the count).
- **The SDK validates `eventName` and rejects a sub-second `Interval`** before
  it reaches the wire.
- GraphQL `entities` ordering documented correctly; the `ChangeType` mapping is
  pinned. Tenant and log-level config validation tightened; store meta recovery
  hardened; the `:latest` image tag is gated on stable release tags.

## [0.6.0] - 2026-06-12

**The corrective release.** A second full audit ran against 0.5.0 the day it
shipped; this release closes its entire P0 lot in one pass — the SDK now
*tells you* when records are rejected, the conformance kit promises exactly
what it checks, backups can no longer back up nothing, reads no longer trip
over maintenance, the liveness memento protects default deployments, and the
project moves to standard Go versioning (v-prefixed tags, independently
versioned SDK module). No wire-contract change, no data migration; the
sharper behaviors are listed in the
[0.5 -> 0.6 migration guide](docs/migration/0.5-to-0.6.md).

### Changed

- **MCP `get_entity` argument renamed `id` -> `entity_id`**, aligning it with
  every other entity-taking tool (`get_neighbors`, `entity_history`,
  `telemetry_keys`, `impact_of`); the 0.5.0 recette flagged the odd one out.
  Breaking for MCP clients that called `get_entity` with `id`.
- MCP `recent_changes` no longer requires `window`: omitted, it defaults to
  the last hour.
- **The `toise-emit` SDK is its own Go module with independent versioning**
  (#160, ADR 0027). `github.com/toise-dev/toise/pkg/emit` now pulls only the
  OTel pdata types and gRPC into a producer's module graph — none of the
  server's storage or query stack — and is released on its own cadence with
  `pkg/emit/vX.Y.Z` tags (first: `pkg/emit/v0.1.0`), at which point
  `go get github.com/toise-dev/toise/pkg/emit@v0.1.0` resolves. The import
  path is unchanged.
- **Server release tags are v-prefixed from `v0.6.0`** (#160, ADR 0027): the
  old no-`v` convention made every release uninstallable as a Go module
  version. `0.1.0`–`0.5.0` are not retro-tagged (a tag push would re-trigger
  the release workflow and duplicate releases). Release assets and the GHCR
  image carry the v-prefixed tag verbatim; docs URLs keep the unprefixed
  `/docs/0.6.0` style.
- **The conformance kit's claim is rescoped and sharpened** (#159): passing
  `Check` means never rejected per record **for shape reasons** — type-registry
  membership is a separately enforced layer unless `accept_unknown_types` is
  set. `Check` now also flags empty attribute keys (a rejection in every mode)
  and returns a new *advisory* problem (`Problem.Advisory`) when a
  ResourceLogs carrying entity events has no usable `service.instance.id`
  (ADR 0019). Producer CI that fails on any returned problem will now fail on
  a missing instance id: set a stable one, or skip `Advisory` problems.
- **Snapshots are on by default** (`snapshot_interval: 5m`) and a final
  snapshot is written per tenant at graceful shutdown, so the liveness memento
  (#139/#150) actually protects default deployments. Restored liveness
  deadlines are floored to `now + interval`, preventing a spurious
  delete-storm-and-recreate after downtime longer than producers' heartbeat
  intervals; a truncated liveness section in a snapshot degrades to a warning
  instead of failing the tenant's boot. (#164)
- **`toise-server checkpoint` is strictly read-only** (#162): it refuses a
  missing data dir, an unmigrated legacy single-tenant layout, and a dir with
  no tenant stores — previously it minted a fresh empty store, "backed it up",
  and exited 0 — and it no longer mutates a valid source (no format-version
  stamp, no default tenant minted alongside real ones). It now resolves its
  data dir with the server's exact config precedence and gains
  `--config`/`TOISE_CONFIG` support.

### Added

- **`pkg/emit/wire` — the single in-repo spelling of the entity-events wire
  vocabulary** (#160): event names, attribute keys, relationship-descriptor
  keys, and the producer-identity resource attribute, stdlib-only, imported by
  the SDK, the conformance kit, and Toise's own ingest. The frozen fixture
  still pins the contract against the world; the shared constants pin the
  repo's two sides against each other, and the two previously untied
  constants (`entity.report.interval`, `service.instance.id`) gain end-to-end
  behavioral tests.

### Fixed

- **History reads no longer fail during maintenance** (#161): `entity_history`,
  `recent_changes`, `graph_diff`, every as-of fold, and their GraphQL
  equivalents could error with `pebble: not found` when a query overlapped
  heartbeat coalescing or retention pruning; dangling secondary-index entries
  (which deterministically mean coalesced/pruned history) are now skipped.
- **Pre-1970 event-time inputs are rejected at the parse boundary** (#165):
  the time index encodes unsigned nanoseconds, so an `as_of` of 1950 returned
  the entire current graph dressed up as ancient history and a pre-1970
  `graph_diff` `from` yielded a silently empty diff; both now return a clean
  error on MCP and GraphQL alike.
- **An unknown embedded `relationship.type` is rejected per record** (#163)
  via OTLP partial success, like every other contract violation, instead of
  failing the whole batch as retryable and poisoning the producer's export
  loop while its valid sibling records never persisted.
- **The SDK surfaces OTLP partial success** (#158): when Toise accepts an
  export but rejects records as contract violations, `State`/`Delete` now
  return a typed `emit.PartialError` (rejected count + the server's first
  rejection reason, detectable with `errors.As`) instead of a silent `nil`.

## [0.5.0] - 2026-06-11

**The time-travel and producer-SDK release.** 0.5.0 completes the audit-driven
P2 lane: the bi-temporal log becomes queryable in the past tense (`as_of` on
every read surface), failures become predictable (`impact_of` blast radius),
producers get a real SDK with a byte-pinned published contract, and the
remaining unbounded internals (tombstones, vocabulary coupling, subscription
backpressure, restart liveness) are closed. No wire-contract change, no data
migration; a handful of sharper behaviors are listed in the
[0.4 → 0.5 migration guide](docs/migration/0.4-to-0.5.md).

### Changed

- **Low-severity hardening sweep** (#144): boot logs any data-dir entry it
  skips as a non-tenant directory (an accidentally hidden store no longer
  vanishes silently); GraphQL `first` is clamped to 200 like the MCP tools;
  native TLS uses an explicit config — minimum TLS 1.2, certificate re-read
  per handshake so renewals apply without a restart; the store stamps an
  on-disk format version and refuses a newer one with an actionable error;
  the unused phase-1 `Snapshotter` stub is gone; the bi-temporal accessor is
  one shared `Event.Times()` across every read surface.
- **Projection memory is bounded by the live graph, not cumulative churn.**
  Soft-deleted entities used to keep their full payload in memory forever;
  on a long-lived instance with ephemeral entities (flapping veth interfaces),
  tombstones eventually dominated. The projection now keeps the most recent
  1024 tombstones readable by id (with `deleted: true`) and evicts older ones
  entirely — their history stays in the log via `entity_history`, and the
  `get_entity` not-found message says so. (#140)

### Added

- **The `toise-emit` SDK and conformance kit.** `pkg/emit` is the first public
  Go package: declare entities and relationships, call `State`/`Delete`, and
  the SDK builds the spec-correct OTLP payload — deterministically (sorted
  keys), so the checked-in `fixture_v1.bin` is the published contract: the SDK
  reproduces it byte for byte and Toise's ingest accepts it with zero
  rejections. `pkg/emit/conformance` validates any producer's output against
  the contract in its own CI, without a running Toise — output that passes is
  never rejected per-record. (#142)
- **Maintenance observability with a tenant label.** Every background pass
  (liveness sweep, heartbeat coalescing, retention pruning, snapshots) records
  `toise_maintenance_runs_total{op,outcome,tenant}` and
  `toise_maintenance_last_duration_seconds{op,tenant}` — a single tenant's
  failing or slowing maintenance is no longer hidden inside cross-tenant
  aggregates. The engine's subscriber contract (non-blocking, non-reentrant,
  runs on the commit path) is now documented on `Subscribe`. (#143)
- **Opt-in open producer vocabulary.** `accept_unknown_types` (off by default)
  lets entity and relation types outside the built-in registry through ingest
  and the store, as long as their SHAPE is sound — identity present,
  well-formed key-values. Accepted unknown types are counted
  (`toise_ingest_unknown_type_records_total`) and show as `registered: false`
  in `describe_type`. Identity hashing is type-prefixed, so unknown types are
  first-class identities with no merge ambiguity (ADR 0018/0020 unchanged) —
  and a new producer lot no longer requires a lockstep Toise release. (#141)
- **Liveness survives restarts.** The engine's liveness bookkeeping — producer
  references with their expiry deadlines (ADR 0019) and per-relation deadlines
  — now rides the projection snapshot and is restored at boot, with absolute
  deadlines so downtime counts against them: a producer that died WHILE the
  server was down is swept on the first tick after restart, instead of leaving
  zombie entities the backstop could never reap. Pre-existing snapshots (no
  liveness section) keep reading fine. (#139)
- **GraphQL subscriptions: server-side filters and an in-band gap signal.**
  `entityChanged` and `relationChanged` take a `ChangeFilter` (entity/relation
  type, change classification, structural-only), so a consumer watching one
  thing no longer receives everything. And a consumer that falls behind is
  told: events dropped under backpressure are counted and the next delivered
  event carries `dropped > 0` — re-query state and resume, never a silent gap.
  (#138)
- **`describe_type` MCP tool — the per-type zoom.** For an entity type: its
  registration, live count, the identifying and descriptive attribute keys
  actually observed (with usage counts and example values, bounded sampling),
  the relation types it participates in with directions and empirically
  observed peer types, and example labels. For a relation type: its observed
  endpoint-type shapes with counts, the structural flag, and the
  failure-propagation direction. One call answers "what does a
  network.interface look like HERE?". (#137)
- **`impact_of` MCP tool — the blast radius.** Propagate a hypothetical
  failure of one entity through the graph and get back everything it takes
  down, nearest first, grouped by type, with a one-line summary. Propagation
  follows each relation type's registered dependency direction (a host failing
  takes down what `runs_on` it and its interfaces; connectivity breaks both
  ways; unregistered types propagate conservatively both ways), transitively,
  under the usual budget contract — and accepts `as_of` to ask the question of
  a past graph. (#136)
- **As-of time travel on the read surfaces.** Every graph-reading MCP tool
  (`find_entities`, `get_entity`, `get_neighbors`, `find_path`,
  `telemetry_keys`, `describe_schema`) takes an `as_of` instant (RFC 3339), and
  the GraphQL `entity` / `entities` / `relations` queries take `asOf`: the
  answer is the graph **as it was then** (event-time reading), folded from the
  bi-temporal log. An as-of older than the retention horizon is refused with a
  clear error (those events are pruned — the store now persists the latest
  prune cutoff); the audit "as known at" reading stays on `entity_history`.
  (#135)

## [0.4.0] - 2026-06-10

**The correctness and LLM-querying release.** A full multi-dimensional audit of
the server (46 confirmed findings, every high/medium counter-verified against
the code) drove this release end to end: first the correctness lot that makes
"the log is the source of truth" actually hold under failure, then a product
lane that turns the MCP surface into a precise, budget-aware query layer —
three new tools, edge-aware traversal, and bounded results — plus tenant
security, an ingestion that is finally observable, and maintenance that no
longer stalls ingest. No wire-contract change for producers; a handful of
sharper behaviors are listed in the
[0.3 → 0.4 migration guide](docs/migration/0.3-to-0.4.md). Validated end to end
on a live staging deployment fed by a real agent before tagging.

### Fixed

- **Ingest integrity: the projection can no longer run ahead of the durable
  log.** Batched commits are a staged unit of work — events reach the in-memory
  graph and the subscribers only after the durable append succeeds, so a failed
  flush leaves no phantom state and a producer retry regenerates everything
  (previously a relation lost in a failed flush was never written again).
  Records violating the wire contract (unknown `entity.type`, malformed
  identity) are rejected **per record** via OTLP partial success instead of
  poisoning their whole batch, and removal of an already-cascaded relation is
  the no-op the contract promises instead of a poison pill that failed every
  subsequent export from that producer. (#108, #109, #110)
- **Restarts no longer corrupt the graph.** Projection snapshots omit
  soft-deleted entities (restoring no longer resurrects the dead), and replay
  rebuilds the identity/type indexes from update events — after retention
  pruning, an entity whose first surviving event was an update used to become
  unmatchable, minting permanent duplicates on its next observation. (#106, #107)
- **OTLP `Export` returns proper gRPC status codes**: `InvalidArgument` for
  permanent caller errors (invalid tenant ids, refused tenants),
  `Unavailable` for transient store failures — previously everything surfaced
  as `Unknown`, which spec-compliant exporters treat as non-retryable, silently
  dropping batches the design intends to be retried. (#111)
- **Lifecycle**: the sweep/compaction/snapshot loops are joined before the
  stores close (no more `panic: pebble: closed` when shutdown coincides with a
  maintenance tick); a post-startup OTLP receiver failure now exits the process
  instead of leaving a green `/readyz` over a dead ingest; and a deploy with
  connected streaming clients (MCP SSE, GraphQL WebSocket) exits clean instead
  of failing its shutdown grace. (#112, #130)
- A mis-typed `entity.report.interval` (e.g. a string) is surfaced on the
  dropped-keys path instead of silently disarming the liveness backstop; the
  out-of-order relation buffer is capped and swept; configurations that would
  silently not do what they say (half-set TLS, retention without a compaction
  interval, unknown log level) are rejected at startup. (#115)

### Added

- **Three new MCP tools.** `graph_diff` folds the change log between two
  instants into the net difference — created / deleted / changed, plus a
  first-class *transient* bucket for flapping entities and relations.
  `find_path` finds the shortest relation path between two entities
  (`reachable: false` is an answer, not an error). `telemetry_keys` derives the
  exact join keys that locate an entity's metrics and logs in observability
  backends — own and 1-hop-inherited OTel resource attributes, each with its
  Prometheus-style flattened label form and usage caveats. (#115)
- **Result budgets across the MCP timeline tools.** `recent_changes` and
  `entity_history` exclude heartbeats by default, accept `change_type` and
  `include_heartbeats` filters, bound their output with `limit`, and report a
  digest (`total`, `truncated`, `heartbeats_excluded`, per-type counts) so an
  LLM can narrow without paging blind. `get_neighbors` now tells *how* each
  entity was reached (`via_relation`, `direction`, `depth`). Every tool call
  runs under a 30-second budget, and store reads honor caller cancellation.
  (#115)
- **Ingest is observable**: hot-path Prometheus counters for export outcomes,
  per-record results (handled / ignored / rejected), dropped attribute values,
  tenant rejections, and authentication failures — "is ingest healthy?" now has
  an answer on `/metrics`. (#113)
- **Tenant security.** Bearer tokens can be bound to tenants
  (`TOISE_TENANT_TOKENS` takes `tenant:token` pairs): a scoped token is
  authorized only for its tenant, enforced on the HTTP surfaces (403) and on
  ingest per *resolved* tenant (`PermissionDenied`) — the per-`ResourceLogs`
  `tenant.id` override cannot bypass it. Runtime tenant creation is bounded
  (`tenant_auto_create`, `tenant_allowlist`, `max_tenants`), query surfaces can
  no longer create a tenant by reading it (unknown tenants are a 404), and
  startup warns loudly when a listener is exposed without auth or TLS.
  (#104, #115)
- **`toise-server checkpoint`** — a consistent, per-tenant cold-backup command
  (the operator-facing trigger `Store.Checkpoint` was documented to have), with
  a new Backups page in the user guide. (#115)
- ADR 0026 fixes the reconciliation policy for Resource-borne entities (OTel
  spec PR 5147) ahead of implementation: entity events stay authoritative for
  lifecycle, resource refs associate and may opt-in bootstrap presence. (#105)

### Changed

- **Store maintenance no longer stalls ingestion.** Heartbeat coalescing and
  retention pruning scan on a Pebble snapshot off the append mutex — each
  maintenance tick used to block that tenant's ingest for the duration of a
  full-log scan, growing with history. Pruning also stopped re-marshaling every
  pruned event just to count bytes. (#115)
- The `toise_entities_by_type` metric reports the 50 largest types and folds
  the tail into `other` (the label was producer-controlled and unbounded).
- Tenant stacks open outside the registry's global mutex (one tenant's Pebble
  open no longer blocks every other tenant's requests), with single-flight
  deduplication.
- Internal error details no longer leak to HTTP clients on tenant-resolution
  failures; `/readyz` names the failing tenant.

### Security

- Cross-tenant read/write/create via a client-chosen `X-Scope-OrgID` is closed
  when tenant-scoped tokens are configured; tenant minting is bounded; reading
  can never create. See *Added → Tenant security*. (#104)


## [0.3.0] - 2026-06-08

**The production-readiness and multi-tenancy release.** 0.3.0 turns the phase-1
backend into something deployable in a real, multi-tenant production posture:
native authentication and TLS, operational endpoints and structured logging,
bounded on-disk growth, fast restart, packaged release artifacts, and — the
headline — **per-tenant isolated graphs**. No wire-contract change to the OTLP
producer payload; the only behavioral change for existing deployments is the
on-disk layout (auto-migrated). See the
[0.2 → 0.3 migration guide](docs/migration/0.2-to-0.3.md).

### Added

- **Multi-tenancy: per-tenant isolated graphs.** One Toise instance can now serve
  multiple tenants with fully isolated graphs. Each tenant gets its own
  `{store, projection, change-engine}` stack under `<data-dir>/<tenant>/` (ADR 0025).
  The tenant id is generic and vendor-neutral — read from the `X-Scope-OrgID`
  request metadata (the Mimir/Loki/Tempo/VictoriaMetrics de-facto standard; HTTP
  header on queries, gRPC metadata on ingest) or a `tenant.id` resource attribute,
  falling back to `default`. Ingest routes per `ResourceLogs` (so one OTLP stream
  can carry several tenants); the GraphQL, MCP, and debug-UI surfaces are scoped by
  `X-Scope-OrgID`; the liveness sweep, compaction, and snapshotting run per tenant;
  `/metrics` reports the sum across tenants. A pre-existing single-tenant data
  directory is migrated to `<data-dir>/default/` automatically on first start, and a
  deployment that never sets a tenant id behaves exactly as before. (#95, #100, #101)
- **Native bearer-token authentication and TLS** on the data surfaces. Tokens are
  supplied via the environment only (`TOISE_AUTH_TOKENS`); the gRPC ingest and the
  HTTP query surfaces enforce them when set. TLS is enabled by pointing at a
  cert/key pair. Both are off by default — the trusted-network posture (ADR 0014) is
  preserved. The operational probes and the metrics scrape stay public. (ADR 0024;
  #43)
- **Operational endpoints and structured logging.** `/healthz` (liveness),
  `/readyz` (readiness — checks every tenant store), and a Prometheus `/metrics`
  endpoint sampled at scrape time (entities, relations, events, disk usage,
  retention/pruning and snapshot counters, build info). Logs are structured; the
  level is set with `--log-level`. (#44)
- **Retention pruning** to bound on-disk growth. With `retention_max_age` set, a
  compaction goroutine prunes events older than the horizon while preserving the
  current-state projection (the keep-set is the latest event per live entity).
  Heartbeat coalescing runs alongside it. (ADR 0013; #45)
- **Projection snapshots for fast restart, plus backup/restore.** With
  `snapshot_interval` set, the server periodically writes a projection snapshot into
  the store; on the next start it loads the snapshot and replays only the tail —
  restart time is bounded by snapshot age, not by total history. `Store.Checkpoint`
  produces a consistent, lock-free backup copy. (#49)
- **Packaged release artifacts.** Tag-triggered CI builds static binaries for
  linux/darwin/windows and a distroless OCI image (GHCR); a `Dockerfile` and a
  `deploy/` directory with systemd and docker-compose examples ship in-tree. (#47)
- **Versioned documentation site** at [toise.dev/docs](https://toise.dev/docs)
  (MkDocs Material, deployed per release with mike): user guide, configuration,
  operations, data model, querying, and migration guides. (#91)

### Changed

- **Production HTTP hardening.** A single `--production` flag (or
  `TOISE_PRODUCTION`) locks down the development surfaces at once — GraphQL
  introspection, the `/playground`, and the debug UI — and an `allowed_origins`
  allowlist gates browser WebSocket origins. Each lever is also individually
  configurable. (#48)
- **On-disk layout is now per tenant** (`<data-dir>/<tenant>/`). A pre-existing
  single-tenant data directory is migrated under `<data-dir>/default/`
  automatically on first start. Take a backup before upgrading, as with any
  store-format change. (#95)
- **`toise-probe` emits topology as first-class entities and `connected_to`
  relations** instead of the legacy fabric `adjacent_to`, aligning the bundled
  probe with the current topology model. (#90)

### Security

- Authentication (bearer tokens) and TLS are now available for the ingest and query
  surfaces, and `--production` removes the development affordances from a public
  deployment. Multi-tenant isolation is by `X-Scope-OrgID`; note that a valid token
  may still set any tenant id, so isolation relies on the upstream OTel Collector
  authenticating each client and stamping its tenant (per-token tenant binding is
  future work — see ADR 0025).

## [0.2.0] - 2026-06-08

**A breaking wire-contract release.** 0.2.0 realigns Toise onto the **merged**
OpenTelemetry entity-events specification (`specification/entities/entity-events.md`,
merged 2026-06-04) and removes the transitional relation extension. What changes is
the **wire contract producers emit**; stored event-log data and the GraphQL/MCP
query schemas are unaffected. Toise is pre-1.0/alpha, so this is a clean break with
no compatibility shim — update producers in lockstep. See the
[0.1 → 0.2 migration guide](docs/migration/0.1-to-0.2.md).

### Changed

- **Relationships are embedded-only.** Edges now ride **embedded** on the source
  entity's state event as an `entity.relationships` array (`{ relationship.type,
  entity.type, entity.id }` naming the target); removal is by absence. The engine,
  change taxonomy, and bi-temporality are unchanged — the ingest boundary still
  translates each descriptor into a first-class relation event (ADR 0022). (#69,
  #70, #71, #72, #74)
- **Ingest realigned onto the merged OTel entity-events spec.** Entity events are
  identified by the LogRecord **`EventName`** (`entity.state` / `entity.delete`),
  not an attribute; attribute keys drop the `otel.` prefix and rename
  (`entity.type`, `entity.id`, `entity.description`); the liveness interval is
  **`entity.report.interval` in seconds** (was `otel.entity.interval` in
  milliseconds — a unit fix); the relationship descriptor field is
  `relationship.type`; `entity.id` is typed `map<string,string>`. (#80)
- **Process identity follows the OTel semantic conventions** —
  `{ process.pid, process.creation.time }` so PID reuse across a restart is a new
  process, not a mutated one. (#62)

### Added

- **Layered configuration for `toise-server`** — built-in defaults < YAML file
  (`--config` / `TOISE_CONFIG`) < environment (`TOISE_*`) < flags. Unknown YAML keys
  are rejected; secrets are sourced from the environment only. The flag surface is
  unchanged. (ADR 0023; `docs/operations/configuration.md`;
  `examples/toise-server.yaml`) (#46)
- **GraphQL API reference** — schema, Relay pagination, the bi-temporal
  `eventTime`/`recordedAt`/`asKnownAt` model, worked example queries, and the
  guardrails. (`docs/reference/graphql.md`) (#85)
- **`connected_to` relation type and topology-as-entities** — ports as
  `network.interface` entities linked by `has_interface`, with bare `connected_to`
  adjacency, so edges stay attribute-free under the embedded model. (#71)
- **`graph-viz` example** — a live GraphQL-subscriptions client rendering the graph
  in real time. (#59)
- Architecture decisions: **ADR 0021** (human interfaces live at the edge, not the
  core), **ADR 0022** (the engine stores facts only), **ADR 0023** (layered
  configuration).

### Removed

- **The `entity.relation.*` relation extension** — separate relation LogRecords,
  edge attributes, and the strict-purity routing path are gone. Relationships are
  embedded-only (see *Changed*). (#74)

### Fixed

- **WebSocket subscriptions no longer hit the per-request timeout.** The GraphQL
  subscription upgrade is routed around `http.TimeoutHandler` (which cannot hijack
  the connection), so long-lived subscriptions work. (#57)

## [0.1.1] - 2026-06-02

First real-world validation against the **real senhub-agent** producer, which
surfaced and fixed a silent OTLP ingestion bug.

### Fixed

- **OTLP ingestion now accepts gzip-compressed exports.** The OTLP/gRPC receiver
  did not register the gzip decompressor, so gzip-compressed exports — the OTel
  SDK default, and what the senhub-agent reference producer ships — failed at the
  gRPC transport (`"Decompressor is not installed"`) *before reaching the handler*
  and were silently dropped (the OTel SDK swallows the export error), surfacing
  only as an empty graph. Found connecting the real senhub-agent to a running
  `toise-server`. (#32)

### Changed

- **Tooling:** the lint CI builds golangci-lint from source with the repository's
  Go toolchain (so it tracks the latest Go) and migrates to golangci-lint v2.
  (#34, #37)

## [0.1.0] - 2026-06-02

First tagged release: the phase-1 backend (M0–M8) plus the producer↔consumer
contract converged with the senhub-agent reference producer.

### Added

- **Data model & proto contract** — OTel-aligned entity/relation model with a
  stable logical entity id plus a 128-bit identity hash, typed attribute values,
  a type registry, and a protobuf wire contract generated with buf. (M1; ADR
  0004, 0005, 0006, 0015, 0017)
- **Event log store** — append-only, bi-temporal log on Pebble with secondary
  indexes (by entity, change type, event time), durable `Append`, crash
  recovery, and heartbeat-coalescing retention. (M2; ADR 0007, 0013)
- **Projection & change detection** — in-memory graph rebuilt from the log, with
  the nine-type change taxonomy, **exact identity matching** (immutable ids), and
  structural relation changes flagged high-priority. (M3; ADR 0008, 0018)
- **OTLP ingestion** — an OTLP/gRPC logs receiver that converts entity-event
  LogRecords into change-engine observations: standard `otel.entity.*` nodes and
  the vendor-neutral `entity.relation.*` edge extension. (M4; ADR 0009)
- **GraphQL API** — schema-first gqlgen API with rich descriptions, Relay cursor
  pagination, subscriptions, a complexity limit and per-request timeout, served
  at `/graphql` with a playground at `/playground`. (M5; ADR 0010)
- **MCP server** — a Model Context Protocol server (official Go SDK) exposing six
  typed tools (`find_entities`, `get_entity`, `get_neighbors`, `entity_history`,
  `recent_changes`, `describe_schema`) over stdio and Streamable HTTP at `/mcp`,
  with a sample Claude Desktop config. (M6; ADR 0011)
- **Debug UI** — a minimal, server-rendered HTML view over the same read model
  (dashboard, entity list, entity detail, recent changes) at `/`. (M7; ADR 0012)
- **Demo fixture** — `toise-demo` seeds the "a day in the life of web-server-1"
  24-hour scenario; `docs/demo/` documents the timeline and twelve LLM example
  prompts. (M8)
- **`toise-server`** — single binary wiring the store, projection, OTLP receiver,
  GraphQL, MCP, and debug UI together; loopback by default. Liveness/robustness
  flags: `--liveness-sweep-interval`, `--relation-buffer-ttl`.

### Added — producer↔consumer contract (senhub-agent #185)

- **Producer vocabulary** in the type registry: entities `service.instance`, `db`,
  `network.device`; relations `monitors`, `runs_on` (also `service.instance→host`),
  `routes_via`, `forwards_to`, `adjacent_to`.
- **Vendor-neutral relation extension `entity.relation.*`** with **strict purity**
  (relation records carry no `otel.entity.*`, discriminated by
  `entity.relation.event.type`), designed to map 1:1 onto the future OTel
  relationships standard.
- **Liveness backstops:** explicit `entity_delete`/`relation_delete` primary, plus
  an `otel.entity.interval` / `entity.relation.interval` TTL sweeper; edge liveness
  derived from endpoints (cascade); an out-of-order edge reconciliation buffer.
- **Per-producer reference counting** for entity liveness, keyed by the OTLP
  Resource `service.instance.id`, so multiple agents observing one entity no longer
  flap on a single producer's delete. (ADR 0019)
- **No silent loss at the boundary:** non-scalar attribute values are logged
  (`Warn`) rather than dropped silently; flat scalar maps are the producer contract.
- **Shared conformance fixture** (`internal/ingest/testdata/conformance/`): an
  OTLP/JSON batch ingested by a contract test, the executable interface between
  Toise and producers.

### Changed

- **Exact identity matching supersedes tolerant matching** (ADR 0018, superseding
  ADR 0017): identities are immutable, so a differing identity is a different
  entity. `entity.identity_changed` is retained in the taxonomy but no longer
  emitted by the engine.

### Security

- **No authentication in phase 1** (ADR 0014). All surfaces bind to loopback by
  default and are intended for trusted networks only; the WebSocket subscription
  endpoint enforces an origin check.

[Unreleased]: https://github.com/toise-dev/toise/compare/v0.9.1...HEAD
[0.9.1]: https://github.com/toise-dev/toise/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/toise-dev/toise/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/toise-dev/toise/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/toise-dev/toise/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/toise-dev/toise/compare/0.5.0...v0.6.0
[0.2.0]: https://github.com/toise-dev/toise/compare/0.1.1...0.2.0
[0.1.1]: https://github.com/toise-dev/toise/compare/0.1.0...0.1.1
[0.1.0]: https://github.com/toise-dev/toise/releases/tag/0.1.0
