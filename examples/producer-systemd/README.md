# producer-systemd

Maps systemd service units to Toise: one `service` entity per unit (identity
`{host.id, service.name}`, descriptive `active_state` and `sub_state`), the
`host`, and a `runs_on` edge. A realistic Linux-host source, wired in with
[`pkg/emit`](../../pkg/emit).

## Requirements

- **Linux** with `systemctl` on `PATH` (it shells out to `systemctl list-units`).
- `service` is **not** a built-in entity type, so start the server with
  `--accept-unknown-types`:

```sh
./bin/toise-server --data-dir /tmp/toise-data --debug-ui --accept-unknown-types
```

## Run it

```sh
go run ./examples/producer-systemd --endpoint 127.0.0.1:4317
```

`systemctl stop <unit>` and watch its `sub_state` flip from `running` to `dead`
within a heartbeat. Over MCP:

```
find_entities  type=service
get_neighbors  entity_id=<the host id>
```

## What to copy

- Identity is the stable `{host.id, service.name}` pair; the `active_state` /
  `sub_state` that change as units start and stop are descriptive attributes.
- The same scan-diff-delete pattern as producer-docker: units that vanish between
  scans are deleted rather than left to expire.
