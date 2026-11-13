# syntax=docker/dockerfile:1

# --- build stage -------------------------------------------------------------
FROM golang:1.26.11 AS build
WORKDIR /src

# Cache modules first. The main module requires the published pkg/emit tag
# (no local replace; #212), so go.mod/go.sum alone resolve and download it.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Version/commit/build timestamp are stamped via ldflags and must come from
# CI: a binary built without them would report "unknown" and hide which build
# is actually deployed.
ARG VERSION
ARG COMMIT
ARG BUILT
# CGO-free, static, stripped — Pebble and the whole tree are pure Go.
RUN if [ -z "$VERSION" ] || [ -z "$COMMIT" ] || [ -z "$BUILT" ]; then \
      echo "VERSION, COMMIT and BUILT build args are required" >&2; exit 1; \
    fi
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/toise-dev/toise/internal/version.Version=${VERSION} -X github.com/toise-dev/toise/internal/version.Commit=${COMMIT} -X github.com/toise-dev/toise/internal/version.Built=${BUILT} -buildid=" \
    -o /out/toise-server ./cmd/toise-server

# Pre-create a data dir owned by the nonroot user (distroless has no shell/mkdir).
RUN install -d -o 65532 -g 65532 /out/data

# --- runtime stage -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/toise-server /usr/local/bin/toise-server
COPY --from=build --chown=65532:65532 /out/data /data

USER 65532:65532
EXPOSE 8080 4317
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/toise-server"]
# Bind to all interfaces inside the container; front with TLS+auth in production.
CMD ["--listen", "0.0.0.0:8080", "--otlp-listen", "0.0.0.0:4317", "--data-dir", "/data"]
