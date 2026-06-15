# toise-conformance

Validate a producer's OTLP entity-event output against the Toise wire contract,
without a running Toise. Works for producers in **any language** — feed it the
bytes your producer would export.

## Install

```bash
go install github.com/toise-dev/toise/pkg/emit/cmd/toise-conformance@latest
```

## Use

```bash
# a captured OTLP ExportLogsServiceRequest (protobuf or JSON, auto-detected)
toise-conformance producer-output.bin

# or straight from stdin
my-producer --dump-otlp | toise-conformance

# force a format, and treat advisories as failures (CI)
toise-conformance -format json -strict batch.json
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Conformant — Toise accepts the records without per-record rejection. |
| 1 | Rejections found (or advisories under `-strict`); fix the producer. |
| 2 | Usage or decode error. |

*Advisory* problems (e.g. a missing `service.instance.id`, which collapses
per-producer liveness reference counting) are reported but do not fail the run
unless `-strict` is set.

Go producers can also import [`pkg/emit/conformance`](../../conformance) and
check the `plog.Logs` they build directly in their tests, against the same
byte-pinned contract fixture.
