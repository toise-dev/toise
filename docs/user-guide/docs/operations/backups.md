# Backups

Toise's state is the per-tenant event log under `--data-dir`. Two mechanisms cover it:

## Cold backup: `toise-server checkpoint`

```bash
toise-server checkpoint --data-dir /var/lib/toise/data /backups/toise-2026-06-10
```

Takes a consistent [Pebble checkpoint](https://pkg.go.dev/github.com/cockroachdb/pebble#DB.Checkpoint) of **every tenant** into `<destination>/<tenant>/`. Each checkpoint directory is a complete, openable data dir: point a `toise-server` at it (or copy it back) to restore.

Run it **while the server is stopped** — a running server holds the Pebble lock and the command fails cleanly rather than producing a torn copy.

## Live restart acceleration: projection snapshots

`--snapshot-interval` (default `5m`) periodically writes a projection snapshot *inside* the store so a restart replays only the tail; a final one is written at graceful shutdown. It is a restart optimization, not a backup: it lives in the same directory it would have to protect.

## What to back up

| Concern | Answer |
|---|---|
| Scope | the whole `--data-dir` (all tenants), or per-tenant checkpoint dirs |
| Consistency | guaranteed by the checkpoint (atomic at a sequence) |
| Restore | start `toise-server --data-dir <checkpoint>` — the projection rebuilds by replay |
| Frequency | the log is append-only; back up at the cadence your retention window requires |
