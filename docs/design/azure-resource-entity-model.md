# Azure resources as entities — the model for senhub-agent 0.6.5

Answers to the seven questions asked for the `azure_monitor` probe (Azure
Resource Graph discovery, Pro tier, agent enterprise). Written for the producer
author; nothing here is code and nothing changes in a deployed Toise.

The short version: **a cloud resource is its own entity, keyed by its ARM id,
and it is never a `host`.** Everything else follows from that, and it follows
from doctrine this project already settled twice — for the hypervisor's view of
a machine and for the cluster's view of a machine. Azure's view is the third
facet of the same shape, so this is an application of an existing rule rather
than a new one.

## 0. The one thing that must not happen

`host.id` in senhub-agent is the **OS machine-id** (gopsutil: `/etc/machine-id`
on Linux, the platform equivalent on Windows). It is not the cloud instance id,
on any provider, today.

So putting the Azure `vmId` into `host.id` — even though the upstream convention
can be read as asking for the cloud instance id — **re-keys every Azure host**:
the old entity dies, a new one appears, and since ADR 0018 removed tolerant
matching **nothing bridges them**. There is no migration and no retrofit. The
history of those machines would simply stop and restart under a new name.

It is worse than a one-off cost, because the failure is silent and symmetric:
our contract makes `host.id`'s rendering normative (lowercase dashed UUID) while
`/etc/machine-id` holds the same bytes as 32 hex with no separators. Two
producers emitting two spellings of one machine produce **a permanent duplicate
of every host**, and neither of them reports an error. An identifying attribute
without a canonical rendering is not yet an identity.

**Rule: `host.id` stays the machine-id, byte for byte, rendering unchanged. The
Azure `vmId` is a descriptive attribute on the host, never its identity.**

## 1. Entity type: generic, keyed on what the observer can obtain

**A single generic type, identified by `cloud.resource_id`** (the full ARM id,
which is unique and stable for the resource's lifetime).

The rule it follows is the one already quoted on `network.endpoint`: *emit a
different entity type keyed on what you can reliably obtain.* What Resource
Graph reliably gives is the ARM id, the ARM type, the name, the region and the
tags — control-plane facts. It does not give what would identify the thing as a
service: an App Service has no `service.name` from this vantage point, a SQL
database has no on-the-wire identity.

**Specialising by family is the expensive option, and the cost is the one above.**
Emitting an App Service as a `service.instance` puts a control-plane object under
an identity that an in-guest producer is *also* emitting from inside the machine,
under a different spelling of the same thing. That is the duplicate-of-every-host
failure again, one level up, and it would be permanent.

So: not (b), and not (c) either. The VM case that looks like specialisation is
not one — it is the generic resource **linked** to the host that already exists
(§2). The link is what carries the meaning; the type stays honest about what was
observed.

**On the spelling: `cloud.resource_id` is the proposed key, and it is not yet in
our contract** — we carry `cloud.provider` and `cloud.platform` and nothing
else from this namespace. Before it is frozen it should be checked against the
semantic conventions by the session that tracks them, per the standing rule that
a data-model key is chosen against the spec and not from memory. If upstream
spells it otherwise, take upstream's spelling: this is a new key for us and there
is nothing to migrate.

The ARM type, name, subscription, resource group, region, tags and provisioning
state are **descriptive** attributes on that entity.

## 2. The agent's own VM: a third facet, joined by `same_as`

This project has already answered this twice, and the two answers agree:

- **`compute.vm`** is a VM as seen from its hypervisor, *"deliberately NOT a
  `host`: a host is keyed by machine-id, so a vmid in host.id would be a wrong,
  permanent identity and would duplicate the in-guest host … the two are distinct
  facets reconciled later by a same_as overlay, never merged."*
- **`k8s.node`** is the cluster's view of a machine, *"NOT a second host: it
  carries the machine's ROLE IN THE CLUSTER … A node cordoned while its host is
  perfectly healthy is what makes the two distinct. Joined to its host by a
  same_as belief, because each identity is complete on its own and the collapse
  would lose no identifying attribute."*

Azure's ARM resource is the **cloud provider's view** of the same machine: it
carries the machine's role in the subscription — provisioning state, tags, SKU,
the fact that it still exists while deallocated — which are not facts about the
operating system.

So: **the ARM resource and the host are two entities, joined by `same_as`.**

The test is the one `k8s.node` states, and it is worth applying rather than
reasoning from lifecycle (which is how one talks oneself out of the right
answer): *same_as when each identity is complete on its own and the collapse
would lose no identifying attribute.* An ARM id and a machine-id are each
complete; collapsing them loses nothing identifying. `same_as` it is.

`same_as` is already a registered relation, so this needs no new vocabulary and
is not subject to the "do not emit a relation before a Toise that registers it is
deployed" gate. It carries `confidence` and `basis`; the basis here is the
`vmId` read from IMDS inside the guest matching the `vmId` on the ARM resource,
which is a strong, non-heuristic match and should be named as such.

### A consequence the producer should know about

`same_as` feeds the read-time canonical collapse, so emitting one per Azure VM
creates one canonical pair per machine. Consumers that build an inventory
currently **cannot group a canonical pair cheaply**: there is no group identity
in `find_entities` or `get_neighbors` output, and the group only appears in
`get_entity`'s `canonical` block — one call per entity (toise#542 and the group-
identity gap recorded with it).

