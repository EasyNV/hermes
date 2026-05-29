# Stage F — Deploy Hardening (Compose-first) — Master Plan

**Owner:** Oracle
**Created:** 2026-05-29
**Status:** Planning
**Predecessor:** Stage E3 (MBS unified inbox) — COMPLETE, merged on `main`
**Successor:** Stage G (K8s migration) — deferred

---

## 0. Why Stage F, why now

Stage E (E1..E3) shipped `hermes-mbs` end-to-end on the wire and in the UI. The
MBS surface compiles, tests green, and the entire MBS unified-inbox path works
from MQTT inbound through to web UI render. **What does not yet work** is
*operating the system as a deployment*. Specifically:

1. **`hermes-mbs` is not in `docker-compose.dev.yml`.** It's a real microservice
   with its own port (8082), its own DEK requirement, its own NATS streams, its
   own migrations (`migrations/mbs/`), and its own healthcheck (`/readyz`). The
   compose stack does not start it. Gateway has `MBS_ADDR=localhost:8082`
   default but no `mbs:8082` neighbour to dial.
2. **`migrations/mbs` is not in the migrate init container loop.** The dev
   compose still iterates `gateway wa campaign inbox contacts proxy notify`.
3. **No DEK provisioning path.** `HERMES_MBS_DEK_FILE` and `HERMES_MBS_DEK_HEX`
   are read by config but compose never supplies either — `cmd/mbs/main.go`
   would `log.Fatal` at boot. There is no documented or scripted way to
   generate, mount, and rotate the DEK.
4. **`Dockerfile.dev` is dev-only.** Source-mounted, no resource limits,
   no non-root user, no read-only filesystem, no static-strip, no caching layer
   strategy. Acceptable for local hack-loop, unacceptable for any production-
   adjacent deploy (even single-host VPS).
5. **No prod compose file.** `docker-compose.yml` is a bare three-service
   infra stack. `docker-compose.dev.yml` is a source-mounted hot-reload dev
   stack. Nothing in between for the "deploy to a single Hetzner / Linode /
   Mac mini at home" case Sam will hit before K8s.
6. **No operational runbooks.** Boot order, secret rotation, DB backup, MBS
   pod-claim takeover after a pod crash, NATS stream recovery — all tribal.
7. **No reverse proxy guidance.** Compose binds raw ports. TLS termination,
   gateway port mapping, web SPA hosting — undefined.
8. **No healthcheck coverage.** Compose `healthcheck` blocks use `nc -z` which
   says "port open" not "service ready." `cmd/mbs/main.go` exposes a real
   `/readyz` on `METRICS_PORT=9092` that flips on `SetReady(true)` after all
   dependencies are up. Other services may or may not — needs audit and
   alignment.

Stage F's job is to close this entire gap end-to-end with the *compose-first,
K8s-later* constraint Sam set. Everything in this plan ships as `docker
compose up` against a freshly-cloned repo.

---

## 1. Scope

### In scope (this Stage)
- Add `hermes-mbs` to `docker-compose.dev.yml` with correct env, dependencies,
  healthcheck, and a working DEK source for dev.
- Extend the migrate init container to run `migrations/mbs/`.
- Add `MBS_ADDR=mbs:8082` to the gateway env block.
- Author a production-grade multi-stage Dockerfile (`Dockerfile`, replacing or
  supplementing `Dockerfile.dev`): non-root, static binary, distroless or
  scratch where possible, predictable layer cache, build args for service
  name only.
- Author `docker-compose.prod.yml` (image-based, restart policies, resource
  limits, named volumes, no source mounts, no `npm run dev`, no `go run`).
- Define a DEK lifecycle: generation script (`scripts/dek-generate.sh`),
  dev path (file-mounted at `./deploy/secrets/dev/mbs-dek.bin`), prod path
  (Docker secret at `/run/secrets/mbs_dek`), rotation runbook
  (`docs/runbooks/dek-rotation.md`).
