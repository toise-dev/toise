# Submitting the blog post to opentelemetry.io

This folder holds the OpenTelemetry community blog post, prepared in the exact
shape the [opentelemetry.io blog guidelines](https://opentelemetry.io/docs/contributing/blog/)
expect. The post lives at:

```
content/en/blog/2026/consuming-opentelemetry-entity-events/index.md
```

Mirror that path inside a fork of `open-telemetry/opentelemetry.io`.

## Before you open a PR: raise an issue first

The blog process asks you to **open an issue first** and get a maintainer ack
before the PR. Use the repo's "Blog post" issue template with:

- **Title:** What can you do with OpenTelemetry entity events?
- **Description / outline:** An educational post on _consuming_ entity events:
  event-sourcing the stream, bi-temporality, immutable identity, and exposing the
  resulting graph via GraphQL and MCP. Uses one Apache-2.0 consumer (Toise) as a
  worked example, with a disclosure line; vendor-neutral throughout. Closes with
  the open relationships question (OTEP 0256 Future Work).
- **Technologies:** OpenTelemetry entity events, OTLP logs, MCP, GraphQL. All
  open source / CNCF-aligned.
- **Related SIG:** **Specification: Entities** (the spec-level SIG that owns the
  entity data model — not Semantic Conventions itself, though entity attribute
  conventions live in `semantic-conventions` under `area:entities`).
- **Sponsor:** optional but recommended — ideally a maintainer from a _different_
  company than yours. The SIG meets **Mondays 09:00 PT**; ask in Slack
  **[#otel-entities](https://cloud-native.slack.com/archives/C06QEG97W7L)** or at
  the meeting. Current SIG leads: **Tigran Najaryan**
  ([@tigrannajaryan](https://github.com/tigrannajaryan)) and **Severin Neumann**
  ([@svrnm](https://github.com/svrnm)).

Put the resulting issue number into the post's `issue:` front-matter field.

## Placeholders to fill in `index.md`

- `author:` — set to `MatthieuNoirbusson` / Sensor Factory (done).
- `issue:` — the pre-submission issue number (currently `0000`).
- `sig:` — set to `"Specification: Entities"` (verified against the
  `open-telemetry/community` SIG list). Reviewers will adjust if their convention
  differs.
- `date:` — a target date; reviewers usually set the real date at merge.
- Keep `draft: true` until it is approved — a maintainer flips it on merge.

## Prepare and validate the fork

```bash
# 1. Fork open-telemetry/opentelemetry.io on GitHub, then:
git clone https://github.com/MatthieuNoirbusson/opentelemetry.io
cd opentelemetry.io
git checkout -b blog-entity-events
npm install

# 2. Create the post folder and copy index.md from this directory into:
#    content/en/blog/2026/consuming-opentelemetry-entity-events/index.md
#    (or: npx hugo new content/en/blog/2026/consuming-opentelemetry-entity-events/index.md
#     then paste the body)

# 3. Format and spell-check (required by CI):
npm run format
npm run check:spelling   # add any flagged words to the cSpell:ignore line

# 4. Optional local preview:
npx hugo server
```

## Open the PR

- Target `open-telemetry/opentelemetry.io:main`.
- Reference the pre-submission issue ("Closes #<issue>").
- Expect review on: front-matter compliance, 80-char line wrapping, `##`-level
  headings, GitHub link stability, spelling, OTel terminology, and **vendor
  neutrality**. The post is framed to pass the last one (Toise is an example, not
  the subject; one disclosure line; no product CTA) — preserve that on edits.

## Notes baked into the post

- Spec status is "in development" (not stable); attribute names are flagged
  illustrative. Re-verify them against the current entity data model just before
  publishing.
- Links used: `/docs/specs/otel/entities/data-model/` (site-relative) and OTEP
  0256 (GitHub). Both were valid at preparation time.
- Content verified against the Toise implementation on 2026-06-02; see
  `../otel-blog-draft.md` for the working draft and its provenance notes.
- SIG name/leads/channel verified 2026-06-02 from the
  [open-telemetry/community SIG list](https://github.com/open-telemetry/community/blob/main/README.md)
  (row "Specification: Entities", anchor `#sig-entities`).
