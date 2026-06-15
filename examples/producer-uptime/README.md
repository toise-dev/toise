# producer-uptime

Probes a list of URLs on an interval and emits each as a `service.endpoint`
entity (identity `{url}`) whose descriptive state — `up`, `status_code`,
`latency_ms` — flips as the site goes up or down. The kind of uptime check many
people already script, wired into Toise with [`pkg/emit`](../../pkg/emit).

## Requirements

`service.endpoint` is **not** a built-in entity type, so start the server with
`--accept-unknown-types`:

```sh
./bin/toise-server --data-dir /tmp/toise-data --debug-ui --accept-unknown-types
```

## Run it

```sh
go run ./examples/producer-uptime --endpoint 127.0.0.1:4317 \
    --urls https://example.com,https://httpstat.us/503
```

Each probe re-asserts the entity, so `up`/`status_code`/`latency_ms` track the
sites live. Point it at something you can take down and watch `up` flip. Over MCP:

```
find_entities  type=service.endpoint
```

## What to copy

- Identity is the stable `url`; everything that changes per probe is a
  descriptive attribute. Putting `up` or `latency_ms` in identity would mint a
  new entity on every change.
- The probe interval doubles as the heartbeat; the liveness `Interval` carries a
  few probes' slack so a transient miss does not expire the entity.