- Externalise other secrets: JWT signing key (gateway), PG password, NATS
  creds (if/when JWT-auth enabled), `BIZAPP_CLIENT_TOKEN` for hermes-mbs.
- Document the deploy story: `docs/runbooks/compose-deploy.md` covers boot
  order, smoke test, log inspection, healthcheck verification, and clean
  shutdown.
- Add a "ready-not-just-port-open" healthcheck to every service. Where a
  service exposes a metrics port with `/readyz` (mbs does), use it. Where
  it doesn't, define one and add it (chunk-scoped).
- A reverse-proxy reference: a Caddyfile or nginx config example that fronts
  gateway (8080 gRPC-Web + 8081 WS+REST) and serves the web SPA build.
- A `make deploy-dev-up` / `make deploy-prod-up` target so the docs and the
  Makefile agree.

### Out of scope (deferred to Stage G / later)
- Kubernetes manifests, Helm charts, kustomize overlays.
- Multi-node orchestration (Swarm, Nomad, K3s).
- Managed-DB migration (RDS, Cloud SQL).
- Vault / SOPS / SealedSecrets integration. Stage F uses **file-based Docker
  secrets** because that's the minimum viable production posture for a
  compose-on-VPS deploy. Vault integration is its own stage when Sam picks
  a managed secret store.
- Observability stack (Prometheus, Grafana, Loki). Stage F leaves the
  `METRICS_PORT` scrape surface exposed; wiring Prometheus is its own stage.
- CI/CD pipelines. Stage F ships build/deploy *artifacts* (Dockerfile,
  compose files, Makefile targets, runbooks); wiring them into GitHub
  Actions / a CI runner is a follow-up.
- Backup automation. Stage F documents the manual `pg_dump` + `nats stream
  backup` procedure in `docs/runbooks/backup-restore.md`; cron-driving it is
  out of scope.

---

## 2. Constraint inheritance from Stage E

- **Wire profile preserved** — Dockerfile builds the same Go binaries from the
  same source. `hermes-mbs` runtime keeps utls/Tigon/OkHttp-H2 fingerprint as
  long as the build flags don't strip net/http hooks. We will **not** use
  `CGO_ENABLED=1` for any service; the bridge driver is pure-Go mautrix.
- **Fail-closed at boot** — every service's compose entry uses
  `depends_on.<service>.condition: service_healthy` for its hard prereqs.
  hermes-mbs depends on Postgres + NATS + migrate; gateway depends on every
  backend service `service_started` (not healthy, to allow staggered ready).
- **Per-uid mutex / pod_id claim** — unchanged. `POD_ID` env is set per-replica
  via Docker's `{{.Service}}-{{.Slot}}` template or a static value in dev.
- **Compose-first, K8s-later** — enforces this stage's existence.
- **AAD format `mbs.<column>.uid=<uid>`** — unchanged; build args do not
  touch the encryption envelope.
- **Tenant-from-JWT at gateway boundary** — unchanged; JWT secret moves from
  hard-coded compose env to Docker secret in prod, file-mounted in dev.
- **TypeScript enums sync manually with proto** — unchanged.
- **mbs internal port 8082, `MBS_ADDR=localhost:8082`** — exactly what we
  ship. In compose, `MBS_ADDR=mbs:8082`.

---

## 3. Chunk decomposition

Each chunk is a single PR-sized unit of work with its own plan, contracts pass,
build/test gates, hostile audit, and commit. Same discipline as Stage E.

### Chunk 1 — `hermes-mbs` in dev compose + migrate loop + gateway wiring
**Estimate:** 2-4 hours.
**Files (anticipated):**
- `docker-compose.dev.yml` (add `mbs` service block, extend `gateway.depends_on`)
- `Makefile` (no change; already iterates `mbs`)
- `deploy/secrets/dev/mbs-dek.bin` (32 random bytes, gitignored — only the
  example/placeholder gets committed)
