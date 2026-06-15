# producer-docker

Maps the local Docker state into Toise: one `container` entity per running
container (identity `{container.id}`, descriptive `container.image.name`,
`container.name`, `state`), the `host` they run on, and a `runs_on` edge. It
re-scans on a heartbeat and deletes containers as they disappear.

A good template for any "bring your own source" producer with
[`pkg/emit`](../../pkg/emit).

## Requirements

- The `docker` CLI on `PATH` and a running daemon (it shells out to `docker ps`).

`container` is a built-in entity type, so this runs against a stock server:

```sh
./bin/toise-server --data-dir /tmp/toise-data --debug-ui
```

## Run it

```sh
go run ./examples/producer-docker --endpoint 127.0.0.1:4317
```

Start and stop containers and watch the subgraph update: a `docker run ...`
appears within a heartbeat, a `docker stop ...` is deleted. Explore over MCP:

```
find_entities  type=container
get_neighbors  entity_id=<a container id>   # the host it runs on
```

## What to copy

- Identity is the immutable `container.id`; the changing `state` is a descriptive
  attribute, never part of identity.
- Diff the current scan against the previous one to `Delete` departed containers
  promptly, instead of waiting for the liveness `Interval` to expire them.
