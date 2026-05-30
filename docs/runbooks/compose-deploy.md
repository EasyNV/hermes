# Compose Deploy — Operator Runbook

**Stage:** F — Deploy Hardening
**Status:** Living document. This revision (chunk 1) covers **dev compose
only**. Prod compose (`docker-compose.prod.yml`), reverse-proxy fronting,
and backup/restore land in chunks 3 and 5.

This is the operator's reference for booting, inspecting, and tearing
down the full Hermes stack on a single host via Docker Compose.

---

## Prerequisites

- Docker 24+ with Compose v2 (`docker compose ...`) or standalone
  `docker-compose` v2.x (`docker-compose ...`). All examples below use
  the standalone `docker-compose` form because Sam's Mac currently runs
  Docker Compose v5.1.2 standalone. The two CLIs are interchangeable for
  the commands used here.
- `openssl` for DEK generation.
- ~4 GB of free RAM (8 services + Postgres + Redis + NATS + node dev
  server). 8 GB recommended.
- Ports 5173, 8080, 8081, 8082, 4222, 5433, 6380, 8222, 9092, 9101-9106
  available on the host.

---

## First-time setup (dev)

```sh
# 1. Generate the MBS Data Encryption Key. Refuses to overwrite if it
#    already exists — rotation is a separate procedure (chunk 3).
./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin

# 2. Boot the stack.
docker-compose -f docker-compose.dev.yml up --build -d

# 3. Wait for all services to report healthy. ~60-90s on a cold cache,
#    ~30s after the first build.
docker-compose -f docker-compose.dev.yml ps
```

When `docker-compose ps` shows every service `Up (healthy)` (or `Up`
for the ones whose healthcheck is still `nc -z`), the stack is live.

---

## Smoke tests

```sh
# Gateway gRPC-Web / REST surface
curl -fsS http://localhost:8080/healthz       # gateway gRPC port
curl -fsS http://localhost:8081/healthz       # gateway REST/WS port (TBD chunk 4)

# MBS service
curl -fsS http://localhost:9092/livez
curl -fsS http://localhost:9092/readyz

# Frontend (Vite dev server)
curl -fsS -I http://localhost:5173/           # 200 OK

# Inspect what migrations applied for mbs
docker-compose -f docker-compose.dev.yml exec postgres \
  psql -U hermes -d hermes -c "SELECT version, dirty FROM schema_migrations_mbs;"
# Expect: latest version = 3 (000003_pod_id_and_freshness)

# Inspect DEK mount inside mbs container
docker-compose -f docker-compose.dev.yml exec mbs ls -l /run/secrets/mbs_dek
# Expect: -r--------    1 root  root    32 ...

# Confirm gateway can dial mbs
docker-compose -f docker-compose.dev.yml logs --tail=200 gateway 2>&1 | grep -i mbs
# Expect: log lines about gRPC connection to mbs:8082, no "connection refused"

# Confirm gateway can dial mbs from inside its container
docker-compose -f docker-compose.dev.yml exec gateway nc -zv mbs 8082
# Expect: succeeded
```

---

## Boot order

Compose enforces the following partial order via `depends_on`:

```
postgres ──┐
           ├──► migrate ─────────────────────────┐
nats ──────┤                                     │
redis ─────┤                                     │
           └──► proxy ───┬────────────────────► gateway
                         └──► wa ──┐
contacts ──────────────────────────┤
notify   ──────────────────────────┤
mbs      ──────────────────────────┤   (added Stage F chunk 1)
campaign ──────────────────────────┤
inbox    ──────────────────────────┘
                                   └──► gateway ──► web
```

Notes:

- `mbs` boots independently — it has no gRPC dependency on any other
  backend service. It needs `postgres`, `nats`, and `migrate` only.
- `gateway` waits for **every** backend to report `service_started`
  (not `service_healthy`). This is intentional: gateway uses gRPC
  reconnect to tolerate backends warming up. Chunk 4 introduces real
  `service_healthy` gating once each service has a proper `/readyz`.
