# Stage F Chunk 3 — `docker-compose.prod.yml` + secret externalisation

**Owner:** Oracle
**Created:** 2026-05-29
**Status:** Plan + contracts written, awaiting chunks 1+2 boot verification
**Parent:** `.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`

---

## 1. Goal

Ship `docker-compose.prod.yml` that boots the full Hermes stack from
**built images** (no source mount, no `go run`, no `npm run dev`)
against externalised file-based secrets, with restart policies,
resource limits, and a single-VPS sizing posture.

This chunk produces the "deploy to a single Hetzner / Linode / Mac
mini" baseline that bridges from dev to K8s.

---

## 2. Constraints inherited

- Compose-first (no K8s manifests).
- Single-host posture (no swarm, no nomad).
- File-based Docker secrets (no Vault, no SOPS — those are separate
  stages when Sam picks a managed store).
- Alpine images throughout (decided 2026-05-29).
- In-compose nginx for web (decided 2026-05-29).
- `MAUTRIX_DISABLE_TLS=false` enforced.
- mbs internal port 8082, `MBS_ADDR=mbs:8082`.

---

## 3. Contracts

### 3.1 File layout

```
docker-compose.prod.yml      ← new; image-based stack
.env.prod.example            ← new; committed placeholder
deploy/secrets/prod/         ← new directory
  .gitignore                 ← *.bin / *.hex / *.key / *.pem / *.token / .env (! .example)
  mbs-dek.bin.example        ← 32-byte NUL placeholder
  jwt-signing-key.example    ← 32-byte NUL placeholder (gateway HS256 key)
  postgres-password.example  ← placeholder text "REPLACE_ME"
  bizapp-client-token.example ← placeholder text "REPLACE_ME"
docs/runbooks/
  compose-deploy.md          ← extended with prod section
  secret-management.md       ← new; DEK / JWT / PG / BIZAPP rotation
  env-reference.md           ← extended with prod-only overrides
```

### 3.2 Image references

Every service in prod compose uses `image: hermes-<svc>:${HERMES_VERSION}`.
`HERMES_VERSION` comes from `.env.prod` (default `latest` if unset, but
operator runbook recommends pinning to a git tag).

No `build:` blocks. Builds happen out-of-band via `make
docker-build-all` and (optional future) `docker push` to a registry.

### 3.3 Secrets contract

#### `mbs_dek`
- Host file: `./deploy/secrets/prod/mbs-dek.bin` (gitignored)
- Container path: `/run/secrets/mbs_dek`
- Mode: `0400`
- Consumer: `mbs` service, env `HERMES_MBS_DEK_FILE=/run/secrets/mbs_dek`
- Rotation: see `secret-management.md` §DEK

#### `jwt_signing_key`
- Host file: `./deploy/secrets/prod/jwt-signing-key` (gitignored)
- Container path: `/run/secrets/jwt_signing_key`
- Mode: `0400`
- Consumer: `gateway` service
- Current gateway reads `JWT_SECRET` env. Chunk 3 introduces support for
  `JWT_SECRET_FILE` env (file-based fallback when set). Same pattern as
  `HERMES_MBS_DEK_FILE`. Small Go code change required —
  `internal/gateway/config/config.go` adds `JWTSecretFile` and `Load`
  prefers it over `JWTSecret`.
- Rotation: see `secret-management.md` §JWT

#### `postgres_password`
- Host file: `./deploy/secrets/prod/postgres-password` (gitignored)
- Container path: `/run/secrets/postgres_password`
- Mode: `0400`
- Consumer: postgres image uses `POSTGRES_PASSWORD_FILE`. Service
  containers consume the password via `DATABASE_URL` which now
  interpolates from env. **Pattern:** boot wrapper resolves
  `DATABASE_URL_FILE` if set, reads the file, exports `DATABASE_URL`.
  - Alternative: leave `DATABASE_URL` env-only (simpler, but password
    is visible to `docker inspect`).
- **Decision:** stay env-only for chunk 3. Password is in `.env.prod`
  (gitignored). Docker secrets cover only the truly high-value secrets
  (DEK, JWT, BIZAPP). PG password rotation is a coordinated drift —
  documented separately, not chunk-3 scope to automate.

#### `bizapp_client_token`
- Host file: `./deploy/secrets/prod/bizapp-client-token` (gitignored)
- Container path: `/run/secrets/bizapp_client_token`
- Mode: `0400`
- Consumer: `mbs` service. Currently mautrix-meta bridge driver reads
  `BIZAPP_CLIENT_TOKEN` env. Chunk 3 introduces `BIZAPP_CLIENT_TOKEN_FILE`
  env support (file-based fallback).
