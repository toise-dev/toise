# 17. Entity identity and stability

- Status: Accepted
- Date: 2026-05-29

## Context

An entity's identifying attributes can evolve over time: a host is renamed, an
interface is reassigned an address, a device's serial is corrected. If we keyed
entities purely by a hash of their identifying attributes (see ADR 0004), such a
change would look like one entity vanishing and another appearing — destroying
the very traceability the event log exists to provide.

We must therefore distinguish two things that are easy to conflate: a **stable
handle** that follows an entity through its whole life, and a **content
fingerprint** of its current identifying attributes that necessarily changes
when those attributes do.

## Decision

Toise carries two distinct concepts. They must not be confused.

- **Logical entity ID** — the stable identifier of an entity across time, even
  as its identifying attributes evolve. It is what consumers reference. It is a
  surrogate, assigned by Toise on first sight of the entity, and remains
  attached for the entity's lifetime — including across `entity.identity_changed`
  events (see ADR 0006). We use a **ULID** (`github.com/oklog/ulid/v2`, pure Go):
  time-sortable, k-sortable, and collision-resistant. `event_id`s are ULIDs too.

- **Identity hash** — a deterministic hash (SHA-256, truncated to 128 bits, hex,
  prefixed by entity type, e.g. `host:1a2b...`) of the *current* set of
  identifying attributes. The set is canonicalized by sorting keys and encoding
  each value with a type tag, so that the string `"1"` and the integer `1`
  produce different hashes. The hash is used internally for fast lookup and for
  idempotent processing of incoming events. It changes when an identifying
  attribute changes; the logical entity ID does not.

Matching with tolerance — designed here, implemented by the Milestone 3
change-detection engine (see ADR 0008):

- On an incoming event, compute its identity hash and look for an exact match.
  If found, update that entity.
- If there is no exact match but the incoming identifying attributes
  **partially** match an existing entity (typically one identifying attribute
  changed while the others remain), treat it as the same entity: emit
  `entity.identity_changed`, keep the logical entity ID stable, and update the
  stored identity hash. This is a recoverable change that traceability requires
  us to retain, not the birth of a new entity.
- If there is no match at all, create a **new** logical entity with a fresh ULID.
- The partial-match rule — which identifying attributes, and how many, may
  differ before two records are still considered the same entity — is a tunable
  policy. Phase 1 uses a conservative default (documented in the change engine)
  and refines it in Milestone 3.

## Consequences

- Consumers get stable references that survive renames and re-addressing, so
  downstream views and links do not break on identity churn.
- History stays continuous across identity changes: a renamed host is the same
  logical entity before and after, with the rename recorded as an event.
- The matching logic lives in the change engine and carries a small risk of
  mis-merging two genuinely different entities. This is mitigated by the
  conservative default and by classifying `entity.identity_changed` as anomalous
  and high-priority (see ADR 0006), so a wrong merge surfaces rather than hides.
- The identity hash gives O(1) idempotent ingest: re-delivered observations of an
  unchanged entity resolve to the same entity without duplicate work.