- `deploy/secrets/dev/.gitignore`
- `deploy/secrets/dev/mbs-dek.bin.example` (committed placeholder, 32 bytes
  of zeros + warning header in companion `README.md`)
- `deploy/secrets/README.md` (how to generate, where to mount, what
  rotation looks like)
- `scripts/dek-generate.sh` (one-liner `openssl rand -out $1 32` wrapper +
  permission hardening — `chmod 400`)
- `docs/runbooks/compose-deploy.md` (initial draft — chunk-1 scope: dev only)
- Plan file `.hermes/plans/2026-05-29_stage-f-chunk1-mbs-in-dev-compose.md`
- Hostile audit `docs/research/mbs-f-chunk1-hostile-audit-2026-05-29.md`

**Acceptance:**
- `make infra && docker compose -f docker-compose.dev.yml up --build` boots
  cleanly on a fresh clone after `scripts/dek-generate.sh
  deploy/secrets/dev/mbs-dek.bin`.
- `docker compose -f docker-compose.dev.yml ps` shows `mbs` Up (healthy).
- `curl -fsS http://localhost:9092/readyz` returns 200.
- `curl -fsS http://localhost:9092/livez` returns 200.
- `migrations/mbs/000003_pod_id_and_freshness.up.sql` is applied (verify via
  `psql -c "SELECT * FROM schema_migrations_mbs;"`).
- Gateway logs show successful gRPC connect to `mbs:8082`.
- `docker compose -f docker-compose.dev.yml down` returns cleanly with no
  orphaned containers.
- `go test ./...` + `cd web && npm run build` still green (chunk 1 should
  not touch any code path that has tests).

### Chunk 2 — Production multi-stage Dockerfile (Alpine-based)
**Estimate:** 3-5 hours.
**Base images (decided 2026-05-29):**
- Builder: `golang:1.25-alpine` (matches `Dockerfile.dev`)
- Backend runtime: `alpine:3.21` (matches `Dockerfile.dev`)
- Web runtime: `nginx:alpine` (in-compose static serve)
- Web builder: `node:22-alpine` (matches dev compose web service)

**Files (anticipated):**
- `Dockerfile` (new — supplements `Dockerfile.dev`; multi-stage Go service
  build with caching layer for `go mod download`)
- `Dockerfile.web` (new — two-stage: `node:22-alpine` builder → `nginx:alpine`
  runtime serving `/usr/share/nginx/html`)
- `deploy/nginx/web.conf` (new — nginx config for the web container: SPA
  fallback to `/index.html`, gzip, cache headers for `/assets/*`)
- `.dockerignore` (new or update — exclude `web/node_modules`, `bin/`, `gen/`
  build artefacts, `.hermes/`, `re/`, `celestial-research/`)
- `Makefile` (add `docker-build-all`, `docker-build-<svc>`, `docker-build-web`)
- Plan + hostile audit pair

**Acceptance:**
- `docker build -f Dockerfile --build-arg SERVICE=mbs -t hermes-mbs:dev .`
  produces an image. Target size: < 80 MB (alpine + ~50 MB Go binary +
  ca-certificates).
- Image runs as non-root UID/GID 65532 (create `hermes:hermes` user in
  Dockerfile).
- `docker run --rm --read-only hermes-mbs:dev` exits gracefully on missing
  config (validates fail-closed posture survives the build).
- All 7 Go services build successfully under the same Dockerfile.
- `Dockerfile.web` produces a `hermes-web:dev` image; `docker run -p
  5173:80 hermes-web:dev` serves the SPA.
- Image labels carry: `org.opencontainers.image.source`, `.version`,
  `.revision`, `.created`. Version comes from `git describe --tags --always`
  build-arg.
- A signed `cosign` keypair is NOT generated this chunk (deferred).

### Chunk 3 — `docker-compose.prod.yml` + secret externalisation
**Estimate:** 3-5 hours.
**Files (anticipated):**
- `docker-compose.prod.yml` (new — image-based, no source mount, restart
  policies, resource limits, named volumes, Docker secrets, healthcheck
  alignment with chunk 4)
