# 1. Record architecture decisions

- Status: Accepted
- Date: 2026-05-29

## Context

Toise is a young project whose architecture is under active design. Significant
decisions — the storage pattern, the data model, public contracts, the
receiver runtime — will be made early and will have long-lived consequences.
Without a written record, the *reasoning* behind a decision is lost as soon as
the people who made it move on, and future contributors are left to reverse
engineer intent from code.

## Decision

We will record architecturally significant decisions as Architecture Decision
Records (ADRs), using the lightweight format introduced by Michael Nygard in
[Documenting Architecture Decisions](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).

- ADRs live in `docs/architecture/adr/`.
- Each ADR is a numbered Markdown file: `NNNN-short-title.md`, numbered
  sequentially.
- An ADR is immutable once accepted. If a later decision changes or reverses
  it, we write a new ADR and mark the old one as `Superseded by ADR-XXXX`.
- A decision is "architecturally significant" if it affects the structure,
  data model, public contracts, dependencies, or operational characteristics
  of the system.

A pull request that makes such a decision should add the corresponding ADR in
the same change.

## Consequences

- The rationale behind decisions is preserved, making onboarding and future
  changes easier and better informed.
- There is a small, deliberate overhead to making significant decisions: they
  must be written down.
- The ADR log becomes a chronological narrative of how and why the architecture
  evolved.

## Template for future ADRs

```markdown
# NNNN. Short title of the decision

- Status: Proposed | Accepted | Deprecated | Superseded by ADR-XXXX
- Date: YYYY-MM-DD

## Context

What is the issue we are seeing that motivates this decision? Describe the
forces at play: technical constraints, requirements, and trade-offs. State
facts neutrally.

## Decision

What is the change we are making? State it in active voice: "We will ...".

## Consequences

What becomes easier or harder as a result? Include positive, negative, and
neutral consequences. Note any follow-up work or risks introduced.
```
