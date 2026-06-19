# Draft — SIG experience report: entity-events needs an operator-metadata story

Status: **DRAFT — do not post.** For maintainer (Matthieu) review. 2026-06-19.

Companion to the Toise governance vocabulary frozen on 2026-06-19 (`internal/model/governance.go`,
contract `senhub-agent-contract.md` AT9, issue toise#231).

## Cible (venue) — informed by an upstream watch (2026-06-19)

A watch of `open-telemetry/semantic-conventions` found the four dimensions at very
different maturity, on different surfaces. **Split, do not post one mega-thread.**
The single strategic message, if any, is *"service-scoped operator metadata should
generalize to entities"* — best floated once in a **Resources & Entities SIG meeting**,
then split into the per-thread comments below.

| Dimension | Existing upstream work | Recommended venue |
| --- | --- | --- |
| Criticality / tier | [#3643](https://github.com/open-telemetry/semantic-conventions/issues/3643) "Stabilize service.criticality" (open, service-scoped); origin [#2986](https://github.com/open-telemetry/semantic-conventions/issues/2986) (done) | Comment on #3643: should stabilization anticipate a general primitive vs lock to `service.*`? |
| Ownership | Active cluster, all `service.*`: [#3754](https://github.com/open-telemetry/semantic-conventions/issues/3754) cost_center (open), [#3475](https://github.com/open-telemetry/semantic-conventions/issues/3475) business_unit, [#3397](https://github.com/open-telemetry/semantic-conventions/issues/3397) cost_center.id | Comment on #3754: raise the cross-entity owner primitive the cluster keeps re-deriving per-attribute |
| On-prem location | [#2576](https://github.com/open-telemetry/semantic-conventions/issues/2576) "Add Deployment.datacenter" (open, in triage, stale ~11mo; datacenter level only) | Revive #2576 with on-prem experience; argue for site/building/room/rack beyond datacenter |
| Lifecycle / maintenance | None found (distinct from `hw.state` health) | New discussion, or float in a SIG meeting first (roadmap bandwidth is tight) |

The [2026 SemConv roadmap (#3330)](https://github.com/open-telemetry/semantic-conventions/issues/3330)
prioritizes none of these; new cross-entity primitives need fresh proposals routed to the
relevant sub-SIG. All four are **operator-asserted facts** (the producer states them), so
championing them does not contradict Toise's facts-only engine posture.

---

# Experience report: entity-events needs an operator-metadata story

## Summary

We have been building a consumer of entity-events (an LLM- and human-facing
infrastructure/topology graph) and have run into a recurring gap: the attributes
operators most want to read off an infra graph — *who owns this*, *how critical is it*,
*where does it physically live*, *what is its lifecycle state* — have almost no home in
semantic conventions today, and what does exist is scoped to services. This report
describes the gap, the provisional vocabulary we adopted to fill it, and a set of open
questions we would like the SIG's input on. It is an experience report, not a proposal.

## The gap

The entity data model already gives us the structural hook we need. It splits an
entity's attributes by role: identifying attributes "MUST not change during the lifetime
of the entity," while descriptive (non-identifying) attributes "MAY change over the
lifetime of the entity." Crucially, descriptive attribute keys only **SHOULD** follow
semantic conventions, not MUST — so a backend is explicitly permitted to carry
producer-asserted descriptive keys that semconv has not (yet) standardized.

The problem is not the model; it is the vocabulary. For the class of attributes we are
calling *operator metadata* — facts an operator or asset system asserts about an entity,
independent of any signal the entity emits — semconv coverage is sparse and mostly
service-scoped:

- `service.criticality` exists (Alpha) with well-known values `critical`/`high`/`medium`/`low`,
  but is described as applying to service instances.
- `service.namespace` exists and explicitly doubles as an ownership hint ("for example the
  team name that owns a group of services") — again, services only.
- For physical location, `cloud.region` and `cloud.availability_zone` cover cloud topology,
  but there is no on-premises vocabulary anywhere we can find: no site/datacenter/rack/room
  keys, and the `host.*` namespace carries zero location keys.
- For lifecycle/maintenance, there is no operator-asserted key. `hw.state` is
  detector-observed hardware *health*, a distinct concept from an operator declaring an
  entity to be in scheduled maintenance or decommissioning.

These attributes are cross-cutting: equally meaningful on a `host`, a `service`, a `db`, a
`network.device`, a `compute.vm`, or a `container`. But the only standardized keys assume
the entity is a service.

## What we adopted (illustrative prior art)

We froze a small cross-cutting vocabulary that may decorate any entity type. Everything
below is carried as **descriptive** entity attributes and is **producer/operator-asserted**
— nothing is inferred. We reuse semconv where it exists and mark our own keys as provisional.

| Dimension | Key(s) | Source |
| --- | --- | --- |
| Criticality / tier | `service.criticality` (`critical`/`high`/`medium`/`low`), **generalized from service-only to any entity type** | semconv reuse (Alpha), scope-stretched |
| Ownership (services) | `service.namespace` | semconv reuse |
| Ownership (any entity) | `entity.owner.team`, optional `entity.owner.contact` | provisional |
| Physical on-prem location | `entity.location.site` / `.datacenter` / `.rack` / `.room` | provisional |
| Lifecycle / maintenance | `entity.lifecycle.status` (open enum, e.g. `active`/`maintenance`/`decommissioning`/`retired`) | provisional |

The one place we deliberately stretched an existing convention rather than minting a new
key is criticality: reusing `service.criticality`'s value set verbatim on non-service
entities felt better than inventing a parallel `entity.criticality`, but it does push the
attribute past its documented scope.

## Open questions for the SIG

We are not asking for any specific outcome — these are the questions our usage raised:

1. **Criticality scope.** Is there appetite to generalize `service.criticality` (or its
   value set) beyond services?
2. **Ownership.** Should semconv define a cross-entity ownership key, given `service.namespace`
   is service-scoped?
3. **On-prem location.** Is there interest in an on-premises physical-location vocabulary
   (site/datacenter/rack/room) to complement the cloud-location keys?
4. **Lifecycle.** Is an operator-asserted lifecycle/maintenance key in scope, distinct from
   detector-observed health like `hw.state`?
5. **Namespacing.** If any of the above is worth standardizing, is a dedicated namespace
   warranted — or is per-backend provisioning the intended steady state?

## Offer

These four dimensions are, in our experience, the highest-value attributes for a human or
LLM reading an infra graph, and precisely the ones with the weakest conventional coverage.
We are happy to share usage data and consumer-side experience (which keys queries actually
hit, how the criticality scope-stretch holds up in practice, what the lifecycle enum drifts
toward) as input to whatever the SIG decides — including the conclusion that this should
stay a backend concern.

## Sources

- [Entity Data Model (identifying vs descriptive; SHOULD-follow-semconv)](https://opentelemetry.io/docs/specs/otel/entities/data-model/)
- [semconv service attributes (service.criticality, service.namespace)](https://opentelemetry.io/docs/specs/semconv/attributes-registry/service/)
- [semconv cloud attributes (cloud.region, cloud.availability_zone)](https://opentelemetry.io/docs/specs/semconv/attributes-registry/cloud/)
- [semconv host attributes (no location keys)](https://opentelemetry.io/docs/specs/semconv/attributes-registry/host/)
- [semconv hw.state (detector-observed hardware health)](https://opentelemetry.io/docs/specs/semconv/registry/attributes/hardware/)
