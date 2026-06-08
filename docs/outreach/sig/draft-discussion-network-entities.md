# Draft — SIG discussion post: network infrastructure as entities

Status: DRAFT — do not post. For maintainer (Matthieu) review.

## Cible (issue / PR / venue)

A **new discussion thread**, not a reply to an existing one — because the watch (below)
found no existing in-flight network-entity work to attach to. Best venue, in order:

1. A short message in **CNCF Slack `#otel-entities`** asking whether the SIG has appetite
   for network-infrastructure entity types, linking the longer write-up. Lowest friction,
   gauges interest before spending anyone's review budget.
2. If there is appetite, a **GitHub Discussion / issue on `open-telemetry/semantic-conventions`**
   tagged for the Entities area (the registry and entity semconv live there), referencing
   the merged entity-events spec.

Do **not** open an OTEP or a semconv PR yet. This is an interest-check and a prior-art
offering, not a proposal we are asking them to merge. Listen first (per our standing
"jeu long" principle).

## Pourquoi maintenant

- The entity-events **state + embedded-relationships** model merged on 2026-06-04
  (`specification/entities/entity-events.md`). The wire shape for "an entity and its
  relationships" is now stable enough to build vocabulary on top of.
- The entity **registry** and data-model page enumerate cloud / k8s / host / process /
  service entity types and **no network-infrastructure entity types at all** — network
  appears only as `network.*` span/metric attributes and `hw.network.*` hardware metrics,
  never as first-class entities (devices, interfaces/ports, routes, links).
- Nobody is visibly pushing network-as-entities (see watch). So this is a genuine gap,
  not a crowded space — and Toise already runs a coherent vocabulary for it in production.
- The SIG itself has flagged relationship modeling and edge semantics as "future work";
  our SNMP-derived model is a concrete, real-world data point for that discussion.

## Risques / ton

- **Risk: reads as a vendor pitch.** Mitigate: frame strictly as prior art and a question
  ("is the SIG interested?"), lead with the merged spec we build on, keep Toise to one
  factual sentence, no product claims, no links that look promotional.
- **Risk: scope too big.** Network topology is enormous (L2/L3, MPLS, BGP, virtual
  overlays). Mitigate: scope this explicitly to the SNMP-MIB-derived core (device,
  interface, route) and say so — offer it as a *starting point*, invite the SIG to
  pick the boundary.
- **Risk: stepping on identity/relationship open questions.** The SIG has open questions
  on identifier selection and edge attributes. Mitigate: present our identity ladder and
  our no-edge-attributes principle as *experience reports*, explicitly deferring to
  whatever the SIG decides; never assert our way is the standard.
- **Risk: SNMP framing too narrow.** Some will monitor network gear via gNMI/NETCONF/
  streaming telemetry, not SNMP. Mitigate: note SNMP MIBs are the *derivation source* for
  the identity keys, but the entity vocabulary is transport-agnostic.
- Tone: humble, factual, sourced. No emoji. No AI attribution. British/neutral English.

---

## Proposed text (English)

**Title:** Interest check — network infrastructure as entities (devices, interfaces, routes)

Hi all,

With the entity-events specification now merged (`specification/entities/entity-events.md`)
— entity state plus embedded relationships — we wanted to raise a gap and offer some
prior art, to see whether the SIG has any appetite for it.

**The gap.** The entity registry and data-model today cover cloud, Kubernetes, host,
process and service entities. Network *infrastructure* itself is not modelled as
entities: `network.*` exists as span/metric attributes and `hw.network.*` as hardware
metrics, but there is no first-class entity type for a network device, an interface/port,
or a route, and no relationship vocabulary for link-layer or routing topology. As far as
we can tell, no one is currently working on this. If we have missed in-flight work, please
point us at it — we would rather contribute to it than duplicate it.

**Why we have a view.** We maintain a system that ingests OpenTelemetry entity-events and
serves them to consumers. It is not a collector; any OTLP producer can feed it. One of our
producers discovers network infrastructure over SNMP and emits it as entity-events, so we
have had to define a working network vocabulary on top of the merged model. We are not
proposing this as *the* standard — we are offering it as a concrete, in-production data
point for whenever the SIG looks at this area, and asking whether that is of interest.

### Entity types (with identity keys)

We model three core entity types. Identity keys are derived from standard SNMP MIBs but
the entities themselves are transport-agnostic (SNMP is just where the discriminators
happen to come from; gNMI/NETCONF/streaming-telemetry producers could populate the same
shapes).

