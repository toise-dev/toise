# Read-surface qualifiers — framing the 0.19.0 lot

Status: proposed, 2026-09-30. Scope: the six open issues #378, #366, #380, #364, #363, #394.
Not a design of the fixes; a frame that says what they have in common, what order they go in,
and what must not be done.

## The invariant, sharpened

#378 states it as *an answer that is empty, partial or filtered must carry the reason it is so*.
That is right and it cannot be checked. Restated so it can:

> **Every qualifier the engine already holds about an answer must be reachable on that answer.**

The engine knows the clamp it applied, the total behind a truncated page, whether a fact is
backed by a liveness promise, which scope asserted it, which interval its producer declared,
and which types carry which attributes. In each of the six issues, it holds the qualifier and
does not return it. That makes the work auditable: enumerate, per read surface, what the engine
knows and what it emits, and the gap is the backlog.

Absence of evidence and evidence of absence must not render identically — and today the
difference is held in memory rather than in the payload.

## Two families

The six are not interchangeable. They split cleanly, and the split is what makes the lot
tractable.

**A — the answer misstates its own completeness.** The result is bounded, filtered or derived,
and the bound is not legible.

| issue | the qualifier held and not returned |
|---|---|
| #366 | `entityHistory` clamps `first:` to 200 and pages oldest-first; the effective page size is known and unsaid, so the newest event is unreachable at any page size |
| #380 | the filtered total is already computed correctly; consumers do not know filters apply *before* truncation, which is the remedy to a false conclusion already recorded |
| #378 faces 1, 2, 4 | truncated windows, a derived overlay painted invisible, and a compact mode that hides every readable name |

**B — the answer misstates how strongly a fact is held.** The result is complete; its standing
is invisible.

| issue | the qualifier held and not returned |
|---|---|
| #363 | whether any reference armed a deadline. An entity nobody promised to refresh reads exactly like one refreshed a minute ago |
| #394 | the instrumentation scope, which the producer contract makes the normative provenance channel, and the declared `entity.report.interval` |
| #364, #378 face 3 | which type carries which fact. `describe_type` holds it; nothing points there at the moment an empty answer is returned |

Family A is about the shape of the answer. Family B is about its warrant. A consumer
misled by A over-trusts a sample; a consumer misled by B over-trusts a fact. Both end in a
confident wrong conclusion, which is why they belong in one lot.

## The method rule, and it is not negotiable

**A fix that adds a warning is not a fix.** #378 records the evidence against prose in the
sharpest possible form: its author read the MCP instructions daily and still committed three of
the five occurrences. The instructions already warn about several of these traps.

What worked, in ADR 0035, was making the data carry its own caveat so the warning no longer
had to be recalled. ADR 0036 did the same on the producer side. This lot is the same move
applied to emptiness and to warrant: **remove the trap, do not document it.**

This has a consequence worth stating, because it revises an issue in this lot. #364's own
suggested shape is to extend the server `instructions` with a paragraph about the grain trap.
By the rule above that is the weakest of the six proposals, and it comes from the issue that
best documents why prose fails. The strong version is data: an empty result for a well-formed
query names the scope it searched and the sibling types that exist — "no `service.instance`
matched; this instance also holds `container`, `db`, `service.listener`". The prose paragraph
can stay, but it is not the fix.

## Order, by cost

This is the part that makes the lot decidable rather than a wish.

**1. Additive, no store change, closes four of six faces.**

- #363 — mark an entity whose references all lack a deadline. Additive field plus one plain
  sentence in the MCP payload, in the style of the `disappearance` gloss.
- #366 — state the effective page size or refuse an over-cap `first:`; document the cap in the
  schema description; make the recent end reachable in one call (`last:` or `order:`); mirror
  the `covered` admission `recentChanges` already carries.
- #364 strong version — an empty result names the searched scope and the sibling types.

**2. Half of #394 is nearly free; the other half is not.**

Per-producer reference counting (ADR 0019) already holds, per entity, the set of scopes
asserting it. Surfacing that on `Entity` is close to reading a map the engine maintains.
Surfacing the scope on `ChangeEvent` is the more useful half for an incident — "who observed
this change" — and may require carrying the scope on the stored event, which is a bigger
change. Check against ADR 0019 before choosing; do the cheap half first and see whether it
answers the questions people actually ask.

The declared interval belongs here too: it is the same shape, a value the producer states in
every event that no surface returns. It cost a wrong conclusion on 2026-09-30, when a silence
of 1771 s was read as overdue against an interval derived from a *different entity type* —
the real one being 1800 s. A derived parameter needs the type attached to it, and a readable
parameter needs no deriving.

**3. #380 last, and separately. It is a store-format change.**

The attribute filter needs the entity id in the time-index tag so the walk can match before
the limit. `TimeIndexEntry` carries `Seq`, `ChangeType`, `Structural`, `Tagged` and no id, and
classifying from the tag without decoding the record is the deliberate property that made the
change-read affordable at scale (#351). Adding the id makes other per-entity filters cheap
too, so it may earn its cost — but it is not in the same bucket as the rest and should not be
bundled with them.

## What must not be done

- **No invented defaults.** #363 says it plainly: a server-side default interval would be
  Toise guessing a promise the producer never made. The whole model refuses that inference
  everywhere else.
- **No fix by prose alone**, per the method rule above.
- **#380 must not resolve its id set from the live projection.** An entity deleted during the
  window is gone from the projection, so a trial filter would silently drop every deletion —
  and for an incident the entities worth looking at are the ones that died. That would
  reproduce the #378 defect *inside the feature meant to fix it*. Resolve as of the window or
  include tombstones; both are available (`tombByHash`, the as-of fold). Test it with an
  entity that dies mid-window, or the trap is untested.
- **No new field on every payload** as a blanket answer. #378 is explicit that the existing
  `covered` / `total` blocks are necessary and have twice failed to change a reader's
  conclusion. Adding a seventh block nobody reads is not progress; the qualifier has to land
  where the wrong conclusion is formed.

## The gate

Each of the five occurrences catalogued in #378 becomes a regression test, plus the two logged
since:

| occurrence | the test |
|---|---|
| truncated `recent_changes` read as a complete sample | a window whose population exceeds the limit must not render as a complete answer |
| derived edges hidden by default | a hidden overlay must be distinguishable from an empty one |
| inventory queried on the wrong type | an empty result must name the sibling types that exist |
| compact mode hides every readable name | compact must carry the readable name (shipped, #385–#389; keep the test) |
| an acceptance criterion satisfied by not looking | *not a code test* — recorded as a review rule: a criterion needs a positive-presence term |
| `entityHistory` newest event unreachable at any page size | asking for the newest event returns the newest event |
| an interval derived from the wrong entity type | the declared interval is readable, so nothing needs deriving |

The lot is done when a consumer cannot form a confident wrong conclusion from a well-formed
query without the payload contradicting them.

## Why this lot and why now

Four of the six were filed from real wrong conclusions, not from review. Two were filed on
2026-09-30 during one day of bench measurement, and that same day the qualifier gap produced
three wrong conclusions of my own — one of them published to an issue before being corrected.
The class is not rare and it is not getting rarer as surfaces are added.

It is also the class that costs the most per occurrence: every one of them ends in someone
cross-checking the graph against SSH, or escalating a coverage gap that does not exist. That
cost is paid by the consumer, which is exactly the audience the product is built for.
