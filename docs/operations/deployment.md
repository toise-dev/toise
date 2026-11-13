# Deploying toise-server

Toise ships first-class release artifacts (#47): prebuilt binaries, a container
image, and packaging examples under [`deploy/`](../../deploy). The build is pure-Go
and CGO-free, so the binaries are static and the image is distroless.

> Toise binds to loopback and trusts the network by default. Before exposing it,
> enable TLS and bearer-token auth (see [Configuration](./configuration.md#authentication--tls))
> or front it with a proxy.

> `/metrics` is served without authentication by design, so an instance exposed
> beyond a trusted network must be fronted by a server that restricts it. The
> endpoint discloses the exact build version and commit (`toise_build_info`),
> tenant labels on several series, per-type entity counts
> (`toise_entities`, `toise_entities_by_type`), and event counts and on-disk size
> (`toise_events_total`, `toise_store_disk_bytes`) —
> which is why the front-server requirement exists, not just the TLS one.
> `/version` discloses the exact build too and follows the same rule as
> `/metrics`. `/healthz` and `/readyz` disclose nothing comparable and stay open.

## Prebuilt binaries

Each [GitHub Release](https://github.com/toise-dev/toise/releases) attaches a
`toise_<version>_<os>_<arch>.tar.gz` (linux/darwin, amd64/arm64) plus a `.sha256`.
Each archive contains `toise-server` and `toise-probe`.

```sh
VERSION=0.2.0
curl -sSLO https://github.com/toise-dev/toise/releases/download/${VERSION}/toise_${VERSION}_linux_amd64.tar.gz
curl -sSLO https://github.com/toise-dev/toise/releases/download/${VERSION}/toise_${VERSION}_linux_amd64.tar.gz.sha256
sha256sum -c toise_${VERSION}_linux_amd64.tar.gz.sha256
tar xzf toise_${VERSION}_linux_amd64.tar.gz
./toise_${VERSION}_linux_amd64/toise-server --help
```

## Container image

Published to GHCR for `linux/amd64` and `linux/arm64`:

```sh
docker run --rm -p 127.0.0.1:8080:8080 -p 127.0.0.1:4317:4317 \
  -v toise-data:/data \
  ghcr.io/toise-dev/toise:0.2.0
```

The image is `distroless/static` running as a nonroot user; state lives in the
`/data` volume. A named volume inherits `/data`'s ownership from the image; for a
bind mount, `chown 65532:65532` the host directory first. See
[`deploy/docker-compose.yml`](../../deploy/docker-compose.yml) for a Compose example
(including how to enable auth and the `--production` lockdown).

## systemd

[`deploy/toise-server.service`](../../deploy/toise-server.service) is a hardened unit
(dedicated user, `ProtectSystem=strict`, `StateDirectory`, a syscall allowlist,
dropped capabilities). It reads secrets from `/etc/toise/toise.env` (so bearer
tokens never appear on the command line) and the config from
`/etc/toise/toise-server.yaml`. The header of the file lists the setup steps.

## Backup & restore

The whole state is the Pebble **data-dir**. To back it up:

- **Cold** (simplest, always consistent): stop the server, copy the data-dir
  (`rsync -a /var/lib/toise/ backup/`), restart. Restore by pointing `--data-dir` at
  the copy on a clean host — a replay rebuilds the same graph.
- **Hot**: the store supports a Pebble *checkpoint* (a live, lock-free consistent
  copy) — see `Store.Checkpoint`. A `snapshot_interval` snapshot is stored *inside*
  the data-dir, so either backup method captures it and the restored host starts
  fast.

A restored data-dir is self-contained: there is no external state. After restore,
the server replays from the latest snapshot plus the event tail.

**A data-dir belongs to one replica.** It carries a journal identifier
(`meta/journal_id`), minted at the first writable start and stamped into the
pagination cursors of `recentChanges`, `entityHistory` and `relations`, so a
cursor from another replica is refused instead of misread (#605). Restoring a
replica from **its own** backup is correct: same journal, same identifiers.
**Do not create a second replica by copying another replica's data-dir**: both
would carry the same journal identifier while minting different event and entity
identifiers from then on, and a cursor carried from one to the other would again
fail silently. Start a new replica on an empty data-dir and let it ingest, as
each replica of a fan-out deployment does.

## Production checklist

- Pin a version (binary or image tag), not `latest`.
- Set `TOISE_AUTH_TOKENS` and `--tls-cert-file`/`--tls-key-file`, or front with a
  TLS+auth proxy.
- Run with `--production` to disable introspection, the playground, and the debug UI.
- Set `retention_max_age` to bound disk, and scrape `/metrics`.
- Point liveness/readiness probes at `/healthz` and `/readyz`.

## Rolling a version change across replicas

A routine production deployment places two replicas behind a DNS
round-robin. During a rolling upgrade both versions answer at the same
time, so two consecutive pages of one paginated enumeration can be
served by two different versions.

When a version changes the ORDER KEY of a pagination, this window has an
asymmetric effect. With the 0.24.0 change, where entity pagination switches
from the local identifier to the identity fingerprint:

- an OLD cursor presented to a NEW binary is rejected explicitly, with
  the remedy named. It is loud, so the reader can react;
- a NEW cursor presented to an OLD binary matches nothing, because the
  old code resolves the cursor by exact equality on the local
  identifier. The response is then an empty page declaring itself the
  last one: the enumeration ends early and reads as complete. It is
  silent, and no fix is possible on the old binary.

During the window, a map or an inventory can read as complete while it is
truncated.

1. Identify the paginating readers before the cutover. Today: the relay
   that serves the portal's Cartography page, and the reading agent
   that queries the API. They are the ones enumerating page by page.
2. Shrink the window to its smallest duration: bring the replicas up
   one after the other without waiting between the two, not one
   replica today and the other tomorrow. The mixed window must count
   in seconds.
3. Start no full enumeration during the window. If a reader has one in
   progress, let it finish before bringing up the second replica, or
   stop it and restart it afterwards.
4. After the cutover, restart without `after` every enumeration that
   began before it. A cursor obtained before the cutover must not be
   reused.
5. Check afterwards, on each replica, that the served version is the
   expected one — a deployment that missed one leaves the window open
   indefinitely.

The real protection does not depend on this procedure. A reader that
enumerates must compare the number of elements it loaded in total
against the total the response reports, and declare its reading
incomplete on any mismatch instead of returning it. The total is in the
same response, on `totalCount` of each connection and on the
`graphScope` counters. This defense covers any truncated read, not only
the cutover window — a failed page, a reached limit, an expired cursor
— and it is the only one that does not rely on the operator having
followed a procedure.
