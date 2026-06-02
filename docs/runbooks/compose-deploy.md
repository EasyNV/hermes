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
# Expect: -r--------    1 root  root    65 ... (64 hex chars + trailing newline)

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
ls -l deploy/secrets/dev/mbs-dek.bin     # must exist, 65 bytes, mode 0400
wc -c deploy/secrets/dev/mbs-dek.bin     # must print 65
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
# Must print 65 (64 hex chars + trailing newline).
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

# 3. Bootstrap secrets. The DEK and JWT signing key are 64-char hex files (32 bytes entropy + newline).
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

## 6. Putting the stack behind a reverse proxy

By default the compose stack binds `gateway:8081` (REST/WS) and the web container on `127.0.0.1:80`. To expose these publicly with TLS, put **Caddy** or **nginx** in front of the stack on the same host. Both options share the same URL surface — `/api/*` and `/ws` go to gateway, everything else hits the web SPA. METRICS_PORT endpoints (`/metrics`, `/readyz`, `/livez`) are NEVER reachable through the proxy (chunk-4 invariant).

### 6.1 Option A — Caddy (default; auto-TLS)

```sh
# Install Caddy on Debian/Ubuntu
sudo apt install -y caddy

# Install the Hermes Caddyfile
sudo cp deploy/caddy/Caddyfile.example /etc/caddy/Caddyfile

# Set env vars (the systemd unit reads /etc/default/caddy by default)
sudo tee -a /etc/default/caddy >/dev/null <<'EOF'
HERMES_DOMAIN=hermes.example.com
HERMES_ACME_EMAIL=ops@example.com
EOF

# Reload
sudo systemctl reload caddy

# Verify
sudo journalctl -u caddy --since '1 minute ago' | grep -E '(certificate obtained|http2|serving)'
```

Acceptance:
```sh
curl -sS https://hermes.example.com/api/v1/auth/me   # 401 (no token) — proves /api/* is reaching gateway
curl -sS https://hermes.example.com/             # SPA HTML
curl -sS https://hermes.example.com/metrics      # 404 (correctly NOT proxied)
```

### 6.2 Option B — nginx + certbot

```sh
sudo apt install -y nginx certbot python3-certbot-nginx

# Replace HERMES_DOMAIN in the example, then install
sed 's/HERMES_DOMAIN/hermes.example.com/g' deploy/nginx/hermes.conf.example | \
    sudo tee /etc/nginx/sites-available/hermes.conf >/dev/null
sudo ln -sf /etc/nginx/sites-available/hermes.conf /etc/nginx/sites-enabled/

# Issue cert via certbot — adds the ssl_certificate paths and a HTTP redirect
sudo certbot --nginx -d hermes.example.com --non-interactive --agree-tos -m ops@example.com

# Verify nginx config, reload
sudo nginx -t && sudo systemctl reload nginx
```

Acceptance: same three curls as §6.1.

### 6.3 What this gives you

| Endpoint | Public surface | Backend |
|---|---|---|
| `https://<domain>/` | TLS-terminated SPA | `web:80` (in-compose nginx) |
| `https://<domain>/api/*` | REST API | `gateway:8081` |
| `https://<domain>/ws` | WebSocket upgrade | `gateway:8081` |
| `https://<domain>/metrics` | 404 (intentional) | not proxied |
| `https://<domain>/readyz` | 404 (intentional) | not proxied |

### 6.4 Common gotchas

- **Caddy ACME rate limit during smoke test** — uncomment the staging ACME directive in `Caddyfile.example` to avoid Let's Encrypt's 5 certs/domain/week production cap.
- **nginx OSS lacks active health checks** — the upstream block uses passive `max_fails=3 fail_timeout=10s`. For active probing of gateway's `/readyz`, you need nginx Plus's `health_check uri=/readyz port=9100`.
- **WebSocket disconnects after ~60s** — you didn't add `proxy_read_timeout 7d` to `/ws` in nginx (or `read_timeout 0` in Caddy). The example configs both have this baked in.
- **`X-Forwarded-For` doesn't reach gateway logs** — the example nginx config sets it; Caddy sets it automatically. Verify by tailing `docker logs hermes-gateway-1` while curl'ing from a different IP.

---

## 7. Tear-down

```sh
make deploy-prod-down               # stop + remove containers, KEEP volumes
docker-compose -f docker-compose.prod.yml down -v   # also drop volumes (destructive)
```

The DEK lives in `deploy/secrets/prod/mbs-dek.bin` on the host filesystem. Loss of that file → loss of every encrypted blob's plaintext. **Back it up before you tear anything down.** See `docs/runbooks/backup-restore.md` §3 and `docs/runbooks/secret-management.md` §5.

---

## 8. What's NOT in this runbook

The Stage F master plan covers the full scope. What's deferred to **Stage G or later**:

- **Image registry / `docker push`** — single-host VPS deployment uses local images.
- **Kubernetes manifests** — out of Stage F.
- **Cron-driven backups** — manual procedure documented in `backup-restore.md`; automation is operator's call.
- **Prometheus / Grafana** — services expose `/metrics` per chunk 4, but federation + dashboards are Stage G.
- **Log aggregation (Loki / Vector)** — services log to stdout; Docker captures via the json-file driver. Centralisation is Stage G.
- **WAL archiving / PITR** — `pg_dump` is sufficient for single-host operations; WAL archiving is Stage G+.

Cross-references for everything that IS in scope:
- Backup + restore procedures: `docs/runbooks/backup-restore.md`
- First-deploy walkthrough: `docs/runbooks/mbs-bootstrap.md`
- Secret rotation: `docs/runbooks/secret-management.md`
- Env reference: `docs/runbooks/env-reference.md`
- Reverse proxy configs: `deploy/caddy/Caddyfile.example`, `deploy/nginx/hermes.conf.example`
