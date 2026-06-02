# syntax=docker/dockerfile:1
# Dockerfile — production multi-stage build for all Go services.
#
# Parallel to Dockerfile.dev (which stays in place for the dev hot-reload loop).
# Produces small, non-root, OCI-labelled Alpine images for any service in
# cmd/<SERVICE>.
#
# Build:
#   docker build -f Dockerfile \
#     --build-arg SERVICE=mbs \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg REVISION=$(git rev-parse HEAD) \
#     --build-arg CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t hermes-mbs:latest .
#
# Or via Makefile:
#   make docker-build-mbs        # single service
#   make docker-build-all        # all 8 services
#
# Contract details: .hermes/plans/2026-05-29_stage-f-chunk2-prod-dockerfile.md

# ─── Stage 1: builder ────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# git is needed for buildvcs stamping; ca-certificates is needed if the build
# step itself fetches anything over HTTPS (it shouldn't — go mod download runs
# below — but we keep it for parity with Dockerfile.dev).
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# ── Module-graph layer (cached unless go.mod / go.sum changes) ──────────────
#
# The hermes-mbs build pulls in three local-replace modules:
#   replace mbs-native                            => ./re/mbs/mbs-native
#   replace go.mau.fi/mautrix-meta                => ./re/mbs/mautrix-meta-patched
#   replace github.com/refraction-networking/utls => ./re/mbs/mbs-native/third_party/utls
#
# Their go.mod / go.sum must be present *before* `go mod download` so the
# replace targets resolve. Other services don't import them, but copying these
# files costs almost nothing and keeps the cache key identical across services.
COPY go.mod go.sum ./
COPY third_party/mautrix-meta-patched/go.mod third_party/mautrix-meta-patched/go.sum third_party/mautrix-meta-patched/
COPY third_party/mbs-native/go.mod third_party/mbs-native/go.sum third_party/mbs-native/
COPY third_party/mbs-native/third_party/utls/go.mod third_party/mbs-native/third_party/utls/go.sum third_party/mbs-native/third_party/utls/

# Cache mounts persist the module + build cache across builds so a go.sum delta
# (e.g. moving the replace targets to third_party/) does NOT force a cold re-fetch
# of the entire transitive .mod graph. First build is cold (~minutes); subsequent
# builds reuse the host-side BuildKit cache.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# ── Source layer (invalidates on any code change) ───────────────────────────
COPY . .

# ── Compile ─────────────────────────────────────────────────────────────────
#
# SERVICE picks the entrypoint package under cmd/.
# VERSION is stamped into main.version via -ldflags -X if the package declares
# it; otherwise the flag is a no-op and the binary still builds.
ARG SERVICE
ARG VERSION=dev

RUN test -n "${SERVICE}" || (echo "ERROR: --build-arg SERVICE=<name> is required" >&2; exit 1)

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/app \
        ./cmd/${SERVICE}

# ─── Stage 2: runtime ────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates: outbound HTTPS (NATS, Postgres TLS, mautrix-meta).
# tzdata        : time-zone names for logs / cron-ish scheduling.
# wget          : healthcheck `wget --spider`. busybox provides it but we
#                 install explicitly so the dependency is documented.
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -H -u 65532 -s /sbin/nologin hermes

WORKDIR /app

COPY --from=builder --chown=hermes:hermes /out/app /app/app

USER hermes

# OCI labels — populated from build args at the top of the file so layer
# caching is preserved (labels live in the final stage only).
ARG SERVICE
ARG VERSION=dev
ARG REVISION=
ARG CREATED=

LABEL org.opencontainers.image.title="hermes-${SERVICE}" \
      org.opencontainers.image.source="https://github.com/hermes-waba/hermes" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.vendor="hermes-waba"

ENTRYPOINT ["/app/app"]