- `.env.prod.example` (committed example, gitignored real)
- `deploy/secrets/prod/*.example` (placeholders for each secret)
- `deploy/secrets/prod/.gitignore`
- `docs/runbooks/compose-deploy.md` (extend with prod section: secret
  provisioning, image build/push if using registry, restart behaviour,
  log rotation via `logging:` driver options)
- `docs/runbooks/secret-management.md` (DEK lifecycle, JWT rotation, PG
  password rotation, NATS creds future-state)
- Plan + hostile audit pair

**Acceptance:**
- `docker compose -f docker-compose.prod.yml --env-file .env.prod up -d`
  boots end-to-end from images (after `make docker-build-all`).
- All 8 services + infra start without `log.Fatal`.
- Killing `mbs` container triggers automatic restart (verify
  `restart: unless-stopped`).
- `docker compose -f docker-compose.prod.yml logs gateway | head` shows
  no plaintext JWT secret leakage.
- `docker compose -f docker-compose.prod.yml exec mbs cat
  /run/secrets/mbs_dek | wc -c` returns 32.
- Memory limit on each backend service is set (default cap 256MiB; mbs cap
  512MiB given mautrix-meta footprint).

### Chunk 4 — Real readiness probes across all services
**Estimate:** 4-6 hours.
**Files (anticipated):**
- `internal/<svc>/handler/health.go` for each service that lacks one
  (audit reveals which)
- `cmd/<svc>/main.go` — boot a sidecar metrics/health HTTP server on
  `METRICS_PORT` (default per-service: 9101-9107)
- `docker-compose.dev.yml` + `docker-compose.prod.yml` — switch `healthcheck.test`
  from `nc -z` to `wget --spider http://localhost:$METRICS_PORT/readyz`
- Plan + hostile audit pair

**Acceptance:**
- Every service exposes `/livez` (always 200 once HTTP server starts) and
  `/readyz` (200 only after deps are reachable and the service is in steady
  state).
- Compose `depends_on` `condition: service_healthy` works for the full
  dependency chain (verify by sleeping NATS startup and observing gateway
  waits).
- `go test ./internal/...` still green (probes are pure HTTP, no business
  logic).
- No new race conditions in graceful shutdown (probe goes 503 *before* gRPC
  goes NOT_SERVING).

### Chunk 5 — Reverse proxy reference + runbook polish + Makefile targets
**Estimate:** 2-3 hours.
**Files (anticipated):**
- `deploy/caddy/Caddyfile.example` (TLS via ACME, serves `web/dist`, proxies
  `/api/*` to `gateway:8081`, proxies `/ws` to `gateway:8081`)
- `deploy/nginx/hermes.conf.example` (alternate, for users on nginx)
- `docs/runbooks/compose-deploy.md` (final pass — fronting the stack)
- `docs/runbooks/backup-restore.md` (manual `pg_dump` + restore, NATS stream
  snapshot via `nats stream backup`)
- `docs/runbooks/mbs-bootstrap.md` (first-tenant DEK + JWT generation +
  superuser seeding)
- `Makefile` — `deploy-dev-up`, `deploy-dev-down`, `deploy-prod-up`,
  `deploy-prod-down`, `deploy-prod-logs`, `deploy-prod-restart` targets
- Plan + hostile audit pair

**Acceptance:**
- Following `docs/runbooks/compose-deploy.md` on a clean VM (or in a `docker
  in docker` test) results in a working browser session at `https://localhost`
  (with self-signed cert override).
- `make deploy-prod-up` is equivalent to following the runbook by hand.
- `docs/runbooks/backup-restore.md` executes successfully against a running
  stack.

---

## 4. Discovery already done