- **`network.device`** — a logical SNMP management entity (a switch, router, firewall…).
  Single identity key `network.device.id`, a subtype-prefixed value chosen by the producer
  from a precedence ladder, highest available tier winning:

  | Tier | Value | Source | Rationale |
  |------|-------|--------|-----------|
  | 1 | `serial:<PEN>:<n>` | ENTITY-MIB `entPhysicalSerialNum`, namespaced by the IANA Private Enterprise Number from `sysObjectID` | immutable hardware id; vendor-namespaced because serials are unique per vendor, not globally; used only when a single chassis and a PEN are present |
  | 2 | `engine:<hex>` | `snmpEngineID` | globally unique by construction (RFC 3411); the robust fallback — covers no-PEN devices and stacks (one engine id stack-wide) |
  | 3 | `mac:<addr>` | LLDP chassis-id (subtype 4) | strong, but LLDP is frequently disabled |
  | 4 | `name:<n>` | `sysName` | may be unset / non-unique |
  | 5 | `mgmt:<ip>` | polled management address | last resort — mutable, so weakest |

  The identity is anchored on SNMP-immutable facts (serial / engine), **not** on LLDP or
  the management IP, because LLDP is often off and a management IP moves under
  DHCP/failover/VIP. The raw parts (`sysName`, management IP, vendor, model) ride as
  descriptive attributes, never as identity. A device stack (more than one
  `entPhysicalClass=3` chassis row) keys on the single stack-wide `engine:` id, not a
  per-member serial that would flip at failover.

- **`network.interface`** — a port on a device. Identity `{network.device.id,
  interface.name}`. Speed, operational state, etc. are descriptive attributes.

- **`network.route`** — a forwarding entry (from IP-FORWARD-MIB `ipCidrRouteTable`). The
  route metric and other route facts are descriptive attributes on this entity.

### Relationships

Using the merged embedded-relationship model (bare descriptors: relationship type + target
type + target id, source implicit):

- **`has_interface`** — `network.device` → `network.interface`.
- **`connected_to`** — `network.interface` ↔ `network.interface`; bare physical/link
  adjacency between two ports (derived from LLDP / bridge FDB).
- **`has_route`** — `network.device` → `network.route`.
- **`next_hop_via`** — `network.route` → `network.interface` (or device); the egress
  next hop.

### The principle we would flag for discussion: topology-as-entities, bare edges

The merged relationship model carries **no edge attributes**. Rather than treat that as a
limitation, we adopted it as a design rule and it has held up well in practice:

> anything an edge would have described becomes an entity, so edges stay bare.

So a port is not an attribute on a device→device edge — it is a `network.interface`
entity, and adjacency is a bare `connected_to` between two interfaces. A route's metric is
an attribute on the `network.route` entity, not on an edge. Provenance (which observer saw
a link, via which protocol) lives on the OTLP instrumentation scope, not on the edge. A
device-to-device "adjacent_to with local_port/remote_port" view, if a consumer wants it,
is *derived* on the read side from the port `connected_to` edges — not stored.

We have found this keeps the model fully compatible with the merged spec (no edge-attribute
extension needed) and avoids the ambiguity of overloaded edges. We think it is a useful
data point for the SIG's open question on edge attributes, but we are not claiming it is
the only answer — it is just what fell out of building on the merged model as-is.

### What we are asking

1. Is network infrastructure as entities something the SIG wants to take on (now, later,
   or "not our scope")?
2. If yes, is an SNMP-MIB-derived core (device / interface / route + the four relations
   above) a reasonable starting boundary, or would you scope it differently?
3. Is the topology-as-entities / bare-edges principle one the SIG would want to adopt
   generally, independent of the network vocabulary?

Happy to write this up more formally (a registry sketch or an OTEP) if there is interest —
but wanted to check appetite before spending anyone's review time. Thanks for reading.

---

## Source notes (for the maintainer; not part of the post)

Watch conclusion: **no in-flight OTel work** on network infrastructure as entities, as of
2026-06-08. Verified against:

- Entity data-model page — entity types are service/host/process/k8s.*/container/
  service.instance; **no network entities**.
  https://opentelemetry.io/docs/specs/otel/entities/data-model/
- Entity registry README — namespaces are Android/App/AWS/Browser/CI-CD/Cloud/CloudFoundry/
  Container/Deployment/Device/FaaS/GCP/Heroku/Host/K8s/OpenShift/OS/OTel/Process/Service/
  Telemetry/VCS/WebEngine/zOS; **no network device/interface/route**.
  https://github.com/open-telemetry/semantic-conventions/blob/main/docs/registry/entities/README.md
- "How to write resource and entities conventions" guidance — zero mentions of network;
  examples are service/k8s/container/process/host.
  https://opentelemetry.io/docs/specs/semconv/how-to-write-conventions/resource-and-entities/
- `model/entity/network.yaml` — **404, does not exist**.
- OTEP repo — only the (archived) entities data-model OTEP 0256; no network-entity OTEP.
- NetBox Labs "NetBox Observability" (the one network vendor near this space) uses **OTLP
  as transport only**; it does not propose network entity semantics to OTel.
  https://netboxlabs.com/blog/announcing-netbox-observability-infrastructure-monitoring-that-understands-design/

Caveat: GitHub issue-search pages (`/issues?q=…`) and tree pages 504-timeout via the
fetch tool, so the issue tracker was searched indirectly (web search + registry/docs/model
files). A logged-in pass over `open-telemetry/semantic-conventions` issues filtered on
`network entity` and the `.project#16` board would make the "nobody is pushing it"
claim airtight — recommend the maintainer do that quick check before posting.
