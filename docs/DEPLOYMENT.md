# Hermes Deployment

Hermes currently supports local development and single-host production-style deployment through Docker Compose.

The full stack contains 12 compose services:

- `postgres`
- `redis`
- `nats`
- `migrate`
- `proxy`
- `contacts`
- `notify`
- `wa`
- `campaign`
- `inbox`
- `mbs`
- `gateway`
- `web`

> Count note: `migrate` is a run-once init container, not a long-running backend service.

## Prerequisites

- Docker Engine / Docker Desktop with Compose support.
- Go 1.25 for host builds/tests.
- Node.js 22+ / npm for frontend work.
- Git submodules initialized.
- `openssl` or equivalent random generator for secret provisioning.
- Optional but recommended: `buf`, `gosec`, `govulncheck`, `staticcheck`, `semgrep`, `gitleaks`, `trufflehog`.

Initialize submodules:

```bash
git submodule update --init --recursive
```

## Development deployment

Generate the MBS development DEK once:

```bash
./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin
```

Boot dev compose:

```bash
make deploy-dev-up
# equivalent:
docker-compose -f docker-compose.dev.yml up -d
```

Watch logs:

```bash
make deploy-dev-logs
```

Inspect service state:

```bash
make deploy-dev-ps
```

Tear down:

```bash
make deploy-dev-down
```

### Development ports

- Frontend Vite: `5173`
- Gateway gRPC: `8080`
- Gateway REST/WS: `8081`
- MBS gRPC: `8082`
- MBS metrics/health: `9092`
- Proxy: `9101`
- Contacts: `9102`
- Notify: `9103`
- WA: `9104`
- Campaign: `9105`
- Inbox: `9106`
- Postgres: `${HERMES_PG_HOST_PORT:-5433}`
- Redis: `6380`
- NATS client: `4222`
- NATS monitor: `8222`

### Development boot order

Compose starts infrastructure, runs migrations, then starts services in dependency order:

```text
postgres ─┐
redis ────┼──► migrate ─────────────┐
nats ─────┘                          │
                                     ▼
proxy, contacts, notify ─────────► wa, campaign, inbox, mbs ───► gateway ───► web
```

The gateway waits for backend health checks in the current compose files.

### Development smoke checks

```bash
curl -fsS http://localhost:8081/healthz
curl -fsS http://localhost:9092/livez
curl -fsS http://localhost:9092/readyz
curl -fsS -I http://localhost:5173/
```

Confirm gateway can reach MBS:

```bash
docker-compose -f docker-compose.dev.yml exec gateway nc -zv mbs 8082
```

## Production-style compose

Production compose uses prebuilt images and file-backed Docker secrets.

### Build images

```bash
make docker-build-all
```

This builds:

- `hermes-proxy:${HERMES_VERSION:-latest}`
- `hermes-contacts:${HERMES_VERSION:-latest}`
- `hermes-notify:${HERMES_VERSION:-latest}`
- `hermes-wa:${HERMES_VERSION:-latest}`
- `hermes-campaign:${HERMES_VERSION:-latest}`
- `hermes-inbox:${HERMES_VERSION:-latest}`
- `hermes-mbs:${HERMES_VERSION:-latest}`
- `hermes-gateway:${HERMES_VERSION:-latest}`
- `hermes-web:${HERMES_VERSION:-latest}`

### Provision secrets

Required production secret files:

- `deploy/secrets/prod/mbs-dek.bin` — 64 hex chars + trailing newline; 32 bytes entropy for the MBS data encryption key.
- `deploy/secrets/prod/jwt-signing-key` — 64 hex chars + trailing newline; 32 bytes entropy for JWT signing.
- `deploy/secrets/prod/postgres-password` — PostgreSQL password file.

Example bootstrap:

```bash
mkdir -p deploy/secrets/prod
./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin
./scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key
printf '%s' '[REDACTED_STRONG_POSTGRES_PASSWORD]' > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/*
```

`dek-generate.sh` writes 64 hex chars plus one trailing newline (65-byte file, 32 bytes entropy).

Back up the MBS DEK immediately. Losing it makes encrypted MBS session/blob rows unrecoverable.

### Configure environment

```bash
cp .env.prod.example .env.prod
```

Edit `.env.prod` and set deployment-specific values. Do not commit `.env.prod`.

