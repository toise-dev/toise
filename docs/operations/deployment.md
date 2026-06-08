# Deploying toise-server

Toise ships first-class release artifacts (#47): prebuilt binaries, a container
image, and packaging examples under [`deploy/`](../../deploy). The build is pure-Go
and CGO-free, so the binaries are static and the image is distroless.

> Toise binds to loopback and trusts the network by default. Before exposing it,
> enable TLS and bearer-token auth (see [Configuration](./configuration.md#authentication--tls))
> or front it with a proxy.

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

## Production checklist

- Pin a version (binary or image tag), not `latest`.
- Set `TOISE_AUTH_TOKENS` and `--tls-cert-file`/`--tls-key-file`, or front with a
  TLS+auth proxy.
- Run with `--production` to disable introspection, the playground, and the debug UI.
- Set `retention_max_age` to bound disk, and scrape `/metrics`.
- Point liveness/readiness probes at `/healthz` and `/readyz`.