- Rotation: see `secret-management.md` §BIZAPP

### 3.4 `.env.prod.example` contract

```sh
# ============================================================
# Hermes Production Compose Env
# Copy to .env.prod and fill in real values.
# ============================================================

# Image version (pin to a git tag in prod)
HERMES_VERSION=latest

# Postgres
POSTGRES_DB=hermes
POSTGRES_USER=hermes
POSTGRES_PASSWORD=CHANGE_ME_STRONG_PASSWORD

# JWT signing (gateway)
# Either set JWT_SECRET inline (dev posture) or mount the file secret
# (prod posture, recommended) and leave this empty.
JWT_SECRET=

# Mautrix bridge driver (BizApp client token)
# Either set inline or use the file secret. Empty here = use the file.
BIZAPP_CLIENT_TOKEN=

# Per-pod identity. Override for multi-replica deployments.
POD_ID_MBS=hermes-mbs-prod-0
POD_ID_WA=hermes-wa-prod-0

# Operational tuning
MBS_BRIDGE_MAX_CONCURRENT=10
MBS_STREAM_REPLICAS=1
LOG_LEVEL=info

# Public-facing hostname (used by Caddy reverse proxy — chunk 5)
HERMES_DOMAIN=hermes.example.com
```

### 3.5 Resource limits

Compose v2 `deploy.resources` works with `docker-compose` v2 + `compose
v3.x` schema even outside Swarm (Compose Spec accepts it). Limits:

| Service | CPU limit | Memory limit | Memory reservation |
|---|---|---|---|
| postgres | 2.0 | 1G | 512M |
| redis | 0.5 | 256M | 128M |
| nats | 1.0 | 512M | 256M |
| gateway | 1.0 | 256M | 128M |
| wa | 2.0 | 512M | 256M |
| mbs | 2.0 | 512M | 256M |
| campaign | 1.0 | 256M | 128M |
| inbox | 1.0 | 256M | 128M |
| contacts | 0.5 | 256M | 128M |
| proxy | 0.5 | 256M | 128M |
| notify | 0.5 | 256M | 128M |
| web (nginx) | 0.5 | 128M | 64M |

Total commit: ~5 GB memory limit, ~13 vCPU limit. Fits a 4 vCPU / 8 GB
VPS with breathing room; oversubscription on CPU is fine for normal
load.

### 3.6 Restart policy

Every service except `migrate`: `restart: unless-stopped`.
`migrate` runs once and exits; restart=no.

### 3.7 Logging

`logging.driver: json-file` with `max-size: 10m` and `max-file: 3`.
Caps total log on-disk per service at 30 MB. Operators rotate manually
or wire log shipping in a future stage.

### 3.8 Healthcheck

Identical contract to chunk 1: `wget --spider /readyz || nc -z localhost
<port>`. Chunk 4 promotes to pure `/readyz`. Until then, prod inherits
the same dual-probe.

### 3.9 Volumes

| Volume | Purpose | Backup target |
|---|---|---|
| `pgdata` | Postgres data dir | YES — `pg_dump` per runbook |
| `natsdata` | JetStream streams | YES — `nats stream backup` per runbook |
| `redisdata` | Redis persistence (if AOF/RDB enabled) | OPTIONAL — ephemeral acceptable |

No source bind-mounts. No anonymous volumes.

### 3.10 Network

Single `hermes-net` bridge. No exposed ports for backend services
except gateway (`8080`, `8081`) and web (`80` — fronted by Caddy from
chunk 5).

For chunk 3, we additionally expose mbs diag port `9092` and others'
metrics ports to host so smoke tests can curl them. Chunk 4 may remove
host exposure once Prometheus scrapes from inside the network.

### 3.11 Boot wrapper for secret-file fallback

A tiny static binary OR shell wrapper in each affected service container
that does:

```
[ -n "$JWT_SECRET" ] || { [ -f "$JWT_SECRET_FILE" ] && export JWT_SECRET=$(cat "$JWT_SECRET_FILE"); }
exec /app/app
```

**Decision:** push this logic into Go (`internal/gateway/config/config.go`
and `internal/mbs/config/config.go`) rather than a shell wrapper. Wrapper
breaks distroless-future, requires a shell in the image, and obscures the
read in process inspection. Go-side `loadSecret(envName, fileEnvName)`
helper in `pkg/config`.

---

## 4. Implementation steps

