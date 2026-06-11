# Contributing to Toise

Thank you for your interest in Toise. Toise aims to be the living map of an
organization's infrastructure — an OpenTelemetry-native graph of devices,
hosts, services, and the relationships between them. It is built in the open,
and contributions from the broader community are welcome.

This document explains how to get set up, how we work, and what we expect from
a contribution.

## Philosophy

Toise favors clarity over cleverness, small reviewable changes over large ones,
and decisions that are written down over decisions that live only in someone's
head. Significant architectural choices are recorded as
[Architecture Decision Records](./docs/architecture/adr/). If you are proposing
a change that affects the data model, the storage pattern, or a public
contract, expect to write or update an ADR as part of the work.

## Prerequisites

- [Go](https://go.dev/dl/) 1.26 or newer (we track the latest stable release)
- `make`
- [`golangci-lint`](https://golangci-lint.run/usage/install/)

## Getting started

Clone your fork and build:

```bash
git clone https://github.com/<your-username>/toise.git
cd toise
make build
```

Run the binary:

```bash
./bin/toise
```

Run the tests and the linter before sending anything:

```bash
make test
make lint
```

`make help` lists every available target.

## Git workflow

We use a standard fork-and-pull-request workflow against `main`.

1. Fork the repository and create a topic branch from `main`:
   `git checkout -b feat/short-description`
2. Make your change. Keep one logical change per commit.
3. Ensure `make test` and `make lint` pass locally.
4. Push to your fork and open a pull request against `toise-dev/toise:main`.

Do not commit directly to `main`. All changes land through pull requests.

## Commit messages

We require [Conventional Commits](https://www.conventionalcommits.org/). The
first line follows `type(scope): description`, in the imperative mood, with no
trailing period and a subject of 72 characters or less.

Accepted types include `feat`, `fix`, `refactor`, `chore`, `docs`, `test`,
`perf`, and `revert`. Example:

```
feat(store): add append-only event log with offset cursor
```

Explain the *why* in the body when the diff alone does not make it obvious.

## Developer Certificate of Origin (DCO)

Toise uses the [Developer Certificate of Origin](https://developercertificate.org/).
By signing off on your commits you certify that you wrote the contribution or
otherwise have the right to submit it under the project's license.

Sign off every commit with the `-s` flag:

```bash
git commit -s -m "feat(store): add append-only event log"
```

This appends a `Signed-off-by: Your Name <your@email>` trailer using your
configured git identity. Commits without a sign-off will not be merged.

## Review process

A maintainer will review your pull request. Reviews focus on correctness,
clarity, test coverage, and fit with the project's architecture. Expect a
conversation — questions and requested changes are a normal part of review, not
a rejection. Once the change is approved and CI is green, a maintainer will
merge it.

Please keep pull requests focused. A smaller, single-purpose pull request is
reviewed and merged far faster than a large one that mixes concerns.

## Releases and tags

Toise carries two release lines out of one repository (see
[ADR 0027](./docs/architecture/adr/0027-sdk-module-and-versioning.md)):

- **The server** is tagged `vX.Y.Z` at the repository root, starting at
  `v0.6.0`. The `v` prefix is what Go module tooling requires of an
  installable version; releases `0.1.0`–`0.5.0` predate it and are **not**
  retro-tagged — pushing a `v0.x.y` twin of an old release would re-trigger
  the release workflow and duplicate artifacts. The release workflow, the
  docs deployment (which strips the `v` for the published docs version, so
  URLs stay `/docs/0.6.0` style), and the Makefile all key on the v-prefixed
  tag.
- **The `toise-emit` SDK** (`pkg/emit`) is its own Go module, versioned
  independently of the server and tagged `pkg/emit/vX.Y.Z`, starting at
  `pkg/emit/v0.1.0`. Go resolves the nested path automatically: once the tag
  `pkg/emit/v0.1.0` exists,
  `go get github.com/toise-dev/toise/pkg/emit@v0.1.0` installs that exact
  version. SDK tags do not trigger the server release workflow.

Tags are cut by maintainers through the release flow; contributors never need
to create one. Note that `./...` from the repository root does not reach the
nested SDK module: `make test` and `make lint` run both modules, and CI does
the same.

## Where to ask questions

Open a GitHub Discussion once Discussions are enabled on the repository. Until
then, open an issue with the `question` label. For anything sensitive, see
[SECURITY.md](./SECURITY.md).

## Code of Conduct

Participation in this project is governed by our
[Code of Conduct](./CODE_OF_CONDUCT.md). By participating, you are expected to
uphold it.
