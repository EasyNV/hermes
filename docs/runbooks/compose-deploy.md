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

## What's not yet here (and why)

- **`docker-compose.prod.yml`** — chunk 3.
- **A real `/readyz` on every service** — chunk 4.
- **Reverse-proxy fronting (Caddy/nginx)** — chunk 5.
- **Backup + restore procedure** — chunk 5
  (`docs/runbooks/backup-restore.md`).
- **Secret rotation procedure** — chunk 3
  (`docs/runbooks/secret-management.md`).
- **Image registry / `docker push`** — out of Stage F scope.
- **Kubernetes manifests** — Stage G.

The Stage F master plan
(`.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`)
tracks all of these.
