# syntax=docker/dockerfile:1

# --- build stage -------------------------------------------------------------
FROM golang:1.26 AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Version/commit are stamped via ldflags (passed from CI; default to dev).
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
# CGO-free, static, stripped — Pebble and the whole tree are pure Go.
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w -X github.com/toise-dev/toise/internal/version.Version=${VERSION} -X github.com/toise-dev/toise/internal/version.Commit=${COMMIT}" \
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
