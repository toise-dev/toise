# 20. Weighted multi-source identity (evidence + same_as + canonical view)

- Status: Proposed — Phase 2 draft (not implemented)
- Date: 2026-06-03

## Context

Toise's identity is **exact and observer-independent** (ADR 0018): a node is
matched byte-for-byte on its declared `otel.entity.id`, with no fuzzy or tolerant
matching (which ADR 0017 had and 0018 deliberately removed). This is the right
model when a thing has one stable, agreed identifier emitted by every observer —
`host.id`, `db.instance.id`, `service.instance.id`.

**Network topology discovery (Lot 5, SNMP) breaks that assumption.** The *same
physical device* surfaces through several sources of unequal reliability:

| Source | Identifier | Reliability |
|--------|------------|-------------|
| ENTITY-MIB chassis serial / snmpEngineID | `serial:…` | high — immutable hardware/engine id |
| LLDP/CDP chassis-id | `mac:…` / `name:…` | high — but **LLDP is often disabled** |
| sysName | `name:…` | medium — may be unset or non-unique |
| Management IP (the polled address) | `mgmt:<ip>` | low — mutable (DHCP, renumber, VIP) |
| Bridge FDB / ARP | `mac:…` | medium — contextual, endpoint may never be polled |

Two structural problems follow:

1. **No single source can be assumed.** LLDP — the cleanest identity *and* the only
   source of link-layer adjacency — is frequently off for security/policy reasons.
   A rigid single-id precedence (`serial` > `chassis-id` > `sysName` > `mgmt-ip`)
   helps, but the common fallback `mgmt:<ip>` is mutable: when the address changes,
   exact matching sees a delete + a new entity. That is exactly the
   network-derived-identity anti-pattern rejected for `db` (ADR 0018 / contract doc).
2. **The same device appears under different ids across sources**, and a device
   *seen but never polled* (only in another device's FDB/ARP/routing) carries only a
   MAC or an IP — it may never converge with its richer, polled identity.

The instinct — *"information arrives by different means; weight each source's
reliability"* — is how serious discovery engines (NetBox/Nautobot, commercial NMS)
work. But putting **probabilistic, threshold-based merging into Toise's matching
core** would re-open precisely what ADR 0018 closed: non-deterministic identity
(a function of accumulated evidence, thresholds, and arrival order), merges that
are hard to explain to an LLM, and a loss of the exact/auditable/append-only
property that is Toise's value proposition. We want the robustness of weighting
**without** the fuzzy core.

## Decision (proposed)

Separate two concerns that are easily conflated:

- **Identity** — what Toise *stores and matches*. Stays **exact** (ADR 0018,
  unchanged). Each source emits its own exact-id node; no node is ever silently
  merged or destroyed.
- **Resolution** — the belief that *"these observations are the same device."*
  This is inherently weighted, and it is expressed **as data, non-destructively**,
  not as fuzzy storage matching.

Concretely:

1. **Every source emits what it observes as an exact-id node** (`serial:…`,
   `mac:…`, `name:…`, `mgmt:…`). Information is never thrown away; a device known
   through three sources is three nodes until evidence links them.
2. **Belief is a first-class relation.** A `same_as` (alias) edge carries a
   `confidence` scalar (0–1) plus provenance — `basis` (e.g. `ifPhysAddress`,
   `lldp_chassis`, `serial_match`) and the observing producer. Reliability tiers
   map to confidence bands (serial/engine-id high, LLDP chassis-id high, sysName
   medium, mgmt-ip low, FDB/ARP MAC medium-contextual).
3. **The producer asserts the links it can justify.** Having polled `name:sw1`
   and read its interface MAC table, the agent emits
   `same_as(name:sw1, mac:00:…, confidence≈0.95, basis=ifPhysAddress)`. It does
   **not** pre-merge; it states evidence.
4. **The canonical (logical-device) view is derived at read time.** Connected
   components over `same_as` edges above a configurable threshold collapse to one
   logical device; the underlying exact nodes and the conflicting/low-confidence
   evidence remain visible. This mirrors Toise's existing "read model derived from
   the event log" shape — the canonical view is a projection, not a stored merge.
5. **Thresholds and overrides live at the read/config boundary**, never baked into
   storage. An operator (or LLM) can see confidence + provenance and override.

This reuses Toise's existing primitives: a new relation type plus a scalar
attribute. It does **not** require fuzzy storage matching, tolerant identity, or a
stateful merge engine.

## Consequences

- **Tout-terrain by construction.** Robust to any source mix, including no-LLDP:
  each source contributes weighted evidence; resolution degrades gracefully rather
  than depending on one signal.
- **ADR 0018 preserved.** Storage and matching stay exact, deterministic, and
  auditable; the bi-temporal append-only log is intact. Because resolution is
  data, it is **reversible** — confidence can be revised, links retracted, without
  rewriting identity. No order-dependent destructive merge.
- **LLM-legible.** The model can answer "why are these one device?" by reading the
  `same_as` edges and their `basis`/`confidence`, instead of trusting an opaque
  merge.
- **Costs / open questions (to resolve before acceptance):**
  - **Who owns confidence** — producer-asserted, Toise-computed heuristics, or
    both? Lean producer-asserted (it has the raw SNMP context); Toise may add
    derived links (e.g. exact MAC coincidence) later.
  - **Canonical-group instability** — components flapping as confidence wavers near
    the threshold; needs hysteresis or a stable-id assignment for the logical
    device.
  - **Contradiction handling** — `A=B` high, `B=C` high, but `A≠C` evidence exists;
    the read-side resolver must detect and surface conflicts, not silently chain.
  - **Surface semantics** — how `get_neighbors` / GraphQL / MCP present the
    canonical view vs the raw evidence nodes (default to canonical, expose raw?).
  - **Cost at scale** — component computation over `same_as` at FDB/ARP cardinality.
- **Explicitly out of scope for Phase 1 / Lot 5.** Lot 5 ships with **producer-side
  resolution and exact ids** (the agent emits one canonical id per device; Toise
  stays exact and unchanged). This ADR is the **additive forward path**: the
  evidence + `same_as` + canonical-view layer grafts on later **without** renouncing
  exact matching, so deciding "exact now, weighted later" incurs no architectural
  debt.

## Relationship to other decisions

- **Complements ADR 0018** (exact matching) — does not supersede it; identity stays
  exact, weighting lives in a separate resolution layer.
- **Complements ADR 0019** (per-producer reference counting) — liveness is still
  per-producer and per exact node; the canonical view is derived above it.
- **Motivated by** the Lot 5 SNMP topology contract discussion (successor scope to
  the senhub-agent producer contract, issue #185).
