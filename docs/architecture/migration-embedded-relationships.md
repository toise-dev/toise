# Migration plan — embedded relationships & a facts-only engine

Implements the decision in [ADR 0022](./adr/0022-engine-stores-facts-only.md) (which
resolves issue #65). The engine stores only asserted facts as OTel entity-events;
relationships move from the separate `entity.relation.*` extension to the spec's
**embedded** `entity.relationships`, attribute-bearing concerns become **entities**,
provenance moves to the **observer scope**, and edge attributes are retired. The
pivot is **staged and deliberate** — the spec (PR #4836) is approved-not-merged and
the OTel libraries we pin (ADR 0015) do not emit embedded relationships yet.

## Current → target

| Aspect | Today | Target (ADR 0022) |
| --- | --- | --- |
| Relations on the wire | separate `entity.relation.*` log events (from/to, state/delete, edge attrs), strict purity | **embedded** `entity.relationships` array on entity-state events (bare `{type, target.type, target.id}`) |
| Edge attributes | `local_port`/`remote_port`, `source`, `metric`, `preferred` on edges | modeled as **entities** (ports → `network.interface`; `metric` → `network.route`; `preferred` → `network.address`) or as **observer scope** (`source`/provenance) |
| Removal | explicit `relation_delete` + cascade | source re-emits state **without** the descriptor (diff) + cascade/interval (liveness unchanged) |
| Provenance (`source`) | edge attribute | OTLP **instrumentation scope** (observer); not stored as an edge fact |
| Internal model | first-class relation events in the log + projection adjacency | **unchanged** — the ingest boundary translates embedded descriptors → internal relation events |
| `entity.relation.*` extension | the contract | **deprecated** transitional compat shim → removed once producers migrate |

## What stays unchanged

Event log, projection, the **change taxonomy**, **bi-temporality**, liveness
(interval / cascade / per-producer ref-counting), exact identity (ADR 0018), and the
GraphQL / MCP read surfaces. Entity identity work (e.g. `network.device.id`
precedence `serial:<PEN>`/…) stays — it is fact.

## The work, in phases

### Phase A — additive ingestion of embedded relationships (safe, no breakage)
- Ingest boundary parses `entity.relationships` on entity-state events → internal
  relation observations (`from` = the emitting entity, `to` = the descriptor target).
- **Keep** accepting `entity.relation.*` unchanged → no producer breaks.
- **Removal by diff:** track the relationship set each entity (per source) asserts; on
  re-emit, diff → `relation.removed`. Reuse the attribute-diff machinery; scope the
  diff to relations the source owns (a relation `A→B` is owned by `A`).
- Conformance fixture: **add** embedded-relationship examples (alongside the extension
  ones for now).
- Outcome: Toise consumes standard OTel embedded relationships from any conformant
  producer, additively and without breaking anything.

### Phase B — topology as entities (contract change; coordinate with the producer)
- Ports become `network.interface` entities (identity `{network.device.id,
  interface.name}`); `has_interface` device→interface; a new **bare** `connected_to`
  interface↔interface; device-level `adjacent_to` → **derived at read** (a surcouche)
  or dropped.
- `metric` → `network.route` attribute; `preferred` → `network.address` attribute;
  `source`/provenance → instrumentation scope.
- Registry: add `connected_to`; retire edge attributes on `adjacent_to`/`routes_via`.
- **Rewrite** the conformance fixture to the embedded + entities model (no edge
  attributes).
- **Resync the senhub-agent contract** (Lot 5): emit ports + `connected_to` + embedded
  relationships, not attributed edges. Cross-repo; coordinated with the producer (not
  unilateral).

### Phase C — retire the extension
- Mark `entity.relation.*` deprecated in the docs; once producers emit embedded,
  remove the extension ingestion + strict-purity routing and the edge-attribute paths.

## Sequencing & gating
- **Phase A is safe to start now** (purely additive) — but end-to-end testing against a
  real OTel SDK producer waits on the pinned libs shipping `entity.relationships`
  (spec approved-not-merged). Toise can implement + unit/conformance-test the ingest
  ahead of that.
- **Phases B/C** require producer coordination and the spec merge; do **not** rush them
  ahead of lib support.

## Risks & open design points
- **Removal-by-diff bookkeeping:** the engine must know which relations a source
  asserted to detect removals on re-emit. Define the scoping precisely (source-owned
  edges only); out-of-order targets still use the reconciliation buffer.
- **Routing relaxation:** embedded relationships ride on entity-state events that *do*
  carry `otel.entity.*`. The boundary must process a node **and** its embedded edges
  from one record — the strict-purity routing rule (extension-only) relaxes for entity
  events.
- **Cardinality:** ports-as-entities raise entity count (a 48-port switch). Acceptable
  for a graph; re-check liveness/heartbeat load at scale.
- **Derived device-adjacency:** computing `adjacent_to` from `connected_to` is a
  surcouche (ADR 0022). Decide whether the engine exposes a convenience derived view or
  leaves it to consumers.

## Derived issues (to create)
1. **[Phase A]** Ingest embedded `entity.relationships` (additive) + per-source removal diff.
2. **[Phase A]** Conformance fixture: add embedded-relationship examples.
3. **[Phase B]** Topology as entities: ports → `network.interface`, `has_interface`, bare `connected_to`; `metric`→route, `preferred`→address; provenance→scope.
4. **[Phase B]** Rewrite the conformance fixture to embedded + entities (drop edge attributes).
5. **[Phase B, producer lane]** senhub-agent contract resync (Lot 5) — cross-repo coordination.
6. **[Phase C]** Deprecate then remove the `entity.relation.*` extension + strict-purity routing.
7. Close #65 (decided by ADR 0022); track implementation via the issues above.