| Question | Answer |
|---|---|
| Is `hermes-mbs` in dev compose? | No. Confirmed by `grep mbs docker-compose.dev.yml` returning only `# mbs` mentions in comments or absent entirely. |
| Does migrate loop cover `migrations/mbs`? | No. Hard-coded list `gateway wa campaign inbox contacts proxy notify`. Note `Makefile` *does* include mbs in its own loops — only the in-compose init container is stale. |
| Does `cmd/mbs/main.go` need a DEK? | Yes. `internal/mbs/config/config.go` reads `HERMES_MBS_DEK_FILE` then `HERMES_MBS_DEK_HEX`; main probes both and `log.Fatal`s if neither resolves. |
| What's the mbs internal port? | 8082 (matches AGENTS.md and `MBS_ADDR=localhost:8082` default in `internal/gateway/config/config.go`). |
| What's the mbs metrics/diag port? | 9092 (default `METRICS_PORT=9092`). |
| Does hermes-mbs expose `/readyz`? | Yes, on `:9092`. Flips to ready after gRPC server starts (per `cmd/mbs/main.go` header comment). |
| Do other services expose `/readyz`? | Unknown. Compose currently uses `nc -z localhost <port>`. Chunk 4 audit will enumerate. |
| What ports do other services use? | gateway 8080 (gRPC) + 8081 (REST/WS) + 9100 (metrics — TBD), wa 9104, inbox 9106, campaign 9105, contacts 9102, proxy 9101, notify 9103. mbs 8082 + 9092. Verified in `docker-compose.dev.yml`. |
| Does dev compose already use `hermes-net` bridge? | Yes. Prod compose will too. |
| Does dev compose mount source? | Yes — frontend (`./web:/app`) but backends use `Dockerfile.dev` which `COPY . .` at build time. Hot-reload happens through go-run not bind-mount. |

---

## 5. Risks & known landmines

- **R1 — Image size creep.** mautrix-meta vendored brings ~30 MB of net/http,
  golang.org/x/net deps. Plus utls. Target stays Alpine-based (per §7
  decision): `alpine:3.21` runtime + static Go binary (`-trimpath
  -ldflags='-s -w'`, `CGO_ENABLED=0`). Realistic per-binary size: 50-70 MB
  including mbs (~51 MB stripped today). Acceptable.
- **R2 — DEK leakage via `docker inspect`.** If we mount the DEK file via
  bind-mount with the path visible in `docker inspect`, the path itself is
  not sensitive but the host file is. Mitigation: use Docker secrets in prod
  (`/run/secrets/<name>` is tmpfs, not inspectable as bind path).
- **R3 — Compose v2 vs v1.** Sam's Mac has Compose v2 (`docker compose` not
  `docker-compose`). All commands and Makefile targets use the v2 form.
  Runbook documents v1 fallback.
- **R4 — Healthcheck transitions race graceful shutdown.** If `/readyz` flips
  to 503 *after* the gRPC server starts refusing connections, clients see
  spurious errors during deploy. Chunk 4 enforces probe-first ordering and
  has a dedicated test.
- **R5 — `restart: unless-stopped` masks crash loops.** A panicking service
  that restarts every 2s will not page Sam in the absence of monitoring.
  Mitigation: structured logs to file via `logging.driver=json-file` with
  rotation; runbook documents how to grep crash signatures. Future stage
  wires alerting.
- **R6 — Bridge driver process-wide TLS-disable flag.** `MautrixDisableTLS`
  is global and unrecoverable until restart (documented in
  `internal/mbs/config/config.go`). Prod compose MUST default `false` and
  the WARN log must be present. Hostile audit gates on this.
- **R7 — Terminal output scrubs `hermes_dev` password as `***`.** False-alarm
  observation during chunk-1 discovery: the on-disk `docker-compose.dev.yml`
  has `postgres://hermes:hermes_dev@postgres:5432/...` correctly, but the
  Hermes terminal tool's output-scrubber rewrites the password-looking
  segment. Confirmed real bytes via `od -c`. No file change needed; flag
  retained as a reminder for future readers that grepping for `hermes_dev`
  via the terminal tool will appear to fail when the file is fine.
