# 23. Layered configuration — file, environment, flags

- Status: Accepted
- Date: 2026-06-05

## Context

`toise-server` was configured by **command-line flags only**. That is fine for
ad-hoc local runs but painful for any real deployment: a systemd unit or container
must encode every setting on the command line, there is no single artifact to
version and review, and there is nowhere to source a secret from other than an
argument visible in `ps` (relevant once authentication and TLS land — #43). The
surrounding stack follows the 12-factor convention (a config file plus environment
overrides); Toise diverged from it.

The settings are few and flat (listeners, data dir, a handful of durations), so the
need is a small, predictable layering mechanism — not a configuration framework.

## Decision

**`toise-server` resolves its configuration from four layers, lowest precedence to
highest: built-in defaults < YAML file < environment variables (`TOISE_*`) <
command-line flags.** The implementation is a small in-tree package
(`internal/config`); we do not add a configuration framework (e.g. Viper).

Concretely:

1. **Defaults** are the historical flag defaults (loopback listeners, `toise-data`
   data dir, no retention cap). Running with no file, no env, and no flags behaves
   exactly as before.
2. **File** (`--config <path>` or `TOISE_CONFIG`) is YAML. Only the keys present in
   the file are overlaid; **unknown keys are rejected** (a typo fails loudly rather
   than being silently ignored — the project's "no silent loss" principle). Durations
   are Go-duration strings (`"30s"`, `"1h30m"`).
3. **Environment** variables are `TOISE_<UPPER_SNAKE>` (e.g. `TOISE_OTLP_LISTEN`,
   `TOISE_RETENTION_MAX_AGE`). They override the file. **Secrets are sourced here and
   only here** — never required on the command line — which is the reason env sits
   above the (reviewable, committed) file.
4. **Flags** keep their existing names and override everything. Precedence is
   achieved by seeding each flag's *default* with the file+env-resolved value, so an
   unset flag keeps that value and a set flag wins — `flag.Parse` does the rest with
   no per-flag bookkeeping.

`yaml.v3` is already in the dependency tree (via the GraphQL parser), so adopting it
as a direct dependency adds nothing to the build.

`SIGHUP` live-reload of non-structural settings is **deferred**: it is genuinely
optional, and most settings here (listeners, data dir) are structural and cannot be
re-applied without a restart.

## Consequences

- A deployment can run fully from one committed YAML file, with secrets injected via
  the environment and the occasional override as a flag — matching the 12-factor
  pattern of the surrounding stack.
- The precedence is uniform and testable: `config.Load(args, getenv)` takes the
  environment as an injected function, so resolution is unit-tested without touching
  the process environment.
- The flag surface is unchanged, so existing runbooks and the recette deployment keep
  working untouched; the file/env layers are purely additive.
- Adding a setting means one struct field (with a `yaml` tag), one `TOISE_*` env
  mapping, and one flag — all in `internal/config`. The boundary stays in one place.
- Documentation: `docs/operations/configuration.md` plus the annotated
  `examples/toise-server.yaml`.
