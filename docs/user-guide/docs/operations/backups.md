# Backups

Toise's state is the per-tenant event log under `--data-dir`. Two mechanisms cover it:

## Cold backup: `toise-server checkpoint`

```bash
toise-server checkpoint --data-dir /var/lib/toise/data /backups/toise-2026-06-10
# or resolve data_dir from the server's config file:
toise-server checkpoint --config /etc/toise/toise-server.yaml /backups/toise-2026-06-10
```

Takes a consistent copy of **every tenant** into `<destination>/<tenant>/`. Each checkpoint directory is a complete, openable data dir: point a `toise-server` at it (or copy it back) to restore.

The data dir resolves exactly like the server's: defaults, then the config file (`--config` or `TOISE_CONFIG`), then `TOISE_DATA_DIR`, then `--data-dir`.

The command opens the stores **read-only** and refuses to run against a data dir that does not exist or holds no tenant stores — a typo'd path errors out instead of silently backing up a freshly created empty store.

Run it **while the server is stopped** — a running server holds the Pebble lock and the command fails cleanly rather than producing a torn copy.

## Scheduled online backups: `backup_dir` + `backup_interval`

For a **running** server, set `backup_dir` and `backup_interval` (off by default) to checkpoint every tenant on a cadence — Pebble's lock-free online checkpoint, so no downtime:

```
backup_dir: /var/lib/toise/backups
backup_interval: 1h
```

Each run writes `<backup_dir>/<UTC-timestamp>/<tenant>/` — a complete, openable data dir per tenant. Sync `backup_dir` **off-node** (rsync/object-store) and rotate it; Toise writes, it does not prune or ship. This is the in-process complement to the `checkpoint` subcommand (which needs the server stopped). Failures are logged per tenant and never interrupt serving (ADR 0029).

## Continuous log shipping: `log_shipping_dir` + `log_shipping_interval`

A scheduled checkpoint copies the **whole** store each run; its recovery point is one `backup_interval`. **Log shipping** is the finer-grained complement: on a cadence it exports only each tenant's **new event-log tail** since the last ship, as an immutable segment — so the recovery point collapses to one `log_shipping_interval` (seconds, if you want), at a fraction of the bytes.

```
log_shipping_dir: /var/lib/toise/segments
log_shipping_interval: 30s
```

Each run writes `<log_shipping_dir>/<tenant>/<from>-<to>.seg`, contiguous by sequence. The cursor is **derived from the destination itself** (the highest shipped sequence), so it needs no local state and a restart or crash never duplicates or skips a segment. The directory may be a **mounted object-store bucket, an NFS export, or an rsync staging dir** — this is the on-prem / dependency-free path of the object-store-backed log (ADR 0029); a native S3-class driver is the next step. Off by default; failures are logged per tenant and never interrupt serving.

Shipping and the cold/scheduled checkpoint are complementary: the checkpoint is a coarse, instantly-openable full copy; the segments are a fine-grained, append-only tail. Keep both for a small RPO **and** a fast restore.

## Restore (runbook)

1. Stop the server (or start a fresh node).
2. Choose a backup: a timestamped dir under `backup_dir`, or a `checkpoint` destination. It already has the `<tenant>/` layout of a data dir.
3. Point the server at it: `toise-server --data-dir <backup>` (or copy it to the data dir). On start the projection **rebuilds by replaying the log** (a snapshot inside accelerates it; an unreadable one falls back to full replay).
4. The live graph re-converges from producers within one heartbeat interval; history/time-travel is whatever the restored log holds.

Shipped **segments** (`log_shipping_dir`) are the fine-grained tail between checkpoints: restore the most recent checkpoint, then the segments after it carry the events up to the last ship. A `restore-log` command that replays segments into a data dir is the next step; until it lands, recover from the latest checkpoint and treat the segments as the low-RPO ledger of what came after.

## High availability

Toise is single-writer and the live graph is **derivable** (producers re-assert every heartbeat), so read HA needs no clustering: run **N identical instances** behind a load balancer, each ingesting the same OTLP fan-out and rebuilding its own projection (the "run two Prometheus" pattern). Point live queries at any replica; point **history/time-travel** queries at a node backed by the durable log. No Raft, no ring (ADR 0029).

**RPO/RTO.** With scheduled backups, the recovery-point is at most one `backup_interval` (plus your off-node sync lag); **log shipping shrinks the recovery point to one `log_shipping_interval`** (seconds-scale) at a fraction of the bytes. Recovery-time is a process start plus the projection rebuild (bounded by one heartbeat window for the live graph). A read replica that is already running has effectively zero RTO for live queries.

## Live restart acceleration: projection snapshots

`--snapshot-interval` (default `5m`) periodically writes a projection snapshot *inside* the store so a restart replays only the tail; a final one is written at graceful shutdown. It is a restart optimization, not a backup: it lives in the same directory it would have to protect.

An unreadable snapshot never blocks startup: the server logs a warning and **falls back to a full replay** of the log (the source of truth), then writes a fresh snapshot. To clear a bad snapshot explicitly — stopping the warning — run, with the server stopped:

```
toise-server drop-snapshot --data-dir /var/lib/toise/data
```

It deletes every tenant's snapshot without touching the event log; the next start rebuilds the projection by full replay.

## What to back up

| Concern | Answer |
|---|---|
| Scope | the whole `--data-dir` (all tenants), or per-tenant checkpoint dirs |
| Consistency | guaranteed by the checkpoint (atomic at a sequence) |
| Restore | start `toise-server --data-dir <checkpoint>` — the projection rebuilds by replay |
| Frequency | the log is append-only; back up at the cadence your retention window requires |