- `migrate` is a one-shot init container. It runs all migrations
  serially (one per service-prefixed `schema_migrations_<svc>` table)
  and exits 0. The other services wait on its
  `service_completed_successfully`.

---

## Inspecting logs

```sh
# Live tail
docker-compose -f docker-compose.dev.yml logs -f mbs

# Crash retrieval — last 500 lines, even if container is exited
docker-compose -f docker-compose.dev.yml logs --tail=500 mbs

# All services together (useful for boot-order races)
docker-compose -f docker-compose.dev.yml logs -f --tail=100
```

---

## Healthcheck semantics (current, chunk 1)

| Service | Probe | Real `/readyz`? |
|---|---|---|
| postgres | `pg_isready -U hermes` | n/a (PG) |
| redis | `redis-cli ping` | n/a (Redis) |
| nats | `wget --spider /healthz` | yes (NATS built-in) |
| migrate | (none — it exits) | n/a |
| proxy, contacts, notify, wa, inbox, campaign, gateway | `nc -z localhost <port>` | **no — TCP-open only** |
| mbs | `wget --spider /readyz \|\| nc -z localhost 8082` | yes (cmd/mbs/main.go exposes it) |

The `nc -z` probe says "port is bound" — it does **not** confirm gRPC
reflection is registered, DB pool is warm, NATS subscriptions are
established, or the service is in steady state.

Stage F chunk 4 promotes every service to a real `/readyz` probe.

---

## Common operations

### Reset DB + state

```sh
docker-compose -f docker-compose.dev.yml down -v   # -v drops volumes
./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin   # if you also wiped the DEK
docker-compose -f docker-compose.dev.yml up --build -d
```

### Rebuild only one service after a code change

```sh
docker-compose -f docker-compose.dev.yml build mbs
docker-compose -f docker-compose.dev.yml up -d mbs
```

### Open a shell inside a service container

```sh
docker-compose -f docker-compose.dev.yml exec mbs /bin/sh
```

### Watch resource usage

```sh
docker stats $(docker-compose -f docker-compose.dev.yml ps -q)
```

---

## Clean shutdown

```sh
docker-compose -f docker-compose.dev.yml down --remove-orphans
```

To also drop the PG + NATS data volumes:

```sh
docker-compose -f docker-compose.dev.yml down --remove-orphans -v
```

This does **not** delete `deploy/secrets/dev/mbs-dek.bin` — your DEK
persists across stack tears. If you ever need to rotate it, see
`docs/runbooks/secret-management.md` (Stage F chunk 3).

---

## Troubleshooting

### `mbs` exits immediately at boot

Almost always missing or malformed DEK. Verify:

```sh
ls -l deploy/secrets/dev/mbs-dek.bin     # must exist, 32 bytes, mode 0400
wc -c deploy/secrets/dev/mbs-dek.bin     # must print 32
```

If the file is missing, generate it (`./scripts/dek-generate.sh
deploy/secrets/dev/mbs-dek.bin`). If it's the wrong size, delete and
regenerate.

### Other dev clones / second compose stack on the same host

Both stacks share `POD_ID=hermes-mbs-dev` by default, which causes
pod-claim row contention on the `mbs_sessions` table. Either:

- Run only one stack at a time, or
- Override `POD_ID` in a `.env` file picked up by the second stack:
  `POD_ID=hermes-mbs-dev-second`.

### Gateway logs `connection refused` to `mbs:8082`

Either `mbs` hasn't finished its boot sequence (wait 10-20s), or its
healthcheck is failing because the DEK file is unreadable inside the
container:

```sh
docker-compose -f docker-compose.dev.yml exec mbs cat /run/secrets/mbs_dek | wc -c
# Must print 32.
```

### Port already in use

