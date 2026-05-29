# Stage F Chunk 2 — Production multi-stage Dockerfile (Alpine-based)

**Owner:** Oracle
**Created:** 2026-05-29
**Status:** Plan + contracts written, awaiting build phase
**Parent:** `.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`
**Predecessor:** Chunk 1 (mbs in dev compose) — code complete on disk

---

## 1. Goal

Author a production-grade multi-stage `Dockerfile` (parallel to
`Dockerfile.dev`) and `Dockerfile.web` that produce small, non-root,
predictably-versioned Alpine-based images for every Go service and the
web SPA — without disturbing the dev hot-reload loop.

This chunk produces **build artifacts only**. Booting from those
artifacts is chunk 3's job (`docker-compose.prod.yml`).

---

## 2. Constraints inherited

- **Alpine base** (per Sam's Stage F §7 decision): `golang:1.25-alpine`
  builder, `alpine:3.21` Go runtime, `node:22-alpine` web builder,
  `nginx:alpine` web runtime. Zero base-image churn vs `Dockerfile.dev`
  and the infra services.
- **CGO disabled** so the binary is statically linked against musl —
  required for `--read-only` containers and for using `alpine:3.21`
  without dragging in libc compatibility shims.
- **Wire profile preserved.** No build-tag flags that strip net/http or
  utls hooks. `MAUTRIX_DISABLE_TLS=false` default unchanged.
- **Tenant-from-JWT at gateway boundary** unchanged.
- **AAD format unchanged.**

---

## 3. Contracts

### 3.1 Image labels (OCI)

Every produced image carries these labels via build args:

| Label | Source | Example |
|---|---|---|
| `org.opencontainers.image.source` | hard-coded URL | `https://github.com/hermes-waba/hermes` |
| `org.opencontainers.image.version` | `--build-arg VERSION` | `v0.1.0-rc.1` or `dev` |
| `org.opencontainers.image.revision` | `--build-arg REVISION` | `f28887d` |
| `org.opencontainers.image.created` | `--build-arg CREATED` | RFC3339 timestamp |
| `org.opencontainers.image.title` | hard-coded per service | `hermes-mbs` |

The Makefile build target injects these from `git describe`,
`git rev-parse HEAD`, and `date -u +%Y-%m-%dT%H:%M:%SZ`.

### 3.2 Image identity

- **User:** `hermes:hermes` (UID/GID 65532). Created in the Dockerfile
  with `adduser -D -H -u 65532 -s /sbin/nologin hermes`. Matches the
  distroless `nonroot` UID so future migration is a 1-line swap.
- **Workdir:** `/app`.
- **Binary path:** `/app/<binary-name>`. Backend image entrypoint is
  the binary itself.
- **`USER hermes` directive** active before ENTRYPOINT.

### 3.3 Filesystem expectations

The Go service images expect:

- `/app/<binary>` — the static binary, exec mode.
- `/etc/ssl/certs/ca-certificates.crt` — provided by alpine `ca-certificates`.
- `/run/secrets/<secret>` — tmpfs mounted by compose. Compose owns the
  mount; the image makes no assumption about which secrets exist.
- `/tmp` — writable scratch (tmpfs in prod compose). Bridge driver may
  use it.

The web image expects:

- `/usr/share/nginx/html/` — built SPA assets (`web/dist`).
- `/etc/nginx/conf.d/default.conf` — `deploy/nginx/web.conf` shipped in.

### 3.4 Build-arg contract (Dockerfile)

```
ARG SERVICE      # required; one of: gateway wa mbs campaign inbox contacts proxy notify
ARG VERSION=dev  # optional
ARG REVISION=    # optional
ARG CREATED=     # optional
```

Build invocation:

```sh
docker build \
  -f Dockerfile \
  --build-arg SERVICE=mbs \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg REVISION=$(git rev-parse HEAD) \
  --build-arg CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t hermes-mbs:dev \
  .
```

The Makefile target wraps this.

### 3.5 `.dockerignore` contract

Build context exclusions (mirrors what the build does NOT need):

```
.git
.hermes/
.idea
.vscode
re/
celestial-research/
bin/
gen/                     # regenerated at build time? NO — kept; chunk 2 does NOT regen proto in image.
                         # Stays committed for repeatable builds.
web/node_modules/
web/dist/
docs/
deploy/secrets/
*.md
**/*_test.go             # tests not needed in prod binary
**/testdata/
```

Note: `gen/` is gitignored at repo level but **must be present** in the
build context. The contract is: developer runs `make proto-gen` before
`docker build`. The Makefile `docker-build-all` target depends on
`proto-gen`.

### 3.6 Build-stage cache contract

Stage 1 (`builder`) layers, ordered by change frequency (slow → fast):

1. `FROM golang:1.25-alpine AS builder`
2. `RUN apk add --no-cache git ca-certificates`
3. `WORKDIR /src`
4. `COPY go.mod go.sum ./` then `RUN go mod download` — **cache hit
   unless deps changed.**
5. `COPY . .` — invalidates whenever any source changes.
6. `RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/app ./cmd/${SERVICE}`

Stage 2 (`runtime`):

1. `FROM alpine:3.21`
2. `RUN apk add --no-cache ca-certificates tzdata wget` — `wget` for
   healthcheck; `tzdata` for time-zone names. `wget` is busybox-built
   already; explicit dependency for documentation.
3. `RUN adduser -D -H -u 65532 -s /sbin/nologin hermes`
4. `COPY --from=builder --chown=hermes:hermes /out/app /app/app`
5. `USER hermes`
6. `WORKDIR /app`
7. Labels via `LABEL` directives wired to ARGs.
8. `ENTRYPOINT ["/app/app"]`

Stage 1 (web-builder for `Dockerfile.web`):

1. `FROM node:22-alpine AS builder`
2. `WORKDIR /src`
3. `COPY web/package.json web/package-lock.json* ./`
4. `RUN npm ci --omit=dev`
5. `COPY web/ .`
6. `RUN npm run build` — produces `dist/`

Stage 2 (web-runtime):

1. `FROM nginx:alpine`
2. `COPY deploy/nginx/web.conf /etc/nginx/conf.d/default.conf`
3. `COPY --from=builder /src/dist /usr/share/nginx/html`
4. nginx runs as `nginx` user by default (UID 101); no override needed.
5. `EXPOSE 80`

### 3.7 `deploy/nginx/web.conf` contract

```nginx
server {
    listen 80;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    # SPA fallback: any path that doesn't match a file returns index.html.
    location / {
        try_files $uri $uri/ /index.html;
    }

    # Aggressive cache for hashed assets.
    location /assets/ {
        expires 1y;
        add_header Cache-Control "public, immutable";
        try_files $uri =404;
    }

    # No cache for the SPA shell.
    location = /index.html {
        add_header Cache-Control "no-cache, no-store, must-revalidate";
    }

    # Health probe for chunk-4 alignment.
    location = /healthz {
        access_log off;
        return 200 "ok\n";
        add_header Content-Type text/plain;
    }

    # Compression (gzip; brotli is module-extra in nginx:alpine).
    gzip on;
    gzip_types text/plain text/css application/javascript application/json application/octet-stream image/svg+xml;
    gzip_min_length 1024;
}
```

The chunk-5 Caddyfile/nginx reverse proxy in front of this container
handles TLS termination and reverse-proxies `/api/*` and `/ws` to
`gateway:8081`. This nginx config is the *container's* server, not the
fronting proxy.

### 3.8 Makefile contract

```makefile
GIT_VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_REVISION := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_DATE   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

DOCKER_BUILD_ARGS = \
  --build-arg VERSION=$(GIT_VERSION) \
  --build-arg REVISION=$(GIT_REVISION) \
  --build-arg CREATED=$(BUILD_DATE)

.PHONY: docker-build-all docker-build-web

docker-build-all: proto-gen
	@for svc in gateway wa mbs campaign inbox contacts proxy notify; do \
	  echo "Building hermes-$$svc..."; \
	  docker build -f Dockerfile $(DOCKER_BUILD_ARGS) --build-arg SERVICE=$$svc -t hermes-$$svc:$(GIT_VERSION) -t hermes-$$svc:latest .; \
	done
	$(MAKE) docker-build-web

docker-build-web:
	@echo "Building hermes-web..."
	docker build -f Dockerfile.web $(DOCKER_BUILD_ARGS) -t hermes-web:$(GIT_VERSION) -t hermes-web:latest .

docker-build-%: proto-gen
	docker build -f Dockerfile $(DOCKER_BUILD_ARGS) --build-arg SERVICE=$* -t hermes-$*:$(GIT_VERSION) -t hermes-$*:latest .
```

---

## 4. Implementation steps

1. Write `.dockerignore` at repo root (or extend if present).
2. Write `Dockerfile` (production multi-stage, parallel to `Dockerfile.dev`).
3. Write `Dockerfile.web` (web builder → nginx-alpine runtime).
4. Write `deploy/nginx/web.conf`.
5. Extend `Makefile` with `docker-build-*` targets.
6. Build all 8 Go service images via `make docker-build-all` (no web yet).
7. Build web image via `make docker-build-web`.
8. Verify image sizes < 80 MB each (per master plan R1 target).
9. Verify non-root execution: `docker run --rm hermes-mbs:latest /bin/sh -c 'id'`
   → returns `uid=65532(hermes) gid=65532(hermes)` — but the entrypoint
   is the binary, so this is run via `--entrypoint /bin/sh`.
10. Verify read-only filesystem boot: `docker run --rm --read-only
    --tmpfs /tmp hermes-mbs:latest`. Expect non-zero exit (no config)
    but no filesystem write errors during init.
11. Verify image labels: `docker inspect hermes-mbs:latest | jq
    '.[0].Config.Labels'`.
12. Smoke-run web image: `docker run --rm -p 8090:80 hermes-web:latest`
    then `curl http://localhost:8090/healthz` → `ok\n`.
13. Write hostile audit. Resolve all P0/P1.
14. Commit.

---

## 5. Files inventory (anticipated diff shape)

```
NEW:
  Dockerfile                                                            [+~55 LOC]
  Dockerfile.web                                                        [+~28 LOC]
  deploy/nginx/web.conf                                                 [+~35 LOC]
  .dockerignore                                                         [+~22 LOC]
  .hermes/plans/2026-05-29_stage-f-chunk2-prod-dockerfile.md            [this file]
  docs/research/mbs-f-chunk2-hostile-audit-2026-05-29.md                [+~200 LOC, post-build]

MODIFIED:
  Makefile                                                              [+~22 LOC]
```

No Go code, no proto, no migrations, no frontend code, no compose files.
`Dockerfile.dev` is untouched — chunk 2 ships a sibling Dockerfile.

---

## 6. Acceptance gates

| # | Gate | Command | Pass condition |
|---|---|---|---|
| 1 | All Go images build clean | `make docker-build-all` | exit 0 |
| 2 | Web image builds clean | `make docker-build-web` | exit 0 |
| 3 | Each Go image size < 80MB | `docker images hermes-*:latest --format '{{.Size}}'` | numeric < 80MB |
| 4 | Web image size < 50MB | `docker images hermes-web:latest --format '{{.Size}}'` | numeric < 50MB |
| 5 | Non-root execution | `docker run --rm --entrypoint id hermes-mbs:latest` | `uid=65532` |
| 6 | Read-only FS tolerated | `docker run --rm --read-only --tmpfs /tmp hermes-mbs:latest` | no filesystem-write errors in logs (may exit non-zero on missing DEK, that's correct) |
| 7 | Image labels present | `docker inspect hermes-mbs:latest \| jq '.[0].Config.Labels["org.opencontainers.image.revision"]'` | non-empty |
| 8 | Web nginx healthz returns | `docker run -d -p 8090:80 --name webtest hermes-web:latest; sleep 1; curl -fsS http://localhost:8090/healthz; docker rm -f webtest` | `ok` |
| 9 | Web SPA fallback works | `curl -fsS http://localhost:8090/does-not-exist` | returns `index.html` content |
| 10 | go test still green | `go test -race -count=1 ./...` | ok |
| 11 | Chunk-1 dev compose unaffected | `docker-compose -f docker-compose.dev.yml config` | parses clean |
| 12 | Dev image still works (cache untouched) | `docker-compose -f docker-compose.dev.yml build wa` | reuses cached layers |

---

## 7. Hostile-audit categories (to fill at chunk close)

- **Image attack surface:** what packages ship in the runtime image
  beyond the binary? (`ca-certificates`, `tzdata`, `wget`, `busybox`)
- **User namespace:** does the binary actually execute as 65532 not
  root? Verified via `docker top`.
- **Read-only compatibility:** does any service write to its own
  `/app` at runtime? (Should not; `/tmp` tmpfs covers scratch.)
- **Build reproducibility:** does `make docker-build-mbs` produce
  bit-identical layers across two consecutive runs on the same commit?
  (`-trimpath` + sorted file listing.)
- **Layer caching:** does changing one `.go` file in `cmd/inbox/`
  invalidate the `go mod download` layer? (Answer: no — `go.mod`
  unchanged keeps that layer cached.)
- **Web cache headers:** does `/assets/<hash>.js` get
  `Cache-Control: public, immutable`? Does `/index.html` get
  `no-cache`? (Verified with `curl -I`.)
- **Web SPA fallback:** does requesting `/inbox/123` return the SPA
  shell? (Should; `try_files $uri $uri/ /index.html;`).
- **Healthcheck behaviour:** is `wget --spider http://localhost:<port>/readyz`
  available inside the Go images? (Yes — busybox `wget` is built in.)
- **Static binary verification:** `docker run --rm --entrypoint ldd
  hermes-mbs:latest /app/app` — should print `not a dynamic executable`.
- **Vulnerability scan:** Trivy or grype against produced images
  (`trivy image --severity HIGH,CRITICAL hermes-mbs:latest`). Document
  any HIGH/CRITICAL findings; defer fixes unless blocker.
- **mautrix-meta footprint:** the mbs binary is ~50 MB. Layer size
  observed.
- **VERSION arg drift:** if `git describe` returns `v0.1.0-3-gf28887d-dirty`,
  is that a valid OCI label value? (Yes — labels accept any string.)

---

## 8. Out of scope reminders

- Multi-arch builds (`docker buildx --platform=linux/amd64,linux/arm64`).
  Stage F is local-machine builds only.
- Image signing (cosign).
- Registry push (`docker push`).
- Image vulnerability scanning in CI.
- Web reverse proxy fronting (chunk 5).
- Image-based compose run (chunk 3).
- Per-service custom Dockerfile variations (e.g. wa needs whatsmeow CGO
  deps? Verified no — `go build` succeeds with `CGO_ENABLED=0`).

---

## 9. Rollback

Chunk 2 is purely additive. To revert:

```sh
git revert <commit-sha>
docker rmi hermes-{gateway,wa,mbs,campaign,inbox,contacts,proxy,notify,web}:latest
```

`Dockerfile.dev` and the dev compose loop are untouched, so dev hack-loop
keeps working.

---

## 10. Open question

**`gen/` in build context:** Currently `gen/` is gitignored (
`make proto-gen` regenerates it). The Dockerfile expects it present in
the build context. Three options:

- **A (chosen):** Documented contract — operator runs `make
  proto-gen` before `make docker-build-all`. Makefile target depends
  on `proto-gen`. Pro: simple, no Docker-side proto deps. Con:
  clean-clone build fails without an explicit `make` step. Mitigated
  by the Makefile dependency.
- B: Run `buf generate` inside the builder stage. Pro: clean-clone
  works. Con: drags `buf` + protoc + protoc-gen-* into builder image,
  ~80 MB stage cache cost.
- C: Drop `gen/` from `.gitignore`, commit it. Pro: no build-time
  regen. Con: diff noise on every proto change.

Chunk 2 ships option A. Switching to B or C is a follow-up if Sam wants.