1. Write `pkg/config/secret.go` — new `LoadSecret(envName,
   fileEnvName string) string` helper. Tries env first; if empty, tries
   reading file path from `fileEnvName`; if both empty, returns "".
   Unit test in `pkg/config/secret_test.go`.
2. Patch `internal/gateway/config/config.go` to call `LoadSecret("JWT_SECRET",
   "JWT_SECRET_FILE")` instead of `GetEnv("JWT_SECRET", "")`. Test update.
3. Patch `internal/mbs/bridge/...` BIZAPP_CLIENT_TOKEN read path to use
   `LoadSecret("BIZAPP_CLIENT_TOKEN", "BIZAPP_CLIENT_TOKEN_FILE")`. Test
   update.
4. (DEK already uses `_FILE` env — no change.)
5. Create `deploy/secrets/prod/` with `.gitignore` + 4 `.example`
   placeholders.
6. Write `.env.prod.example`. Extend root `.gitignore` to cover
   `.env.prod`.
7. Write `docker-compose.prod.yml`. ~280 LOC.
8. Write `docs/runbooks/secret-management.md` — rotation procedures for
   DEK, JWT, PG password, BIZAPP token.
9. Extend `docs/runbooks/compose-deploy.md` with prod section.
10. Extend `docs/runbooks/env-reference.md` with prod-only env overrides
    table (HERMES_VERSION, POD_ID_*, HERMES_DOMAIN, *_FILE vars).
11. Add Makefile targets: `deploy-prod-up`, `deploy-prod-down`,
    `deploy-prod-logs`, `deploy-prod-ps`.
12. Build all images: `make docker-build-all && make docker-build-web`.
13. Generate prod secrets: `./scripts/dek-generate.sh
    deploy/secrets/prod/mbs-dek.bin` (similarly for jwt-signing-key,
    bizapp-client-token — runbook documents each).
14. `cp .env.prod.example .env.prod && editor .env.prod` (fill in
    POSTGRES_PASSWORD).
15. Boot: `docker compose -f docker-compose.prod.yml --env-file .env.prod
    up -d`.
16. Run acceptance gates.
17. Write hostile audit.
18. Commit.

---

## 5. Files inventory (anticipated diff shape)

```
NEW:
  docker-compose.prod.yml                                       [+~280 LOC]
  .env.prod.example                                             [+~35 LOC]
  deploy/secrets/prod/.gitignore                                [+2 LOC]
  deploy/secrets/prod/mbs-dek.bin.example                       [32 bytes NUL]
  deploy/secrets/prod/jwt-signing-key.example                   [32 bytes NUL]
  deploy/secrets/prod/postgres-password.example                 [+1 LOC text]
  deploy/secrets/prod/bizapp-client-token.example               [+1 LOC text]
  pkg/config/secret.go                                          [+~30 LOC]
  pkg/config/secret_test.go                                     [+~50 LOC]
  docs/runbooks/secret-management.md                            [+~200 LOC]
  .hermes/plans/2026-05-29_stage-f-chunk3-prod-compose.md       [this file]
  docs/research/mbs-f-chunk3-hostile-audit-2026-05-29.md        [+~250 LOC]

MODIFIED:
  internal/gateway/config/config.go                             [+~3 LOC, ~1 LOC]
  internal/gateway/config/config_test.go                        [+~25 LOC]
  internal/mbs/bridge/<file>.go                                 [+~3 LOC, ~1 LOC]
  internal/mbs/bridge/<file>_test.go                            [+~25 LOC]
  docs/runbooks/compose-deploy.md                               [+~150 LOC append]
  docs/runbooks/env-reference.md                                [+~80 LOC append]
  Makefile                                                      [+~25 LOC]
  .gitignore                                                    [+1 LOC: .env.prod]
```

---

## 6. Acceptance gates

