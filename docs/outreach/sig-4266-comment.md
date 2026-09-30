This thread is about letting fine-grained detectors each contribute identity instead of the merge rule dropping the second one. We ran into the same question building Toise (an OTLP entity-event store that serves an infrastructure graph to LLM/MCP and GraphQL consumers) and shipped an answer in v0.8.0 (2026-06-22). Sharing it as prior art — one shape that worked for us, not a prescription. It is complementary to the Dash0 report in #5021: theirs reconciles conflicting attribute *values*; ours is specifically about multiple detectors contributing *identity*.

**1. We made "no fabricated id" a hard rule.**

A producer emits only the identity it can reliably observe. Toise never invents a host id and never fuzzy-merges two entities on a partial match — a non-matching identity is a distinct entity, not an identity change. So a generic detector that cannot discover a stable id emits *nothing* for that slot rather than synthesizing one. (For the analogous PID-reuse problem we pair `process.pid` with `process.creation.time`, so a bare reusable value only becomes an identity when paired with a lifetime-stable discriminator.) This matches the data model's "repeatably obtained by observers" and "MUST not change during the lifetime" requirements.

**2. A second detector contributes identity via a `same_as` belief edge — without a write-time merge.**

This is the direct answer to the merge problem here. The current "if identity differs, drop the new entity" rule is winner-take-all: when a `host` detector has already created the entity, a `hostid`/cloud detector's contribution is lost. Toise avoids the write-time merge entirely. A secondary detector that discovers an additional id (Linux machine-id, AWS instance ARN, VMware managed-object id, SNMP engineID, Hyper-V KVP guest id, …) asserts a `same_as` edge between the two facet-entities, carrying a **confidence** (0–1) and a **basis** (e.g. a serial match, or a KVP guest id read from the hypervisor). Above a configurable threshold the **consumer** sees a single canonical group at read time; storage is never merged and the weaker signal is retained, so the assertion stays auditable and reversible. This is option 3 made concrete *without* changing the storage merge rule: producers stay append-only, nothing is dropped, and the combination is a derived/consumer-side view rather than a destructive write.

**3. On "is-a host -> ec2.instance": it interoperates, it isn't an alternative.**

We chose reconciliation by `same_as` (a belief edge with confidence) over an is-a type hierarchy because it generalizes to N heterogeneous sources and carries uncertainty natively. We are not arguing against is-a — an is-a relation maps cleanly onto `same_as` as a confidence ~1.0 assertion, so the two models interoperate: is-a is the certain special case of the same reconciliation edge.

**4. A precedence-chain template, for "use whatever source makes the most sense in your context."**

For network devices — another entity with no universal id — we run a defined precedence over discriminators, anchored on the most immutable source and degrading explicitly:

```
serial:<PEN>   (ENTITY-MIB entPhysicalSerialNum)   most immutable
> engine:      (snmpEngineID)
> mac:         (LLDP chassis-id)
> name:        (sysName)
> mgmt:        (management IP)                       mutable, last resort
```

The principles, offered as a possible template for a `host.id` precedence rule:

- **Anchor on the most immutable fact available, not the most convenient one.**
- **Treat mutable/network-derived values as an explicit, labeled last resort** — a management IP (or a leased host IP) can change without the device changing.
- **Make the chosen tier visible** (the id is prefixed with the discriminator kind), so a consumer can tell *how confidently* the entity is identified and reconcile across sources accordingly.

A `host.id` analogue might read: cloud-provider stable id (ARN / instance id / moid) > firmware/SMBIOS UUID > OS machine-id > … > hostname as a labeled last resort — with the rule that a detector emits at the highest tier it *can* observe, and a detector seeing only a lower tier emits at that tier (or contributes via `same_as`) rather than guessing a higher one.

Happy to go deeper on any of these (the threshold/eviction behavior, the basis taxonomy, or the precedence chain) if useful.