Default-bound host ports are listed under "Prerequisites" above. If any
of them collide with something on your host, edit the `ports:` block
for that service in `docker-compose.dev.yml` (or omit the port
entirely if you only need in-network access).

---

## Prod deploy (chunk 3)

The prod compose stack (`docker-compose.prod.yml`) boots from prebuilt
images instead of mounting source. The wire surface is identical to
dev — gateway, web, NATS, Postgres — but every service runs from
`hermes-<svc>:${HERMES_VERSION}` images you build out-of-band via
`make docker-build-all`.

### Prod prerequisites

- Docker Engine ≥ 24 (Compose v2 / Buildx required).
- `make docker-build-all` has succeeded on this host (or images are
  pulled from a registry — see "image registry" below).
- The repo is checked out (proto sources, migrations).
- One host has ≥ 4 vCPU / 8 GB RAM (the resource limits in the prod
  compose total ~5 GB memory; the limits cap blast radius, they do not
  reserve).

### Build artifacts

The prod stack expects these container images to exist locally (or be
pullable):

```
hermes-gateway:<version>
hermes-wa:<version>
hermes-mbs:<version>
hermes-campaign:<version>
hermes-inbox:<version>
hermes-contacts:<version>
hermes-proxy:<version>
hermes-notify:<version>
hermes-web:<version>
```

Where `<version>` matches `HERMES_VERSION` in `.env.prod` (default
`latest`).

**Operator contract:** before `make docker-build-all`, run `make
proto-gen` so `gen/go/hermes/v1/*.pb.go` are present in the build
context. The `docker-build-*` Makefile targets depend on `proto-gen`
so this is automatic if you go through the Makefile.

### First-time deploy procedure

```sh
# 1. Generate the proto stubs (idempotent, fast).
make proto-gen

# 2. Build every image. ~5-10 minutes on a 4-core machine; mostly
#    spent inside the mbs build pulling mautrix-meta + utls deps.
make docker-build-all

# 3. Bootstrap secrets. The DEK and JWT signing key are 32-byte hex.
./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin
./scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key

# 4. PG password — 32 chars, ASCII, no metachars. See
#    docs/runbooks/secret-management.md §3 for full rationale.
PASSWORD=$(openssl rand -base64 32 | tr -d '=+/' | head -c 32)
printf '%s' "$PASSWORD" > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/postgres-password

# 5. .env.prod
cp .env.prod.example .env.prod
# Edit .env.prod and set DATABASE_URL to include the same PG password
# you wrote in step 4. The file is gitignored; .env.prod.example is
# the committed template.
editor .env.prod

# 6. Boot.
make deploy-prod-up

# 7. Verify.
make deploy-prod-ps
# Every service should be Up (healthy) within ~30 seconds.

# 8. Smoke test.
curl -fsS http://localhost:8081/api/v1/healthz   # gateway
curl -fsS http://localhost/healthz               # web
docker-compose -f docker-compose.prod.yml exec mbs \
  wget --spider -q http://localhost:9092/readyz && echo MBS_READY
```

### Day-2 controls (Makefile targets)

```sh
make deploy-prod-up      # boot (idempotent; safe to re-run after edits)
make deploy-prod-down    # graceful stop, keep volumes
make deploy-prod-ps      # show service status
make deploy-prod-logs    # tail aggregated logs
make deploy-prod-restart # rolling restart
```

`deploy-prod-up` refuses to run if `.env.prod` or any required secret
file is missing. The error message tells you which one and how to
generate it.

### What's mounted vs what's not

The prod stack uses **no source bind mounts**. The only host->container
mounts are:

- `./migrations` → `/migrations:ro` (migrate init container only)
- Docker secrets (`deploy/secrets/prod/*`) → `/run/secrets/*`
- Named volumes: `pgdata`, `natsdata`, `redisdata`

Backend Go containers run **read-only** with a `/tmp` tmpfs. mbs in
particular is verified clean (chunk-2 hostile audit F5).

### Image registry (future)