| # | Gate | Command | Pass |
|---|---|---|---|
| 1 | Prod compose boots from images | `docker compose -f docker-compose.prod.yml --env-file .env.prod up -d` | exit 0 |
| 2 | All services Up | `docker compose -f docker-compose.prod.yml ps` | every service Up/Healthy |
| 3 | mbs reads DEK from secret | `docker compose -f docker-compose.prod.yml exec mbs cat /run/secrets/mbs_dek \| wc -c` | 32 |
| 4 | Gateway reads JWT from secret | `docker compose -f docker-compose.prod.yml exec gateway cat /run/secrets/jwt_signing_key \| wc -c` | non-zero |
| 5 | JWT env not in plaintext | `docker compose -f docker-compose.prod.yml exec gateway env \| grep JWT_SECRET` | empty (only `JWT_SECRET_FILE` set) |
| 6 | mbs container restarts on kill | `docker kill <mbs-container>; sleep 5; docker ps \| grep mbs` | Up again |
| 7 | Resource limits enforced | `docker stats --no-stream <mbs-container>` | shows `512M` memory limit |
| 8 | Log rotation in effect | `docker inspect <mbs-container> \| jq '.[0].HostConfig.LogConfig'` | `max-size: 10m`, `max-file: 3` |
| 9 | go test still green | `go test -race -count=1 ./...` | ok |
| 10 | Dev compose still works | `docker compose -f docker-compose.dev.yml config` | parses |
| 11 | No source bind mounts | `docker inspect <gateway-container> \| jq '.[0].Mounts'` | only volumes + secrets, no `/src` |
| 12 | Memory ceiling sane | sum all `mem_limit` | < 8 GB |

---

## 7. Hostile-audit categories

- **Secrets leakage:** `docker inspect <svc>` does not surface any
  secret values. `docker compose config` shows file paths but not file
  contents.
- **`/run/secrets/` permission drift:** verify `0400` on every mount.
- **Restart loop hazard:** if mbs panics 100× in 60s, does the host
  OOM? (Resource limits cap blast radius.)
- **Volume orphan:** if `docker compose down -v` is run, do any
  named volumes leak? (Compose tracks them; pruning is explicit.)
- **CPU contention:** with all limits added together, can a noisy
  neighbour starve mbs? (Total limit > host vCPU; CPU is best-effort,
  noisy neighbour possible. Documented.)
- **Boot order under image registry latency:** if `image:` pull
  for mbs is slow, does the rest of the stack wait or race? (`depends_on`
  enforces; documented.)
- **Migration idempotency:** running prod compose twice should re-apply
  zero migrations (`migrate up` is idempotent at the file level; verify).
- **JWT secret file readability:** does the gateway container's
  non-root user (UID 65532) have read permission on
  `/run/secrets/jwt_signing_key` mounted at `0400 root:root`?
  (Docker secrets are owned by root inside the container — chmod 0400
  works only if reader is root. For non-root reader, mount mode must be
  0444 OR the secret must be `chown`'d. Audit clarifies.)
- **MAUTRIX_DISABLE_TLS guard:** verify prod compose explicitly sets
  `MAUTRIX_DISABLE_TLS=false` even though that's the default — defence
  in depth.
- **POD_ID collision:** verify each service that supports POD_ID has it
  explicitly set from `.env.prod`.
- **Network reachability:** verify host firewall only exposes 80 (web)
  and 8080+8081 (gateway). Backends bound only to `hermes-net`.

---

## 8. Risk: secret-file UID/GID collision (R10, new)

Docker compose `secrets.mode: 0400` sets `rw-------` for **root inside
the container**. Our images run as UID 65532. If the secret file is
owned root:root with mode 0400, our process cannot read it.

**Mitigation options:**

- A: Use `mode: 0444` (world-readable). Pro: works. Con: any compromised
  process in the container can read secrets.
- B: Use `uid: 65532, gid: 65532, mode: 0400`. Pro: only our user.
  Compose supports this per service.
- C: Use Docker's `secrets` configuration which is currently swarm-only
  for the full UID feature — not applicable in single-host compose.

**Decision:** option B. Per-secret mount block:

```yaml
secrets:
  - source: mbs_dek
    target: mbs_dek
    uid: "65532"
    gid: "65532"
    mode: 0400
```

Chunk 3 contracts updated to reflect this. The dev compose chunk-1
mount currently uses `mode: 0400` without uid/gid — and the dev image
(from `Dockerfile.dev`) runs as **root** so it works. Once chunk 2 prod
Dockerfile lands and dev compose adopts the non-root image, dev
compose's mbs_dek mount must also flip to `uid/gid: 65532`. **Add a
follow-up note** to chunk 5 to verify dev-after-prod-build still
works.

---

## 9. Out of scope reminders

- TLS termination / reverse proxy → chunk 5.
- Backup automation → chunk 5 runbook (manual procedure).
- Image registry push.
- Multi-replica scale-out.
- Database connection pooler (pgbouncer).
- Read replicas.
- NATS clustering (single node only this stage; replicas=1 enforced
  via env).

---

## 10. Rollback

```sh
docker compose -f docker-compose.prod.yml down --remove-orphans
git revert <commit-sha>
docker compose -f docker-compose.dev.yml up -d   # back to dev hack-loop
```

No data migrations in chunk 3 (the secret-file Go code is read-only).

---
