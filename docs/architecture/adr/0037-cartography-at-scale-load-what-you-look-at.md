```markdown
# 37. Cartography at scale: load what you look at

- Status: Accepted
- Date: 2026-11-09
- Decided: 2026-11-09
- Constrains: ADR 0002 (event sourcing as the storage pattern), ADR 0008
  (in-memory projection from the event log), ADR 0010 (GraphQL as the
  primary query language), ADR 0011 (MCP server design), ADR 0021 (human
  interfaces live at the edge, not in the core), ADR 0022 (the engine
  stores facts only; derivation is a surcouche), ADR 0025 (multi-tenancy,
  a stack per tenant), ADR 0035 (entity handles: the identity fingerprint
  is the consumer's reference)

## Context

### What exists today

Toise persists infrastructure as an append-only journal of events per
tenant. Two read paths sit on top of it: an MCP server for agents and a
GraphQL API for the web cartography page. The projection that serves
both is held in memory in `internal/projection/graph.go`, with a
`byType` index, a `byHash` index, and two adjacency tables `out` and
`in`. Listing a type is indexed; walking the neighborhood of one entity
is O(degree); filtering on an attribute is a linear scan of the whole
type, performed in memory after the listing. `CountByType()` recomputes
by walking every identifier of every type and testing membership in the
deleted set, O(number of entities) on every call. No aggregation API
exists: GraphQL exposes `totalCount` on connections and a `graphScope {
tenant entities relations newestEvent oldestAnswerable }` document, but
no grouping on any dimension.

The current cartography page (`web/cartography/index.html`) enumerates
two persisted documents, `entities` and `relations`, in pages of 200
(the server caps `first` at 200), both in parallel. On a fleet of
16,858 entities and 18,713 relations this is roughly 85 + 94 = 179
requests; one reader observed 181, of which 2 timed out. The code
catches the error, shows a one-line warning, and does not redraw. Both
datasets are replaced only after both enumerations have succeeded, so at
first load a single failed page out of 179 discards the other 178 and
leaves a blank canvas. A guard stops each enumeration at 1000 pages,
i.e. 200,000 entities; beyond that the page draws a truncated graph
without saying so. The page has no `graphScope` document: a valid but
empty response renders as a blank canvas that does not name its scope
(issue #596). The root cause is that the list of persisted documents is
frozen on the relay side, which removed from the page even the ability
to ask for its scope.

Measured load history: 10,000 machines / 311,000 entities showed that
memory is not the problem, but `recentChanges` and `graph_diff` took
7.5 s over a one-hour window. Measured sizing for that projection is
roughly 130 MB RAM, 28 MB disk, and about 47 entities per host. The
three real readable fleets today carry 16,858, 2,320 and 1,065
entities; the largest real fleet is therefore around 17,000 entities,
two orders of magnitude below the one-million target.

Grouping dimensions available today as attributes: `entity.location.site`,
`entity.location.datacenter`, `entity.owner.team`, `service.criticality`,
`entity.lifecycle.status`, measured present on nearly all entities of
several types, plus `entity.label.app`. There is no network attribute
on the host: an address is an entity two hops from the host (host
-has_interface-> network.interface <-bound_to- network.address), and a
`network.segment` type exists, but no network mask is emitted anywhere.
None of these dimensions is indexed; grouping on any of them today is a
full scan of the fleet.

Workload separation, in relative order of magnitude, without any
schedule: what exists today is small (indexed type listing, O(degree)
neighborhood walk, `graphScope`, cursor pagination stable across
replicas); what must be built is medium (server-side grouping and
counts, scope-aware empty states, incremental summaries on the
ingestion path); what must be built and proven is large (a measured
performance contract at 100,000 then 1,000,000 entities).

Measured today, through the same gateway the page uses, with the exact
page query (one page of 200 entities, full attributes): alone, 0.23 to
0.26 s on the fleet of 16,858 entities, and 0.20 s on a fleet of 1,065
entities, so fleet size costs 40 ms, not seconds. Four simultaneous
calls: 0.25 s each. Eight simultaneous: 0.31 to 0.33 s each. Each
replica taken separately: 0.24 s, none is slow. Six chained cursor
jumps: 0.24 s flat, depth costs nothing. The conclusion is explicit: the
timeouts observed by the page reader (2 calls out of 181 beyond 10 s,
then 8 out of 205 beyond 30 s) do NOT come from Toise read latency, and
no ADR should be built against that imaginary cause.

The real server defect was found by reading the code.
`ListEntities(typ)` in `internal/projection/graph.go`, called by the
GraphQL `Entities` resolver at EACH page, walks every entity of the
tenant, copies them into a slice, then SORTS THE WHOLE SLICE, before
cutting out 200. The MCP path does the same with
`ListEntitiesByFingerprint`, which additionally walks the identity index
of ALL types before filtering by type. Consequence: 85 pages
materialized and sorted the entire fleet 85 times. At 17,000 entities
this is invisible, and the measurements above confirm it. At one
million, each BOUNDED page becomes a scan and a sort of one million
elements. The principle "load what you look at" is therefore NECESSARY
BUT NOT SUFFICIENT, because a bounded page is not today a bounded cost.
An aggregate overview layered on top of this pagination would inherit
the same cost. This defect remains true and stays in this document, but
it only manifests at large scale: at 17,000 entities it is invisible,
and it is NOT what fails the page today.

The rendering was measured on a test bench by the page owner, with
headless Chromium, no interface, no type folding — but against a stub
server, so the data-arrival figure it produced does not measure Toise.
On that stub server, the data for 16,858 entities and as many relations
arrived in 1.0 s, then the vis-network drawing took 25 to 40 s with the
main thread blocked — the page does not respond during that time, and a
screenshot waits more than 30 s. That 1.0 s figure comes from a false
server and is not a measurement of Toise; it is reported here only to
be discarded. In production, the measurements above give 0.25 s per
page of 200 and 0.33 s with eight concurrent requests; 179 pages with
four concurrent requests therefore cost about 11 s, and with eight
concurrent requests about 7 s. The real data time in production is 7 to
11 s, not 1 s. The render/data ratio is therefore about 2.5 to 5, not 25
to 40. Both figures point in the same direction, but this ADR carries
the right one.

The consequence is the reversal: the dominant cost item is neither the
server nor the network, it is the DRAWING. The decisive argument in
favor of the aggregated overview is therefore not to save bytes or
server computation: it is to NOT DRAW. Everywhere this document
previously justified the decision by server cost, the justification is
reformulated in this sense.

The ceiling of drawn nodes is not measured; it sits below a few
thousand for the view to stay interactive, and measuring it is a
prerequisite of section 5, not a tuning detail.

### The detail step has no API on the page's transport

Verified in the schema: the GraphQL `Query` surface is exactly
`graphScope`, `entity(id)`, `entities(filter, first, after)`,
`relations(filter, first, after)`, `entityHistory`, `recentChanges`,
`canonical`. The `Entity` type carries NO relation field. GraphQL
therefore offers no traversal at all. MCP has `get_neighbors` with a
depth; GraphQL has nothing equivalent.

The consequence, stated without softening: detail-on-demand IS a
bounded neighborhood walk — the only operation the projection really
does well, because adjacency is in memory and the walk is O(degree).
But the page goes through GraphQL, behind a relay that serves only
named persisted documents, and there is no document to persist for a
walk. Half of the drawing that was supposed to be cheap is the half
that has no API. A page that emulated it would make one round trip per
neighbor, then one per neighbor's neighbor: exactly the round-trip
storm this decision exists to remove. Cited as issue #597.

### The tension with ADR 0021 and ADR 0022

This decision, as formulated, sits in tension with two decisions that
are already taken, and the tension must be stated honestly rather than
waved away.

ADR 0021 decides that human interfaces live at the edge, not in the
core. Yet the retained decision asks for an aggregated overview for a
page — that is human-interface work, and the question is where it
lives.

ADR 0022 decides that the engine stores facts only, a faithful store of
entity events, and that any derivation is a surcouche. Yet a summary
maintained incrementally as events arrive is precisely a derived state.

Three readings were possible, each with a cost:

- (a) The aggregate is a fact about the fleet, not an interface
  derivation, so it belongs in the core and ADR 0022 does not oppose
  it. Cost: ADR 0022's "facts only" boundary becomes fuzzy; every future
  aggregate will claim the same status.
- (b) The aggregate is a derivation, so it lives in a distinct surcouche
  that reads the event log and serves the page, leaving the core
  intact. Cost: an extra moving part, an extra read path, and the
  summary is no longer in the core.
- (c) The aggregate is computed by the consumer (the relay, the agent),
  and Toise provides only bounded counts and neighborhoods. Cost: every
  consumer re-implements the aggregation; the original problem returns.

Reading (b) is retained. It is not a compromise between the two ADR;
it is the reading that both ADR are already written to support. ADR
0021 is about where human-facing formatting happens, and that
formatting stays in the page and the relay. The surcouche, for its
part, only renders reports and perimeters: it makes no presentation
decision. It is on this ground that it does not violate ADR 0021 —
not because it sits "at the edge": it ships inside the binary, and
that must be said plainly.
ADR 0022 says the engine stores
facts and derivation is a surcouche, and that is exactly what is
built: the core keeps storing facts, and the aggregate is a surcouche
that derives from the journal. Readings (a) and (c) are rejected: (a)
would blur the facts-only boundary, and (c) would push the aggregation
back onto every consumer and resurrect the problem this ADR exists to
remove.

### The network dimension is not an attribute

Section 2 describes detail-on-demand as `find_entities` with a cursor
"restricted to the bucket's key". That is true for the dimensions that
are attributes of the host entity: site, datacenter, application, entity
type, criticality. It is false for the network dimension.

There is no network attribute on the host. An address is an entity two
hops away:

    host -has_interface-> network.interface <-bound_to- network.address

No mask is emitted anywhere, and the type `network.segment` exists in
the model without any producer populating it today for these fleets.
Consequently, a grouping by network cannot be served by an attribute
filter, and neither can its detail. It requires a graph traversal, and
it depends on a definition of "a network" that nobody has settled.

The consequence: the network dimension costs strictly more than the
other dimensions, and it depends on a modeling decision that is still
open. The proposal is therefore to ship the attribute dimensions first
and the network dimension separately, behind its own modeling decision.

### The three defects, and which one this decision leaves open

The current page has three named defects. First, the all-or-nothing
behavior over 179 requests: one failed page discards 178 successful
ones and leaves a blank canvas. Second, the silent truncation at
200,000 entities: the guard stops enumeration and the page draws a
truncated graph without naming it. Third, the unnamed scope: a valid
empty response renders as a blank canvas that does not say what it
covers (issue #596).

Applying this decision removes the first defect by construction: the
page no longer enumerates the whole fleet, so there is no 179-request
all-or-nothing batch to fail. The unnamed scope is treated by the
loading contract of section 7 of Decision: the scope is read first and
displayed permanently, so a blank canvas can always name its perimeter.
The silent truncation remains entirely open and is repaired by nothing
in this document: any bounded detail view can still be truncated and
must say so, and that fix is separate.

## Decision

What is decided here, neatly: the principle — never load the whole fleet
into the browser, load what is being looked at — the two-step contract
built on it (an aggregate overview first, then on-demand detail for what
the user actually points at), AND the placement of the aggregate
computation. The aggregate overview is a distinct surcouche, packaged
alongside the engine and not inside it: the core stores only facts,
conformément to ADR 0021 (human interfaces live at the edge) and ADR
0022 (the engine stores facts; derivation is a surcouche). Reading (b)
of the tension above is the retained one. Every section below is
written for that placement.

### 1. Aggregate overview served by the surcouche

The overview is a server-computed grouping, not a client-side reduction
of a full listing. The surcouche computes it from the event journal and
serves it; the response shape is a list of buckets, each carrying a
dimension key, a count, and a state summary, capped at a few hundred
points:

```
overview {
  dimension: "site" | "datacenter" | "type" | "app" | "criticality"
  buckets: [ { key, entities, relations, byStatus: { ok, degraded, ... } } ]
  totalEntities, totalRelations, scope: graphScope
}
```

A summary read returns, for one dimension: the list of buckets, each
with its key and its count; a total entity count for the dimension; and
a cursor handle per bucket for detail-on-demand. When a dimension has
more distinct values than the bound (a few hundred points), the tail is
grouped: buckets beyond the bound are merged into a single `other`
bucket whose count is the sum of the tails. The reader then loses the
ability to distinguish individual tail values — they are only visible
as an aggregate — and any ranking among them is impossible. The bound
is a property of the response, not of the store: the store keeps exact
counts, only the served view is truncated.

The granularity is one bucket per distinct value of the chosen
dimension, with entity and relation counts and a per-status breakdown.
The night sky is rendered as a density map over these few hundred
points, not over individual entities. A density map built from these
buckets can lie: a dense zone is not a failed zone. Density says "many
entities here", not "many problems here", and the overview must not be
read as a health map without a separate signal. What the consumer can
deduce: where the mass of the fleet sits, which sites or applications
dominate, and the health distribution per bucket. What the consumer
cannot deduce: anything about individual entities, neighborhoods, or
topology inside a bucket; the overview is a counting surface, not a
graph. An empty overview is a valid answer only when accompanied by
`graphScope`, so the page can name the perimeter it is looking at.

Beyond a volume threshold, the page wants to draw an aggregate instead
of the detail, in two precise forms. First, an aggregate by type AND by
host: the number of entities per type, and for each host the number of
its children, rendered as "N hosts · M containers…"; when empty:
"no entities of this kind". Note that the per-host aggregate is a
CHILD COUNT, therefore an aggregate over a relation and not over an
attribute — unlike the attribute dimensions of this section, it counts
edges, not values, and it cannot be served by an attribute filter.
Second, a detail by host on demand: the depth-2 neighborhood of an
identity, displayed on double-click on an aggregated node. This detail
depends on a traversal that GraphQL does not offer (issue #597), so it
is specified here but not deliverable as long as #597 is not handled.

The surcouche serves this response shape directly; there is no consumer
-side recomputation, because the surcouche is the component that owns
the aggregation.

### 2. On-demand detail

A click or a zoom loads the contents of one bucket or one window. The
page imposes a ceiling on the number of drawn elements; the value of
that ceiling is not known today, and it will be fixed by measuring the
cost of drawing — not the other way around. The ceiling is a target to
be set once measured, not a guarantee: nothing in this document
promises a specific number of on-screen elements. Two cost properties
must be stated in order, because they do not say the same thing. First,
the adjacency walk is O(degree): walking the neighborhood of an entity
is a property of the index, so the neighborhood part of the detail is
cheap by construction. Second, the pagination is not: the current
resolver materializes and sorts the whole type at each page, so the
list part of the detail is not yet cheap — a bounded page is not yet a
bounded cost. The detail is therefore good value only for the
neighborhood part, and not yet for the list part.

For attribute dimensions, the detail response is the existing
paginated connection (`find_entities` with cursor, capped at 200 per
page) restricted to the bucket's key, plus the adjacency edges of the
returned entities. For the network dimension, detail requires a
traversal, not a filter; see the dedicated subsection in Context. The
on-screen cap is a rendering constraint, not a server constraint; the
server keeps its 200-per-page cursor pagination, which is stable across
replicas and survives a failover, with the known caveat that it is not
a snapshot: an entity created or deleted between two pages appears or
disappears. The caveat from Context applies here too: a bounded page is
not yet a bounded cost, because the current resolver sorts the whole
type per page. And the transport caveat applies as well: the page's
GraphQL surface has no traversal (issue #597), so the neighborhood
half of the detail has no API on the page's transport today.

The surcouche does not change the detail path: the bucket restriction
is still served by the existing bounded reads, and the surcouche only
supplies the counts and the bucket keys that the detail is restricted
to.

### 3. Search before drawing

Search happens before any drawing: the query returns a neighborhood,
server-side filters, and counts, and only then does the page render.
The empty result is a first-class state: it renders as an explicit "no
match within scope" message carrying the `graphScope` that was
searched, not as a blank canvas. The truncated result is also a
first-class state: when the neighborhood or the bucket exceeds the
on-screen cap, the page says "showing N of M, zoom to narrow", with M
coming from a server-side count. The `match` filter remains strict
equality on identity and attributes, applied in memory after the type
scan; the proposed prefix filter (issue #595) has exactly the same cost
shape, since the scan already happens and the prefix only saves
serialization and transport.

### 4. Incrementally maintained summaries

Summaries are maintained as events arrive, instead of being recomputed
on every open. This is the most important part of the decision. The
surcouche consumes the event journal by sequence number (section 8),
and each event that changes an entity's dimension membership updates
the affected buckets of the corresponding summary on the surcouche's
own consumption path, so a read of a summary sees the state the
surcouche has reached — which may lag behind the journal, and whose
agreement with it is verified by reconciliation, not guaranteed by
construction. The write cost lands on the surcouche's consumption
path: each event updates the affected buckets in memory, which is
acceptable because the projection is already in memory.
The update cost of a
summary is an unmeasured estimate: O(1) per bucket touched. Nobody has
measured it.

Consistency with the append-only journal is NOT preserved by
construction of the mechanism. It is VERIFIED by reconciliation, and
the summary is a derived state that can diverge. Reconciliation
compares, per dimension and per bucket granularity, the summary counts
against counts recomputed from the event log. When a discrepancy is
found, it is surfaced explicitly — logged and exposed as a
reconciliation metric — and the summary is rebuilt from the log. A
silent discrepancy is worse than no summary at all: a summary that
quietly disagrees with the log is a lie the operator cannot detect,
whereas the absence of a summary is at least honest. Tenant isolation
is inherited from the journal, which is partitioned per tenant, so
summaries are per tenant by construction.

Under replay and under `asOf`, summaries are rebuilt from the replayed
graph rather than trusted: `asOf` is a full replay of the tenant
journal into a fresh graph, bounded at NumCPU/2 concurrent replays per
process, and its cost follows the size of the journal, not the
requested distance — measured 1.5 s at 5.6 million events, and HTTP 503 at
all distances under ingestion load (issue #585). Therefore `asOf` views
recompute their summaries from the replayed state and never read the
live counters.

The surcouche owns these maintained summaries; the consumer never
recomputes counts on each request, because the surcouche already
carries them.

### 5. The performance contract does not exist yet

The figures of 2 s for the overview and 1 s for the detail at one
million entities are TARGETS TO BE MEASURED, not a contract. They
become a contract only after the bench has produced numbers; until
the bench runs at one million entities, they are not promises. An
unmeasured contract is worthless: every number in this ADR that is not
attributed to a measurement is marked as an unmeasured estimate to
verify.

A synthetic load bench generates fleets of 100,000 then 1,000,000
entities that resemble a real fleet: a realistic degree distribution,
the measured average of 47 entities per host, and a realistic
proportion of relations. A uniformly synthetic fleet would measure the
wrong cost: uniform graphs have no hot buckets, no tail behavior, and
no skew, so they understate the cost of bucket updates and overstate
the usefulness of the bound. The synthetic fleet must reproduce the
asymmetry of a real fleet — the skew, the hot grappe, the tail — to be
a meaningful measurement. The bench measures the surcouche, at
100,000 then 1,000,000 entities:

- the initial build time from a snapshot, and the catch-up time by
  `ScanFrom` over a given backlog;
- the per-event cost in steady state, on the subscription path;
- the memory and disk consumption of the summary store, UNKNOWN today;
- the latency of one overview read, target under 2 s at one million;
- the latency of one on-demand detail, target under 1 s.

These targets are TARGETS TO BE MEASURED, not a contract, until the
bench has run. The bench must also measure the latency of one overview
read AS A FUNCTION OF THE FLEET SIZE, at 100,000 then 1,000,000
entities, because it is this curve — and not the value at one size —
that says whether the surcouche holds. The case was lived in
acceptance: the allocation of a page became bounded while its time
stayed proportional to the fleet, so a single value at one size would
have hidden the curve.

The bench must also measure the ceiling of drawn nodes: the number of
nodes the client can render while staying interactive. That ceiling is
not measured today; it is below a few thousand, and measuring it is a
prerequisite of this section, not a tuning detail, because the
detail/aggregate threshold and the on-screen cap both depend on it.

### 6. What changes for an MCP consumer and for issue #596

An agent reading Toise over MCP gains the same two-step contract: ask
for the overview grouped by a dimension, then ask for the neighborhood
of a bucket or a search result. The agent no longer needs to page
through `entities` and `relations` in full; it reads counts and scopes
first and expands only what it inspects. For issue #596, `graphScope`
ceases to be a cosmetic fix and becomes the first call of the page:
every view, including the empty one, is framed by the scope document
`{ tenant entities relations newestEvent oldestAnswerable }`, so a
blank canvas can always name its perimeter.

### 7. What the page does, and what the reader is told

The page owner has delivered a loading contract already in acceptance.
It is integrated here keeping the EXACT PHRASES the reader sees, in
quotes, because that is the deliverable and not the mechanism:

- `graphScope` is read first; during the read: "Reading the map
  scope…".
- The scope stays displayed permanently: "<name or tenant N> · X
  entities · Y relations · updated Z ago", or "scope unavailable".
- Entities and relations in parallel, pages of 200, each failed page
  retried twice, after 1 s then 3 s. During that time: "Loading:
  entities X / Y, relations Z / W".
- If a page still fails: "Loading interrupted: <entities|relations>,
  X of Y loaded." with a "Retry" button. The previous drawing is kept,
  and the page NEVER shows an empty canvas without a word.
- Empty scope (`graphScope` at 0 and first page empty): "Tenant <t>:
  0 entities, no events yet."

In one sentence, this contract removes the all-or-nothing behavior AND
it settles the unnamed perimeter, so two of the three defects named in
"The three defects, and which one this decision leaves open" are now
treated by the page; only the third, the silent truncation, remains
entirely open.

### 8. What the surcouche reads, and why it cannot be the read API

A surcouche must build its aggregates from something. The apparent
way is the public read API, GraphQL or MCP. It is impracticable, and
it is MEASURED: a bounded page of 200 now costs a bounded allocation
but a time still proportional to the fleet — 30 ms at one hundred
thousand entities, 318 ms at one million. These figures come from a
synthetic bench of entities built for the acceptance run of the
pagination batch, on a loaded workstation, so they are an order of
magnitude, not a reference value. The argument still holds, because
it rests on the proportionality to the fleet, not on the absolute
value.
A 
full enumeration at one million, i.e. five thousand pages, therefore
costs on the order of twenty minutes. A surcouche that rebuilt its
aggregates by enumerating the read API would hold no freshness
contract.

So decision (b) has a consequence it did not announce: the entry of
the surcouche is the EVENT JOURNAL, by sequence number, and not the
read API. It is incremental by construction, because the journal is
append-only.

The three primitives needed ALREADY EXIST, verified in the code:
`Store.ReadSnapshot()` returns a snapshot with the sequence number
reached; `Store.ScanFrom(afterSeq, fn)` resumes reading after a given
number; `Engine.Subscribe(fn)` notifies continuously in the process.
A surcouche therefore starts on a snapshot, catches up with
`ScanFrom`, then follows by subscription. Nothing to build for the
entry.

BUT this holds ONLY IN THE PROCESS. No network surface offers a
resume by sequence number: the GraphQL subscriptions (`entityChanged`,
`relationChanged`) push a stream without resume, so a reconnection
loses the events of the interval — and a hole in an aggregate is
silent, it does not show in the rendered count. A surcouche OUT OF
PROCESS is therefore not realizable today without adding a sequence
resume to a network surface.

The conclusion is stated plainly: the surcouche is a distinct package
IN the binary, alongside the engine and not inside it, that consumes
the journal by sequence. The precedent exists in this repository:
`internal/canonical` is already a read surcouche placed alongside the
engine (ADR 0020), and nobody put it in the core.

For reconciliation, this gives: a surcouche that consumes the journal
by sequence can rebuild itself by REPLAYING THE JOURNAL FROM THE
BEGINNING, which is the same mechanism as `asOf` and shares its cost —
measured 1.5 s at 5.6 million events, bounded at NumCPU/2 simultaneous
replays for the whole process, and HTTP 503 under ingestion load. A
reconstruction is therefore not a free gesture: it is planned, it is
not triggered at every suspected discrepancy.

## Consequences

Warning: no real fleet today approaches one million entities. The
largest real readable fleet is 16,858 entities, about sixty times
smaller, that is between one and two orders of magnitude below the
target, so this entire document rests on an extrapolation of between
one and two orders of magnitude. Every performance figure beyond the
measured ones is an unmeasured estimate to be verified on the
synthetic bench.

Positive consequences: one fast read answers the fleet-overview
question; because the page adopts the two-step contract, the browser
never holds the fleet, so the 179-request all-or-nothing load
disappears by construction;
the dominant cost of
the page today is the drawing, and the aggregate overview exists
precisely to not draw the whole fleet; detail around a point is
O(degree) and cheap for the neighborhood part; summaries remove the
per-open recomputation of counts; detail stays cursor-based and
bounded; the core stays facts-only, because the derived state lives in
the surcouche, not in the engine. This decision does not by itself
repair the silent-truncation defect, and does not by itself improve
the cost of a bounded page — those remain separate fixes.

Negative consequences: an extra moving part and an extra read path —
the surcouche; the network dimension is deferred behind its own
modeling decision; reconciliation is a permanent operational cost;
write amplification on the surcouche's consumption path for summary
maintenance; a synthetic bench that must be built and run before any
contract number is real; the drawn-node ceiling is unmeasured and gates
both the on-screen cap and the detail/aggregate threshold; the detail
step has no API on the page's transport (issue #597); a surcouche out
of process is not realizable until a network surface offers a resume by
sequence number; and the silent-truncation defect remains as a
separate fix.

The memory and disk consumption of the summary store is unknown today:
the 130 MB RAM and 28 MB disk figures measured so far belong to the
in-memory projection of a 10,000-machine fleet, not to a summary store,
and the benchmark scale has never been measured. Measuring the summary
store's consumption is part of what the bench must do.

### The first lot

- Attribute dimensions ONLY: site, datacenter, application, entity
  type, criticality. These are counts over attribute values.
- The network is DEFERRED behind its own modeling decision, for the
  reason already written in this document: it is not an attribute, an
  address is two hops away, no mask is emitted, and "a network" is
  not defined.
- The child count per host, requested by the page, is an aggregate over
  EDGES and not over values, so it is NOT part of the first lot of
  attribute dimensions and requires its own treatment.
- And the "detail on demand" half depends on a traversal that GraphQL
  does not offer (issue #597), so it is specified but not deliverable
  as long as #597 is not handled.

### Deliberately not done

Indexing the grouping attributes (`entity.location.site`,
`service.criticality`, and the others) is deliberately not done in this
ADR: grouping is served by maintained summaries, not by new attribute
indexes, because the dimensions are few and the summaries already carry
the counts. Prefix search is deliberately not adopted as a fix for
anything here: issue #595 keeps the same scan cost and only saves
transport. Client-side rendering of the full graph is deliberately not
done: it is the behavior this ADR removes. Replacing `asOf` replay with
incremental snapshots is deliberately not done: replay is the
consistency mechanism, and its journal-size cost is accepted. Shipping
the network dimension in the first version is deliberately not done:
it costs strictly more than the attribute dimensions and depends on an
unsettled definition of "a network". Putting the surcouche inside the
engine is deliberately not done: ADR 0022 keeps the core facts-only.

### Open questions

- Which dimensions become first-class overview dimensions beyond site,
  datacenter, type, app, and criticality; network grouping needs a
  decision because no network mask is emitted anywhere today.
- The reconciliation interval and the alert threshold for summary
  divergence are not decided; they need a maintainer decision.
- Whether the on-screen detail cap is a fixed constant or adapts to
  measured client rendering cost is not decided.
- Whether the 2 s / 1 s contract targets are confirmed or revised once
  the 100,000-entity bench has produced numbers is not decided.
- Whether a persistent, ordered pagination index per type is needed, so
  that a page costs the page and not the fleet, is not decided; the
  bench curve of one-page latency versus fleet size should answer it.
- The threshold beyond which the page switches from detail to
  aggregate is not decided, and it cannot be decided before the
  drawn-node ceiling is measured.
- Whether a network surface should gain a resume by sequence number,
  so that a surcouche can run out of process, is not decided.
```
