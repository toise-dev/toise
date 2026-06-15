# Producer directory

A *producer* is anything that emits OpenTelemetry entity events into Toise — a
host agent, a network scanner, a cloud-inventory job, a small script. This page
is the catalog of producers we know about, plus the SDK and the conformance tool
you build them with. To write your own, start with [Write a producer](write-a-producer.md).

## The SDK

| | |
|---|---|
| [`pkg/emit`](https://github.com/toise-dev/toise/tree/main/pkg/emit) | The Go producer SDK. A `Client` with two verbs (`State`, `Delete`) that builds and exports spec-correct entity events, so you never hand-roll the wire contract. Versioned as a nested module (`go get github.com/toise-dev/toise/pkg/emit`). |

You do **not** need the SDK to produce — any language that can send OTLP logs
works. The SDK just removes the boilerplate and keeps you on-spec. Whatever you
use, validate the output with the conformance tool below.

## Example producers (in this repo)

Worked, self-contained examples under [`examples/`](https://github.com/toise-dev/toise/tree/main/examples).
None is compiled into the server; each talks to Toise only over OTLP ingest, the
way a third-party integration would. Start with **producer-minimal**.

| Producer | Source it maps | Vocabulary | Needs |
|----------|----------------|------------|-------|
| [producer-minimal](https://github.com/toise-dev/toise/tree/main/examples/producer-minimal) | A host and a service listener (static) | built-in | nothing — runs against a stock server |
| [producer-docker](https://github.com/toise-dev/toise/tree/main/examples/producer-docker) | Local Docker containers | built-in (`container`) | `docker` |
| [producer-uptime](https://github.com/toise-dev/toise/tree/main/examples/producer-uptime) | HTTP/website uptime of a URL list | open (`service.endpoint`) | server with `--accept-unknown-types` |
| [producer-systemd](https://github.com/toise-dev/toise/tree/main/examples/producer-systemd) | systemd service units (Linux) | open (`service`) | `systemctl` + `--accept-unknown-types` |

"Open vocabulary" producers emit entity types outside the built-in registry, so
they need a server started with `--accept-unknown-types` — see
[the open vocabulary](api-stability.md#the-open-vocabulary).

## External producers

Producers maintained outside this repo. Open a PR to add yours.

| Producer | Maintained by | What it maps |
|----------|---------------|--------------|
| [senhub-agent](https://github.com/senhub-io/senhub-agent) | senhub | A real-world host/service/network monitoring agent that emits entity events as one of its OTLP producers. |

## Validate any producer: the conformance tool

`toise-conformance` checks a producer's OTLP entity-event output against the wire
contract **without a running Toise**. It works for producers in any language —
dump the OTLP `ExportLogsServiceRequest` your producer would send (protobuf or
JSON) and pipe it in:

```bash
# install (Go toolchain)
go install github.com/toise-dev/toise/pkg/emit/cmd/toise-conformance@latest

# validate a captured batch (format auto-detected)
toise-conformance producer-output.bin

# or straight from a producer that can dump its OTLP
my-producer --dump-otlp | toise-conformance
```

It prints every violation and exits:

- **0** — conformant (Toise accepts the records without per-record rejection).
- **1** — rejections found (records Toise would drop); fix the producer.
- **2** — usage or decode error.

*Advisory* problems (e.g. a missing `service.instance.id`, which collapses
per-producer liveness) are reported but do not fail the run — pass `-strict` to
treat them as failures in CI.

Go producers can also wire the [`pkg/emit/conformance`](https://github.com/toise-dev/toise/tree/main/pkg/emit/conformance)
package directly into their tests, checking the exact `plog.Logs` they build
against the same byte-pinned contract fixture.
