# DRAFT — Toise experience report on multi-detector entity identity (host.id)

> **Status: UNPOSTED DRAFT, pending maintainer (Matthieu) approval.**
> Prepared by the OTel SIG liaison. Do not post upstream until the maintainer
> approves. This is prior art offered as an experience report — explicitly not
> prescriptive about what the SIG adopts.
>
> **Target (chosen): comment on**
> `open-telemetry/opentelemetry-specification` **issue #4266** —
> "Resource/Entity merge logic prevents fine-grained detectors"
> https://github.com/open-telemetry/opentelemetry-specification/issues/4266
> (OPEN, tigrannajaryan, "Entities: Phase 1" board). Our report answers its
> "option 3" (revise merging so same-type contributions combine).
>
> Supporting precedent (cite, do not cross-post): #5021 (Dash0 entity-merging
> experience report) shows a vendor prior-art comment is welcome in this lane.
>
> **Dead targets (do NOT use):** collector-contrib #18533 / #21482 — CLOSED 2023,
> detector-precedence already resolved there.

---

## Proposed comment text (English)

This thread is about letting fine-grained detectors each contribute identity
instead of the merge rule dropping the second one. We ran into the same question
building Toise (an OTLP entity-event store that serves an infrastructure graph to
LLM/MCP and GraphQL consumers) and shipped an answer in v0.8.0 (2026-06-22).
Sharing it as prior art — one shape that worked for us, not a prescription. It is
complementary to the Dash0 report in #5021: theirs reconciles conflicting
attribute *values*; ours is specifically about multiple detectors contributing
*identity*.

**1. We made "no fabricated id" a hard rule.**

A producer emits only the identity it can reliably observe. Toise never invents a
host id and never fuzzy-merges two entities on a partial match — a non-matching
identity is a distinct entity, not an identity change. So a generic detector that
cannot discover a stable id emits *nothing* for that slot rather than synthesizing
one. (For the analogous PID-reuse problem we pair `process.pid` with
`process.creation.time`, so a bare reusable value only becomes an identity when
paired with a lifetime-stable discriminator.) This matches the data model's
"repeatably obtained by observers" and "MUST not change during the lifetime"
requirements.

**2. A second detector contributes identity via a `same_as` belief edge —
without a write-time merge.**

This is the direct answer to the merge problem here. The current "if identity
differs, drop the new entity" rule is winner-take-all: when a `host` detector has
already created the entity, a `hostid`/cloud detector's contribution is lost.
Toise avoids the write-time merge entirely. A secondary detector that discovers an
additional id (Linux machine-id, AWS instance ARN, VMware managed-object id, SNMP
engineID, Hyper-V KVP guest id, …) asserts a `same_as` edge between the two
facet-entities, carrying a **confidence** (0–1) and a **basis** (e.g. a serial
match, or a KVP guest id read from the hypervisor). Above a configurable threshold
the **consumer** sees a single canonical group at read time; storage is never
merged and the weaker signal is retained, so the assertion stays auditable and
reversible. This is option 3 made concrete *without* changing the storage merge
rule: producers stay append-only, nothing is dropped, and the combination is a
derived/consumer-side view rather than a destructive write.

**3. On "is-a host -> ec2.instance": it interoperates, it isn't an alternative.**

We chose reconciliation by `same_as` (a belief edge with confidence) over an is-a
type hierarchy because it generalizes to N heterogeneous sources and carries
uncertainty natively. We are not arguing against is-a — an is-a relation maps
cleanly onto `same_as` as a confidence ~1.0 assertion, so the two models
interoperate: is-a is the certain special case of the same reconciliation edge.

**4. A precedence-chain template, for "use whatever source makes the most sense
in your context."**

For network devices — another entity with no universal id — we run a defined
precedence over discriminators, anchored on the most immutable source and degrading
explicitly:

```
serial:<PEN>   (ENTITY-MIB entPhysicalSerialNum)   most immutable
> engine:      (snmpEngineID)
> mac:         (LLDP chassis-id)
> name:        (sysName)
> mgmt:        (management IP)                       mutable, last resort
```

The principles, offered as a possible template for a `host.id` precedence rule:

- **Anchor on the most immutable fact available, not the most convenient one.**
- **Treat mutable/network-derived values as an explicit, labeled last resort** — a
  management IP (or a leased host IP) can change without the device changing.
- **Make the chosen tier visible** (the id is prefixed with the discriminator kind),
  so a consumer can tell *how confidently* the entity is identified and reconcile
  across sources accordingly.

A `host.id` analogue might read: cloud-provider stable id (ARN / instance id / moid)
> firmware/SMBIOS UUID > OS machine-id > … > hostname as a labeled last resort —
with the rule that a detector emits at the highest tier it *can* observe, and a
detector seeing only a lower tier emits at that tier (or contributes via `same_as`)
rather than guessing a higher one.

Happy to go deeper on any of these (the threshold/eviction behavior, the basis
taxonomy, or the precedence chain) if useful.

---

## Internal notes (do NOT post — for the maintainer)

**Target rationale.** #4266 is the tightest conceptual match (multiple detectors
contributing identity; current merge is winner-take-all; maintainer's option 3 =
revise merging) and is owned by a TC member + board-tracked, so a comment lands in
front of the right people at low risk. #5021 (Dash0) establishes that a vendor
experience-report is welcome in this lane; cite it, don't cross-post. Collector-
contrib #18533/#21482 are CLOSED since 2023 — do not post there.

**Timing.** Entities SIG meets Mondays 16:00 UTC, weekly. The 2026-06-22 meeting
has passed; next is 2026-06-29. Post mid-week (Wed/Thu) so it has async read time
before the 29th.

**Accuracy guardrails:**
- ADR 0020 is ratified and shipped in v0.8.0 (2026-06-22) — cite the *release*, not
  the repo ADR file (which still reads "Proposed/surcouche").
- Release notes name only two example bases (serial match, Hyper-V KVP guest id);
  the fuller id list is our design framing, kept as "different detectors see
  different ids."
- `network.device.id` precedence is frozen + implemented but not yet emitted by a
  real production producer (phase-1 = synthetic; real SNMP = phase-2). Framed as "we
  run a defined precedence" (the rule exists) without claiming a live SNMP fleet.
- `process.pid` + `process.creation.time` identity verified against ADR 0018,
  otel-mapping.md, the senhub-agent contract, and internal/demo/demo.go on origin/main.

**Tone:** experience report, prior art, no "we know better", no emoji, offers
detail on request. On-principle: facts in the store, reconciliation is a
derived/consumer-side view.
