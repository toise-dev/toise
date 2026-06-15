# producer-minimal

The smallest useful Toise producer. It emits a `host` and a `service.listener`
that `runs_on` it, keeps them alive on a heartbeat, and deletes them on Ctrl-C.

It uses only the built-in entity vocabulary, so it runs against a stock
`toise-server` with no extra flags — the best starting point for writing your own
producer with the [`pkg/emit`](../../pkg/emit) SDK.

## Run it

Start a server (OTLP/gRPC on `:4317`, debug UI on):

```sh
./bin/toise-server --data-dir /tmp/toise-data --debug-ui
```

Then, in another terminal:

```sh
go run ./examples/producer-minimal --endpoint 127.0.0.1:4317
```

You should see your host and its service listener appear in the debug UI
(`http://127.0.0.1:8080/`), and over MCP:

```
find_entities  type=host
get_neighbors  entity_id=<the host id>   # shows the service.listener via runs_on
```

Press Ctrl-C: the producer emits `entity.delete` for both entities and they leave
the graph. (If you kill it without Ctrl-C, they expire on their own once the
liveness `--interval` elapses with no heartbeat.)

## What to copy

- `emit.New(...)` with a stable `ServiceInstanceID` — your producer's liveness key.
- Identity (`Entity.ID`) is the *exact* set of identifying attributes; keep
  changing values (status, metrics) in `Attributes`.
- The heartbeat loop re-asserts state well below `Interval`; the deferred
  `Delete` on shutdown is the graceful path.
