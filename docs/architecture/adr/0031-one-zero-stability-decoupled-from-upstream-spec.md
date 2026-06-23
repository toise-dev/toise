# 31. 1.0 stability — commit to our own contract while the upstream entity-events spec is still Development

- Status: Accepted
- Date: 2026-06-23
- Relates to: ADR 0015 (tracking the OTel entity-events spec), ADR 0027 (SDK
  module and versioning), ADR 0030 (deployment tiers), the
  [API stability policy](../../user-guide/docs/api-stability.md)

## Context

Toise is approaching 1.0. Per the API stability policy, 1.0 flips the three public
surfaces — the **producer wire contract** (OTLP entity events), the **MCP surface**,
and the **GraphQL schema** — from "a minor *may* break with deprecation" to strict
semantic versioning. The wire contract is the load-bearing one: it is what every
producer builds against, and it is pinned by a byte-exact conformance fixture
(`pkg/emit/conformance/fixture_v1.bin`).

That wire contract follows the OpenTelemetry **entity-events** specification. As of
spec release **1.58.0 (2026-06-22)** that specification has a dedicated normative
file (`specification/entities/entity-events.md`) — but its **Status is Development**:
normative MUST/SHOULD language, explicitly *not yet stable*, with no committed date
to reach Stable.

This creates the question this ADR answers: **can Toise promise 1.0-grade stability
on a wire contract that mirrors an upstream spec which is itself still moving?**

What de-risks it:

- Our contract is already **pinned and tested** (`fixture_v1.bin`), and our policy
  is **additive-only + deprecate-before-remove** within a release series.
- The recent upstream motion is **additive**: a gap analysis of 1.58.0 found Toise
  fully aligned on every load-bearing field, with only new *optional* fields
  (`entity.delete.reason`, `entity.report.interval == 0` semantics, AnyValue in
  `entity.description`) — none breaking.
- We already run a spec-tracking watch (issue #66 / ADR 0015).

The residual risk is a future **breaking** upstream change — a renamed key, a
changed identification mechanism — landing after we have made a 1.0 promise.

## Decision

1. **Toise 1.0 commits to the stability of *its own* contract, decoupled from the
   upstream spec's Development status.** The guarantee a 1.0 integrator gets is
   Toise's contract — pinned by `fixture_v1.bin`, governed by our additive-only /
   deprecate-before-remove policy — not a mirror of the upstream lifecycle label.

2. **Track upstream additively by default.** Compatible upstream additions (new
   optional fields, new entity/relation types) are adopted within the 1.x series
   without breaking existing producers. This is already our policy; 1.0 does not
   change it.

3. **Absorb a breaking upstream change through our deprecation path, never
   silently.** If OTel introduces an incompatible change, Toise accepts both the
   old and the new form for at least one release (dual-read), with a changelog
   deprecation notice. Only if coexistence is genuinely impossible does it become a
   Toise **major** bump (2.0). The conformance fixture is the tripwire: any wire
   drift fails the build until deliberately addressed.

4. **Do not gate 1.0 on the upstream reaching Stable.** The Development label
   governs OTel's own process; it does not bound the value Toise delivers to its
   consumers today. Blocking 1.0 on it is an open-ended external dependency we
   reject (consistent with ADR 0029's refusal of open-ended dependencies on the
   data path, and ADR 0030's adoption-first posture).

5. **Be explicit in the docs.** The API stability page and the 1.0 release notes
   state plainly: (a) the wire contract follows the OTel entity-events spec;
   (b) the stability guarantee is Toise's own (our policy), not the upstream
   label; (c) how a breaking upstream change would be handled (dual-read, then a
   major bump if unavoidable). Issue #66 remains the early-warning instrument.

## Consequences

- **Unblocks 1.0.** Producers and consumers get a contract they can build on now,
  with a clear and honest promise, instead of waiting on an externally-controlled
  Stable date.
- **We carry the divergence cost.** A future breaking upstream change costs us a
  dual-read period or, worst case, a 2.0. This is acceptable: recent motion is
  additive, the fixture detects any drift mechanically, and the #66 watch buys lead
  time to plan rather than react.
- **No code change required to adopt this ADR** — it ratifies the posture the
  existing policy and conformance fixture already encode. The work it unlocks is
  the rest of the RC-readiness checklist (field validation of OIDC/mTLS, adoption
  frictions, production soak).
- **Honest positioning.** "Stable for you, tracking a still-evolving spec, and here
  is exactly what happens if upstream breaks" is stronger than either silence or an
  indefinite wait — and consistent with Toise's adoption-friendly, LLM-first stance.
