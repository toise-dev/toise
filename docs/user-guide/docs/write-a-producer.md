# Write a producer

Toise is fed entirely by **OpenTelemetry entity events** ([Ingesting data](ingestion.md)).
A *producer* is anything that emits those events — a host agent, a network
scanner, a cloud inventory job, or a small script that maps a source you care
about. Writing one is the highest-leverage way to contribute to Toise: each new
producer widens what the graph can see, and it is self-contained — you don't need
to understand the engine internals.

This guide uses **`pkg/emit`**, the Go producer SDK that ships with Toise. It
builds and exports spec-correct entity events for you, so you never hand-roll the
wire contract.

!!! tip "Looking for a first contribution?"
    The [good first issues](https://github.com/toise-dev/toise/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
    include several "add a producer example" tasks (Docker, systemd, uptime, …).
    Pick one, or bring your own source.

## The SDK in one minute

`pkg/emit` gives you a `Client` and two verbs:

- `client.State(ctx, entities...)` — assert that these entities exist now
  (one `entity.state` event each, in a single OTLP export).
- `client.Delete(ctx, entities...)` — assert that these entities are gone.

An `Entity` is a type, an identifying map, descriptive attributes, an optional
liveness interval, and optional relationships:

```go
type Entity struct {
    Type          string            // e.g. "host", "service.listener"
    ID            map[string]string // identity — every key/value counts (exact match)
    Attributes    map[string]string // descriptive, non-identifying
    Interval      time.Duration     // liveness backstop; re-assert at least this often
    Relationships []Relationship    // edges this entity owns
}

type Relationship struct {
    Type       string            // e.g. "runs_on"
    TargetType string            // the target entity's type
    TargetID   map[string]string // the target entity's identity
}
```

## A complete minimal producer

A producer that asserts a host, a service listening on it, and the `runs_on`
edge between them — then heartbeats so Toise keeps them alive:

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/toise-dev/toise/pkg/emit"
)