This does not change the answer. It does mean the group handle becomes a
prerequisite for a consumer that wants to count Azure machines once rather than
twice, and it should be sequenced before a large Azure fleet lands.

## 3. Subscription and resource group: attributes, not entities

**Descriptive attributes for filtering, in 0.6.5.** Both have assigned
identifiers (a subscription GUID; a resource group's ARM id), so they *could* be
entities under ADR 0034's test. The test that decides against it is the one used
to refuse publishing VLANs: an entity earns its place when something needs to
**traverse** it, and a subscription carries no hop of any path. Membership would
be necessary and not sufficient for anything a consumer asks.

If someone later needs to answer "what else is in this resource group", that is a
different conversation and it should arrive with the use case attached. Opening
it now would mint two entity types to carry a filter.

## 4. ARM relations: none in 0.6.5, and the reason is a deployment rule

**Agreed: nothing but the host link.** Beyond the design argument, there is a
hard constraint: a record carrying a relation type this Toise does not register
is **rejected per record, silently from the producer's point of view**, and the
conformance kit cannot catch it because it validates shape, not registry
membership. The only observable is a counter.

VM → managed disk, NIC → VNet, App Service → plan, database → server would each
need either a new registered type or an existing one stretched past its meaning.
Both are 0.7 conversations, and each must ship behind a producer option that is
off by default until a Toise carrying the type is deployed to that tenant.

## 5. Disappearance: conforms, with one obligation

**Two successive missed discoveries → producer removal is correct.** A removal
speaks for the speaker that sends it, and a debounce of two cycles is the right
shape for a control-plane poll where a single failed query must not look like a
decommission.

The obligation that goes with it: **declare a liveness interval, and declare it
above the real cadence, never below.** With a 15-minute discovery, a declared
interval in the 40–60 minute range leaves room for one missed cycle without the
backstop firing. A declared interval shorter than the real cadence makes Toise
reap and recreate the entities every cycle — flapping, which is invisible in a
short acceptance run and pollutes every later change query.

`delete_reason` is an **open enum and is never validated**; MCP renders it raw.
Pick one word, keep it stable, and do not reuse `rekeyed`, which means something
else. The plain-language disappearance sentence a consumer reads is built from
`delete_source`, not from this field, so the word is for humans reading the log.

## 6. A resource excluded by a configuration filter

**It must be visible as an exclusion, because an absence that does not say it is
an absence has misled a reader seven times in this project.** Emitting nothing
makes "excluded by configuration" and "does not exist in Azure" the same answer.

**The existing precedent is narrower than it sounds, and it is the better half of
the answer.** `unmonitored` in senhub-agent is a **delete reason**
(`internal/agent/services/entity/model.go:66`), not an entity state. So the
precedent is not "emit an unmonitored entity" — it is "remove the entity and say
why". That is exactly right for a resource that **was** in scope and leaves it:
it disappears from the graph, with `delete_reason = unmonitored` naming the
operator's own filter as the cause rather than a decommission in Azure. A reader
who sees it vanish is told, in the log, that nothing happened in Azure.

It does not cover a resource **excluded from the start**, which was never
emitted, so there is no removal to carry the reason. That half needs a counter:
resources excluded by filter, per subscription and per ARM type. Cheap, bounded,
and it answers "why is my inventory short" without asserting the existence of
things the operator chose not to monitor.

**So: both.** `delete_reason = unmonitored` on the way out of scope, a counter for
what never entered it. Neither alone is complete, and silence is not an option in
either direction.

## 7. Entra ID: out of scope for 0.6.5

Metrics attached to the tenant as an attribute, no entities.

A tenant has an assigned identifier and so do applications and service
principals, so the identifier test does not refuse them. The reason to wait is
that an identity-provider object model is the kind of thing that is wrong
permanently if it is opened casually, and nothing in 0.6.5 needs to traverse it.
When a use case arrives, it should name what it needs to traverse — "which
applications hold a credential expiring this month" is a different model from
"which principals can reach this subscription".

## Summary

| Question | Answer |
| --- | --- |
| Entity type | Generic, keyed `cloud.resource_id` (the ARM id) |
| Specialisation by family | No — the observer cannot obtain service-level identity |
| `host.id` on an Azure VM | **Unchanged: the machine-id.** `vmId` is descriptive |
| ARM resource vs host | Two entities, joined by `same_as`, basis = matching `vmId` |
| Subscription / resource group | Descriptive attributes, not entities |
| ARM relations | None in 0.6.5 beyond the host link |
| Disappearance | Two missed cycles → producer removal; declare an interval above the cadence |
| Excluded by filter | Never silent — `delete_reason = unmonitored` leaving scope, a counter for what never entered |
| Entra ID | Out of scope; metrics on the tenant attribute |
