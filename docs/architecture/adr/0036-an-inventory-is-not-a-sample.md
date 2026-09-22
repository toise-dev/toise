# 36. An inventory is not a sample

- Status: Proposed
- Date: 2026-09-22
- Constrains: the producer contract's removal-by-absence rule
  (`docs/data-model/senhub-agent-contract.md`), within [ADR 0032](0032-connection-derived-edges-continuity-and-proxy-discontinuity.md)
  (connection-derived edges) and [ADR 0022](0022-engine-stores-facts-only.md)

## Context

The producer contract says removal is **by absence**: an item a source stops
listing on its next state event is removed. That rule rests on a premise stated
one sentence later — the producer "emits each entity's *full current*
relationship set every state event". Absence means removal **only because the
emission is claimed to be exhaustive**.

A production case shows what happens when it is not. On one host, the
`nginx` service instance was created and deleted **nine times in sixty-three
hours**, absent between twenty minutes and eight and a half hours at a stretch.
Eight of the nine removals carried `delete_source=producer` — the strongest
signal in the taxonomy, an assertion that the producer saw it gone. nginx had in
fact been running continuously since 9 July, and its ten listeners were reported
without a single interruption throughout.

The mechanism, found with the operator: that producer derives service instances
from a *dependency* source that samples outbound connections every five minutes
and debounces over two samples. nginx makes only brief outbound calls, one at a
time. The sampler catches them in bursts. So the instance appears when two
consecutive samples happen to catch a connection and is retracted after two that
do not — a pattern that follows traffic, not existence.

The producer was not lying. It reported what it saw. The error is in the
contract: **it let a sample be read as an inventory.** And the failure is
expensive precisely because the resulting signal is confident — a
`producer`-sourced removal is what a consumer trusts most.

Generalized, and this is the useful form: **some things are structurally present
but only intermittently observable.** An HTTP dependency between two services
exists continuously; a connection carrying it exists for milliseconds at a time.
Sampling the second and reporting it as the first turns a stable fact into a
blinking one. The same shape covers anything observed by polling a transient:
short-lived flows, cron-driven processes, on-demand workers, a backup that runs
nightly.

Toise today has one way to say a thing exists and one way to say it is gone. It
has no way to say *"this exists continuously and I see it now and then"* — so
producers say the only thing they can, and say it wrongly.

The producer's own maintainer put the distinction more sharply than this ADR
first did, once the case was in front of both of us, and it is worth quoting
because it is the shortest form of the rule: *the listener inventory is
exhaustive by construction — you read the complete table of listening sockets,
so an absence there is a real absence. Sampling outbound connections is not.
Treating them the same is the error.* Two sources, one of which can be read
negatively and one of which cannot, and nothing in the contract said which was
which.

## Decision

1. **Removal-by-absence is valid only for an exhaustive enumeration.** A
   producer may omit an item to mean "gone" only when its emission enumerates
   everything of that kind that exists at that instant. This is made an explicit
   precondition of the contract rather than an assumption buried in its
   rationale.

2. **A producer that samples a transient MUST NOT retract by absence.** An empty
   sample is not evidence of absence; it is the absence of evidence, and the two
   have been conflated in production. A sampling producer has exactly two honest
   options: keep asserting the fact at a cadence sized to the *phenomenon* rather
   than to the *scrape*, or not assert it at all.

3. **Observation frequency becomes a property, not a life cycle.** Where a
   relationship is derived from sampling, the honest model is not a node that
   blinks but a node that persists carrying *when it was last observed* and *how
   often*. A dependency seen in twelve of the last two hundred samples is one
   fact with a measure, not twelve creations and twelve deletions. Consumers ask
   "how alive is this edge", which a measure answers and a boolean cannot.

4. **The distinction is declared by the producer, not inferred by Toise.** Toise
   records facts and does not guess at their epistemics (ADR 0022). A producer
   states whether an emitted set is an inventory or a sample; Toise applies
   removal-by-absence to the first and never to the second. Inferring
   "this looks like sampling" from churn would be exactly the kind of clever
   guess this project refuses elsewhere.

5. **Nothing is inferred retroactively.** Existing producers emit inventories by
   default, as the contract has always assumed, so the rule changes no current
   behaviour until a producer declares otherwise.

## Consequences

- **The defect becomes stateable rather than mysterious.** "Your producer
  retracts by absence on a set it only sampled" is a diagnosis a maintainer can
  act on. Today it surfaces as an entity that flaps for no visible reason, and
  the investigation costs hours.
- **A whole class of false disappearances goes away** — every dependency derived
  from polling transient connections, which is most of them.
- **Consumers gain a truer question.** "Does this edge exist" is the wrong
  question for a flow; "when was it last observed, and how often" is the right
  one, and the answer stops changing every time traffic pauses.
- **The producer contract gains a precondition it always relied on.** Nothing
  that honours it changes; what changes is that violating it is now a contract
  violation instead of an undocumented trap.
- **This does not fix the consumer-side blindness on its own.** An entity that
  flaps still fragments its own history across re-minted ids, so the flapping
  stays invisible to whoever asks for that entity's history. That is a separate
  defect, and it is what hid this one.

See also: ADR 0019 (per-producer reference counting), ADR 0022 (the engine
stores facts only), ADR 0032 (connection-derived edges — its rule that a proxy
bridge is modelled "at durable routing granularity, never per live connection"
is the same principle, stated for one case; this ADR generalizes it).
