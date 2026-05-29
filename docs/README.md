# Toise Documentation

This directory holds the design notes, architecture decisions, and roadmap for
Toise. The public website is at [toise.dev](https://toise.dev).

## Contents

- [**Architecture Decision Records**](./architecture/adr/) — the log of
  significant design decisions and their rationale, in
  [Michael Nygard's ADR format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).
- [**Data model**](./data-model/) — the entities Toise tracks, their
  relationships, and how they map onto the OpenTelemetry entity data model.
- [**Roadmap**](./roadmap.md) — planned milestones and their rough timing.

## How the docs are organized

Decisions that shape the system — storage pattern, data model, public
contracts — are recorded as ADRs so the reasoning survives the people who made
them. Reference material that describes the system as it is (the data model,
operational guides, API references) lives alongside the relevant topic and is
kept current as the code evolves.

If you are contributing a change that affects architecture or a public
contract, add or update the relevant ADR as part of the same pull request. See
[CONTRIBUTING.md](../CONTRIBUTING.md).