Important fields:

- `HERMES_VERSION`
- `POSTGRES_DB`
- `POSTGRES_USER`
- `DATABASE_URL` with `[REDACTED]` password replaced locally.
- `POD_ID_MBS`
- `POD_ID_WA`
- `MBS_BRIDGE_MAX_CONCURRENT`
- `MBS_STREAM_REPLICAS`
- `LOG_LEVEL`
- `HERMES_WEB_PORT`
- `HERMES_GATEWAY_GRPC_PORT`
- `HERMES_GATEWAY_HTTP_PORT`
- `HERMES_DOMAIN`

### Start production compose

```bash
make deploy-prod-up
# equivalent:
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d
```

Inspect:

```bash
make deploy-prod-ps
make deploy-prod-logs
```

Restart:

```bash
make deploy-prod-restart
```

Stop:

```bash
make deploy-prod-down
```

## Migrations

Migrations are run by the `migrate` init container. Each service gets a separate migration table:

- `schema_migrations_gateway`
- `schema_migrations_wa`
- `schema_migrations_mbs`
- `schema_migrations_campaign`
- `schema_migrations_inbox`
- `schema_migrations_contacts`
- `schema_migrations_proxy`
- `schema_migrations_notify`

Manual host migration target:

```bash
make migrate
```

## Health checks

Production compose uses HTTP health probes where services expose metrics/health ports.

Common probes:

```bash
curl -fsS http://localhost:8081/healthz      # gateway HTTP
curl -fsS http://localhost:9092/livez        # mbs liveness
curl -fsS http://localhost:9092/readyz       # mbs readiness
curl -fsS http://localhost:8222/healthz      # nats monitor
```

## Reverse proxy / TLS

Production should be fronted by Caddy, nginx, or equivalent TLS termination. Gateway REST/WS and the frontend should not be exposed with permissive development CORS/origin assumptions.

Required proxy behavior:

- Serve frontend over HTTPS.
- Proxy `/api/v1/*` to gateway HTTP port.
- Proxy `/ws` and `/ws/mbs/bridge-login` with WebSocket upgrade support.
- Scrub query strings containing WebSocket JWTs from access logs.
- Restrict allowed origins.
- Apply security headers appropriate for the deployment.

## MBS bootstrap

Use `docs/runbooks/mbs-bootstrap.md` for first MBS session provisioning.

Current flow is email/password plus optional TOTP secret through the frontend bridge-login dialog. The old cookie-blob paste flow is retired for operator login; cookies/bridge envelope handling is now internal to the patched mautrix-meta + mbs-native path.

## Troubleshooting

### `mbs` exits or never becomes ready

Check the DEK mount:

```bash
docker-compose -f docker-compose.dev.yml exec mbs sh -c 'wc -c /run/secrets/mbs_dek'
```

Expected file size: `65` bytes, representing 64 hex chars plus one trailing newline.

In production, inspect Docker secrets and file permissions under `deploy/secrets/prod/`.

### Gateway cannot reach MBS

```bash
docker-compose -f docker-compose.dev.yml logs --tail=200 gateway
 docker-compose -f docker-compose.dev.yml exec gateway nc -zv mbs 8082
```

Check that `mbs` is healthy before gateway startup, and that `MBS_ADDR` points at `mbs:8082`.

### MBS bridge login fails

Check:

- Browser is authenticated and sends a valid JWT.
- `/ws/mbs/bridge-login` is proxied with WebSocket upgrade support.
- Gateway has `MBS_ADDR` configured and can dial `mbs:8082`.
- MBS has a valid DEK and DB/NATS connectivity.
- The Meta account is not checkpointed/burned and TOTP input is correct.
- Logs do not expose raw passwords, cookies, access tokens, or TOTP secrets.

### `go mod verify` fails for `mbs-native`

The main module uses local `replace` directives for `mbs-native`, `mautrix-meta-patched`, and a vendored `utls` fork. The latest audit observed `go mod verify` failing for `mbs-native` with missing ziphash metadata. Treat this as a current module-integrity status item to normalize/document before relying on `go mod verify` as a hard release gate.

### Frontend production build warning

The latest Vite production build passed but warned that the main JS chunk is around `680 kB`, above Vite's default `500 kB` warning threshold. Consider code splitting for the operator SPA if startup performance matters.
