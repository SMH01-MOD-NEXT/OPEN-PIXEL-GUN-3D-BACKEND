# syntax=docker/dockerfile:1
# SPDX-License-Identifier: GPL-3.0-or-later

ARG GO_VERSION=1.24
ARG ALPINE_VERSION=3.22

FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# Copy only inputs needed by the build. Runtime state, credentials and local
# vendor bundles are excluded by .dockerignore and can never enter an image.
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY content ./content

ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go test ./... && \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/opg3d-backend \
      ./cmd/opg3d-backend

FROM alpine:${ALPINE_VERSION} AS runtime

RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 10001 opg3d && \
    adduser -S -D -H -u 10001 -G opg3d opg3d && \
    mkdir -p /var/lib/opg3d /var/log/opg3d /app/content/bundles && \
    chown -R opg3d:opg3d /var/lib/opg3d /var/log/opg3d /app

COPY --from=build --chown=10001:10001 /out/opg3d-backend /usr/local/bin/opg3d-backend
COPY --from=build --chown=10001:10001 /src/content/ /app/content/

WORKDIR /app

ENV OPG3D_ADDR=:8080 \
    OPG3D_DATA_DIR=/var/lib/opg3d \
    OPG3D_LOG_DIR=/var/log/opg3d \
    OPG3D_CONTENT_DIR=/app/content \
    OPG3D_DISCOVERY_PORT=47318

USER 10001:10001

EXPOSE 8080/tcp 47318/udp
STOPSIGNAL SIGTERM

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/api/v1/ping"]

ENTRYPOINT ["/usr/local/bin/opg3d-backend"]
