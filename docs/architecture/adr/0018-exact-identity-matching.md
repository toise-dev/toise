# 18. Exact identity matching (supersedes ADR 0017's tolerant matching)

- Status: Accepted
- Date: 2026-06-01
- Supersedes: the tolerant-matching part of [ADR 0017](0017-entity-identity-and-stability.md)

## Context

ADR 0017 introduced **tolerant** identity matching: an observation differing from
a live entity in at most one identifying value (same key set) was treated as the
*same* entity that had "changed identity" (`entity.identity_changed`). The intent
was to keep a logical ID stable when an identifying attribute legitimately changes.

In practice that conflates two cases the data cannot distinguish — "same entity,
attribute changed" versus "a different, similar entity" — and so causes **silent
entity over-merges**: two databases on a host differing only by port, two NICs,
two jobs collapse into one. We hit this twice during phase 1 (a GraphQL test, then
the M8 demo's service listeners) and worked around it with composite keys.

The producer↔consumer contract review with senhub-agent (#185) made the call
explicit. The OpenTelemetry entity model treats an entity's **Id as immutable**;
under that model there is no need for fuzzy matching, and a single unique key is
perfectly valid. For an infrastructure graph that aims to be a **source of
truth**, a silent collision is a worse failure than losing a heuristic.

## Decision

Entity identity is matched **exactly** against the producer-declared identity (by
identity hash). There is no tolerance.

- `projection.MatchIdentity(type, identity)` returns an exact match or "not found".
  The `maxDiff` parameter and the tolerant scan are removed; relation endpoints
  already resolved by exact identity and are unchanged.
- The change engine no longer emits `entity.identity_changed`. An observation
  whose identity does not match exactly is a **different entity** (`entity.created`
  with a new logical ID); an exact match with differing attributes is an
  `attribute_updated` / `state_changed` as before.
- A **single-key identity is valid** (`host.id`, the agent key). The "≥2 values or
  composite key" rule from the contract is no longer required; a composite key
  (e.g. `db.instance.id`) remains a good convention by choice, not to dodge a
  fuzzy merge.
- The corollary for producers: **Ids must be immutable.** A *bare* reused value
  (a raw pid, a leased IP) is not an identity on its own — but it becomes a valid
  immutable identity when paired with a discriminator that is stable for the
  resource's lifetime. The OTel semconv `process` entity does exactly this: its
  identity is **`process.pid` + `process.creation.time`** (the creation time
  disambiguates PID reuse), so a real restart yields a new creation time — a
  genuinely new process (delete + create) — while a descriptive change at the same
  pid + creation time is an `attribute_updated`. Where no such lifetime-stable
  discriminator exists — e.g. a `db` keyed by `host:port`, which moves under
  DHCP/failover — the value stays descriptive and a stable source id is used instead.
  (The demo, accordingly, models a process by `process.pid` + `process.creation.time`,
  so a restart is a delete + create and a config reload is an `attribute_updated`.)
- `entity.identity_changed` is **retained in the taxonomy and the proto enum** for
  wire compatibility and so the projection can still **replay** historical (or
  producer-signalled) identity-change events; it is simply never produced by the
  engine.

## Consequences

- No silent entity collisions: distinct identities are always distinct entities.
- The change taxonomy still has nine types, but the engine now emits eight; the
  demo covers those eight.
- Producers carry the responsibility of stable, immutable Ids — which is the OTel
  model anyway, so this aligns Toise with the spec it tracks (ADR 0015) and eases
  the eventual migration.
- ADR 0017's dual logical-ID / identity-hash design and its immutable-Id principle
  stand; only the fuzzy matching is removed.

See also: ADR 0004 (data model), ADR 0006 (change taxonomy), ADR 0017 (superseded),
`docs/data-model/otel-mapping.md` and `senhub-agent-contract.md` (the contract).