func main() {
    client, err := emit.New(emit.Options{
        Endpoint:          "127.0.0.1:4317", // toise-server OTLP/gRPC
        ServiceName:       "my-producer",
        ServiceInstanceID: "my-producer-1",  // stable per instance: the liveness key
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    host := emit.Entity{
        Type:       "host",
        ID:         map[string]string{"host.id": "srv-001"},
        Attributes: map[string]string{"host.name": "web-1", "os.type": "linux"},
        Interval:   60 * time.Second,
    }
    svc := emit.Entity{
        Type:       "service.listener",
        ID:         map[string]string{"host.id": "srv-001", "server.port": "443"},
        Attributes: map[string]string{"server.address": "10.0.0.1"},
        Interval:   60 * time.Second,
        Relationships: []emit.Relationship{{
            Type:       "runs_on",
            TargetType: "host",
            TargetID:   map[string]string{"host.id": "srv-001"},
        }},
    }

    // Heartbeat well below the declared Interval, or the liveness sweep expires
    // the entities between beats.
    ticker := time.NewTicker(6 * time.Second)
    defer ticker.Stop()
    for {
        if err := client.State(context.Background(), host, svc); err != nil {
            log.Printf("emit: %v", err)
        }
        <-ticker.C
    }
}
```

Run a server and point the producer at it:

```bash
./bin/toise-server --data-dir ./live-data &
go run ./examples/producer-minimal   # or your own main package
```

The host, the service, and the `runs_on` edge appear in the
[debug UI](querying/debug-ui.md), over [GraphQL](querying/graphql.md), and via
[MCP](querying/mcp.md).

## Getting identity right

Identity is matched **exactly** — every key and value in `ID` counts. The one
rule that matters: **put values that change in `Attributes`, not `ID`.**

- A host's stable id (`host.id`, a serial, a UUID) belongs in `ID`.
- Its current address, load, or last-seen state belongs in `Attributes`.

If a volatile value is in the identity, every change forks a brand-new entity.
Identity maps must also be **flat scalars** — pre-flatten with dotted keys
(`{"server.address": "10.0.0.1"}`, never a nested map). See
[the data model](data-model.md#two-identities-not-one).

## Liveness: heartbeat and delete

Two mechanisms keep the graph fresh:

1. **Explicit delete.** When you know an entity is gone, call
   `client.Delete(ctx, entity)`. Toise releases your producer's reference to it.
2. **Interval backstop.** Set `Entity.Interval` and re-assert (`State`) more often
   than that interval. If your producer dies without sending a delete, the sweep
   expires the entity after the interval — so the graph self-heals.

Heartbeat comfortably below the interval (e.g. interval 60s, heartbeat 6s) to
survive jitter and a missed beat.

## Relationships

Relationships ride **embedded** on the source entity (the one that carries them
in `Relationships`); removal is by absence on the next `State`. Embedded edges are
**attribute-free** — anything describing *how* two things relate becomes an entity
(a port is a `network.interface`, a route a `network.route`). See
[Ingesting data](ingestion.md#relationships-are-embedded).

**The one exception: `same_as` identity beliefs.** When one source cannot be sure
two nodes are the same real thing (a network device seen as `name:sw1` by SNMP and
as `mac:…` in another device's address table), it asserts a `same_as` edge with a
`Confidence` in `[0,1]` and a `Basis` naming the evidence, instead of pre-merging:

```go
Relationships: []emit.Relationship{{
    Type:       "same_as",
    TargetType: "network.device",
    TargetID:   map[string]string{"mac": "00:11:22:33:44:55"},
    Confidence: 0.95,           // 0..1; higher = stronger belief
    Basis:      "ifPhysAddress", // what evidence justifies it
}}
```

Toise never merges the nodes; it derives a **canonical (logical-device) view** at
read time by collapsing `same_as` edges at or above a confidence threshold, leaving
the underlying exact nodes and any low-confidence evidence intact (ADR 0020). A
`same_as` edge with no valid confidence collapses nothing — the conformance kit
flags it as an advisory. `Confidence`/`Basis` are ignored on any other edge type.

## Auth, TLS, and multi-tenancy

For an exposed server, pass credentials and a tenant via `Options`:

```go
emit.New(emit.Options{
    Endpoint: "toise.example.com:4317",
    TLS:      &tls.Config{ /* ... */ },              // omit for loopback/insecure
    Headers: map[string]string{
        "authorization": "Bearer " + token,           // matches TOISE_AUTH_TOKENS
        "x-scope-orgid": "acme",                       // the tenant
    },
    ServiceName:       "my-producer",
    ServiceInstanceID: "my-producer-1",
})
```

## Handle partial rejections

If the server accepts the export but rejects some records as permanent contract
violations, `State`/`Delete` return a `*emit.PartialError`. **Do not retry it** —
fix the producer:

```go
var pe *emit.PartialError
if err := client.State(ctx, entities...); errors.As(err, &pe) {
    log.Printf("rejected %d record(s): %s", pe.Rejected, pe.Message)
} else if err != nil {
    // transport error — safe to retry
}
```

## Prove it stays spec-correct

`pkg/emit` ships a **conformance kit** (`pkg/emit/conformance`) that checks the
exact bytes your producer emits against the published contract, with a
byte-pinned fixture. Go producers wire it into their tests so a future change
can't silently drift off-spec.

Producers in **any language** can use the `toise-conformance` CLI — no running
Toise needed. Dump the OTLP `ExportLogsServiceRequest` you would send (protobuf
or JSON) and pipe it in:

```bash
go install github.com/toise-dev/toise/pkg/emit/cmd/toise-conformance@latest
my-producer --dump-otlp | toise-conformance     # exit 0 = conformant
```

It reports every contract violation and exits non-zero on rejections (add
`-strict` to also fail on advisories like a missing `service.instance.id`). See
the [Producer directory](producers.md) for the full catalog and tool usage.

## Contribute it back

Got a producer working against a source others would want? Turn it into an
`examples/producer-*` and open a PR — see
[CONTRIBUTING.md](https://github.com/toise-dev/toise/blob/main/CONTRIBUTING.md)
and the open
[good first issues](https://github.com/toise-dev/toise/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22).
