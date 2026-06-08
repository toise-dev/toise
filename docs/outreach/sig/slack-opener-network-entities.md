# Slack opener — network infrastructure as entities (Entities SIG)

Status: DRAFT — for maintainer (Matthieu) review. Do not post.
Companion to `draft-discussion-network-entities.md` (the long write-up / one-pager).
Venue decision: Slack interest-check FIRST (#otel-entities), per the long draft's plan.

---

## (a) Ready-to-paste Slack message

Plain text, no markdown headers, ~8 lines. Humble interest-check, prior art, not a pitch.

```
Hi all — quick interest-check for the SIG. We maintain a temporal infrastructure graph built on the merged entity-events model (entity state + embedded relationships), and we keep hitting a gap: there's no first-class way to model SNMP-discovered network topology as entities — a device, an interface/port, a route — only network.* attributes. We've built a working vocabulary for this (device/interface/route + a few relations), with identity keys derived from standard SNMP MIBs and aligned with the merged data model. Two questions: (1) is network infrastructure as entities in scope / of interest to this SIG, now or later? (2) is there in-flight work we've missed — we found semconv #2399 (SNMP in the network namespace) but it looks untriaged and doesn't settle attributes-vs-entities, so we'd rather build on whatever exists than duplicate. Happy to drop a one-pager if there's appetite. Thanks!
```

Note: that renders as ~8 sentence-lines; Slack will soft-wrap. If the maintainer wants it visually shorter, the #2399 clause can move to a threaded follow-up reply. "We maintain" keeps it honest without naming Toise/senhub as a pitch (mainteneur can add the name if asked who "we" are).

## (b) Channel + meeting reconnaissance

Source of truth: `open-telemetry/community` README (main), confirmed 2026-06-08.

- **Slack channel: #otel-entities** — CONFIRMED. Lives on the CNCF Slack workspace
  (cloud-native.slack.com). Direct archive link:
  https://cloud-native.slack.com/archives/C06QEG97W7L
- **How to join CNCF Slack**: sign up at https://slack.cncf.io (self-serve), then search/join
  `#otel-entities`. General OTel community pointer: https://opentelemetry.io/community/
- **Meeting**: Entities SIG ("Specification: Entities") meets **every Monday at 09:00 PT,
  weekly** (Zoom). For CET that is ~18:00 (17:00 in PDT-vs-CEST offset weeks — confirm against
  the calendar invite, it tracks US DST).
- **Calendar / how to subscribe**: join the Google group **calendar-entities** —
  https://groups.google.com/a/opentelemetry.io/g/calendar-entities — which auto-invites you to
  the series and keeps your calendar in sync (the OTel-recommended way to follow a SIG).
- **Meeting notes**: Google Doc
  https://docs.google.com/document/d/15Yt9ss2_EhuFPqItPbk4vjfpeRDAQ5WCUVuY_kCeOAo
  (not anonymously fetchable; open while logged in).
- **Adjacent**: "Semantic Conventions: General" meets Monday 08:00 PT (the hour before
  Entities). Network/SNMP semconv vocabulary issues land in `open-telemetry/semantic-conventions`.

Sources:
- https://github.com/open-telemetry/community/blob/main/README.md
- https://opentelemetry.io/community/
- https://slack.cncf.io

## (c) Caveat closure — "is anything in flight?"

Last time the GitHub issue-search pages 504'd, leaving the "nobody is working on network
entities" claim with a gap. This pass the searches succeeded. Verdict:

**The "nothing in flight on network-as-entities" claim HOLDS — with one caveat to state
honestly, not omit.**

What exists / what does not:

- **semconv #2399 "Support snmp in the network namespace"** — OPEN, but **untriaged
  (needs-triage), unassigned, zero comments, dormant ~12 months** (opened 2025-06-20 by
  thompson-tomo). Asks to map SNMP/RFC1213-MIB data to telemetry with "type attributes" to
  avoid duplication. It does **not** decide whether SNMP data should be entities or just
  `network.*` attributes — that ambiguity is exactly the open question our model answers.
  Same author (thompson-tomo) reviewed our process-identity on the blog. We should cite this,
  not pretend the space is empty.
  https://github.com/open-telemetry/semantic-conventions/issues/2399
- semconv #2400 (thompson-tomo, "Capturing of communications") — about network comms, not
  topology/entities. Tangential.
  https://github.com/open-telemetry/semantic-conventions/issues/2400
- spec #1298 (adding resource attributes via auto-discovery), spec #4324 (recording multiple
  entities, closed) — entity *mechanism*, no network vocabulary.
- No `model/entity/network.yaml`; entity registry README has no network device/interface/
  route namespace.
  https://github.com/open-telemetry/semantic-conventions/blob/main/docs/registry/entities/README.md
- semconv #3682 (system.network.tcp/udp metrics) — metrics, not entities. Not relevant.

Bottom line: there is **no first-class network-entity modeling work in flight** anywhere in
`semantic-conventions` or `opentelemetry-specification`. The single adjacent thread (#2399) is
an untriaged, attribute-vs-entity-ambiguous SNMP request that has sat for a year — which is a
reason to engage, and a thing to reference, not a contradiction of the gap claim. The Slack
opener above folds this in directly so we're not asking a question we can already half-answer.

Searched 2026-06-08:
- https://github.com/open-telemetry/semantic-conventions/issues?q=is%3Aissue+network+entity
- https://github.com/open-telemetry/semantic-conventions/issues?q=is%3Aissue+SNMP
- https://github.com/open-telemetry/opentelemetry-specification/issues?q=is%3Aissue+network+entity

Still unverified (needs the maintainer, logged in): the **`.project#16` "Resources and
Entities" board** (404 anonymously) and the **meeting-notes doc**. A glance at those two would
make the claim fully airtight; nothing public contradicts it.