Stage F doesn't ship a `docker push` workflow — images live in the
local Docker daemon. To deploy to a remote VPS today: build locally
then `docker save | ssh remote 'docker load'`, or set up a private
registry and `docker push` in your wrap script. CI/CD wiring is a
follow-up stage.

### Restart behaviour

Every service except `migrate` has `restart: unless-stopped`. A
panicking service will be restarted by the Docker daemon indefinitely.
Without monitoring this will silently masquerade as healthy at the
docker layer — see master-plan R5 — but the resource limits cap the
blast radius. Future stage adds Prometheus alerting.

### Resource limits summary

| Service | CPU | Mem limit | Mem reserve |
|---|--:|--:|--:|
| postgres | 2.0 | 1 GB | 512 MB |
| nats | 1.0 | 512 MB | 256 MB |
| redis | 0.5 | 256 MB | 128 MB |
| gateway | 1.0 | 256 MB | 128 MB |
| wa | 2.0 | 512 MB | 256 MB |
| mbs | 2.0 | 512 MB | 256 MB |
| campaign | 1.0 | 256 MB | 128 MB |
| inbox | 1.0 | 256 MB | 128 MB |
| contacts | 0.5 | 256 MB | 128 MB |
| proxy | 0.5 | 256 MB | 128 MB |
| notify | 0.5 | 256 MB | 128 MB |
| web | 0.5 | 128 MB | 64 MB |
| **Total** | 12.5 | 5.0 GB | 2.5 GB |

CPU is oversubscribed against a 4-vCPU host — that's fine because Docker
CPU limits are *quota*, not *reservation*. Memory limits are hard caps
(OOM-kill on overrun); reservations are scheduling hints.

### Troubleshooting prod

#### `make deploy-prod-up` fails with "secrets missing"

Run the missing step from the first-time procedure above. The
Makefile target prints the exact command.

#### `migrate` container exits non-zero

The PG password in `.env.prod` (the inline part of `DATABASE_URL`)
doesn't match `deploy/secrets/prod/postgres-password`. Both must be
the same string. See `docs/runbooks/secret-management.md` §3.

#### `gateway` logs "JWT secret not set"

`JWT_SECRET_FILE=/run/secrets/jwt_signing_key` env is set in the
compose file, but the secret file is empty or unreadable. Check:

```sh
docker-compose -f docker-compose.prod.yml exec gateway \
  wc -c /run/secrets/jwt_signing_key
# Must print 65 (64 hex chars + newline).
```

If 0 / missing: regenerate via `./scripts/dek-generate.sh
deploy/secrets/prod/jwt-signing-key` and restart gateway.

#### A backend container restarts continuously

```sh
make deploy-prod-logs
# Look for "fatal" / "panic" lines.
docker-compose -f docker-compose.prod.yml --env-file .env.prod \
  logs --tail=100 <svc>
```

Common causes:

- Postgres password mismatch → see migrate troubleshooting.
- Missing/wrong DEK on `mbs` → §1 of secret-management.md.
- Migration table conflict → re-run migrations: `make migrate`.

### Tear-down

```sh
make deploy-prod-down               # stop + remove containers, KEEP volumes
docker-compose -f docker-compose.prod.yml down -v   # also drop volumes (destructive)
```

The DEK lives in `deploy/secrets/prod/mbs-dek.bin` on the host
filesystem. Loss of that file → loss of every encrypted blob's
plaintext. **Back it up before you tear anything down.** See
`docs/runbooks/secret-management.md` §5.

---

## What's not yet here (and why)

- **A real `/readyz` on every service** — chunk 4.
- **Reverse-proxy fronting (Caddy/nginx)** — chunk 5.
- **Backup + restore procedure** — chunk 5
  (`docs/runbooks/backup-restore.md`).
- **Image registry / `docker push`** — out of Stage F scope.
- **Kubernetes manifests** — Stage G.

The Stage F master plan
(`.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`)
tracks all of these.