- **R8 — gateway depends on too many `service_started`.** A backend that
  starts (TCP bind) but is not actually ready (gRPC reflection registered,
  DB pool warmed) causes gateway's bootstrap calls to fail. Chunk 4 switches
  these to `service_healthy` once probes are real.
- **R9 — NATS JetStream stream replicas in compose are forced to 1.** Prod
  compose runs single-node NATS, so `MBS_STREAM_REPLICAS=1` (default) is
  correct. Document for the K8s migration that this changes to 3.

---

## 6. Gate sequencing

```
chunk 1 → boot mbs in dev → smoke test (manual)
  ↓
chunk 2 → prod Dockerfile → image-build smoke
  ↓
chunk 3 → prod compose → boot from images smoke
  ↓
chunk 4 → real probes → service_healthy gating works
  ↓
chunk 5 → reverse proxy + runbooks → end-to-end fresh-VM test
```

Each chunk's hostile audit must pass with zero P0 / zero unresolved P1 before
the next chunk starts.

---

## 7. Open questions to resolve before / during chunk 1

These are Sam-facing decisions. The plan defaults are listed; Sam can
override before chunk 1 starts.

| Decision | Resolution | Status |
|---|---|---|
| Distroless base or alpine base for prod images? | **Alpine — `golang:1.25-alpine` builder + `alpine:3.21` runtime.** Matches existing `Dockerfile.dev`, all infra images (`postgres:17-alpine`, `redis:7-alpine`, `nats:2-alpine`, `node:22-alpine`), zero churn for operator muscle memory. Tradeoff: ~7 MB base + glibc-via-musl quirks; mitigated by `CGO_ENABLED=0`. | ✅ Decided 2026-05-29 |
| `web` build strategy in prod? | **In-compose nginx container** serving `npm run build` output (`web/dist`). Single-VPS story stays single-machine. Image base: `nginx:alpine`. | ✅ Decided 2026-05-29 |
| Image registry pattern? | local-only (no `docker push` in chunk 2 — that's CI/CD scope) | Default; override Y/N |
| Reverse proxy default in runbook? | Caddy (auto-TLS via Let's Encrypt) | Default; override possible (nginx/traefik) |
| Single-host VPS target the prod compose is sized for? | 4 vCPU / 8 GB RAM single node | Default; document larger sizing in chunk 5 |
| DEK rotation cadence? | runbook recommends 90 days, no automation this stage | Default; override Y/N |

---

## 8. Contracts touched by Stage F

**None** of the gRPC, NATS-event, or WebSocket contracts change in Stage F.
This is pure operational surface — Docker, compose, env, secrets, runbooks.
The proto/, docs/contracts/proto/ directory is read-only this stage.

What does change is the **environment contract** between operator and
service. Stage F formalises that contract:

- `docs/runbooks/env-reference.md` (new — generated from each service's
  config.go) lists every env var, default, and prod-required overrides.
- `docs/runbooks/secret-management.md` (new — chunk 3) is the operator's
  reference for which secrets exist, where they live, and how they rotate.

These are *deployment contracts*, not API contracts, but they get the same
discipline: written before code, reviewed in a hostile audit, kept current.

---

## 9. Definition of done (whole Stage F)

- A fresh clone of `hermes` on a clean Linux VM with Docker installed can:
  1. Run `scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin`.
  2. Run `make deploy-dev-up`.
  3. Browse to `http://localhost:5173`, log in (with seeded admin user
     from chunk 5's `mbs-bootstrap.md`), and see the inbox.
- The same VM can switch to prod via:
  1. `scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin`
  2. `cp .env.prod.example .env.prod && editor .env.prod`
  3. `make docker-build-all`
  4. `make deploy-prod-up`
  5. Browse to `https://hermes.example.com` (Caddyfile-fronted) and log in.
- All five chunk hostile audits attached and resolved.
- Memory + skill files updated to reflect Stage F completion.
