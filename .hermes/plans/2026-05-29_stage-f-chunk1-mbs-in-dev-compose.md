# Stage F Chunk 1 — `hermes-mbs` in dev compose + migrate loop + gateway wiring

**Owner:** Oracle
**Created:** 2026-05-29
**Status:** Plan written, contracts written, awaiting build phase
**Parent:** `.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`

---

## 1. Goal

Make `docker compose -f docker-compose.dev.yml up --build` boot `hermes-mbs`
cleanly alongside the other seven services on a fresh clone, with:

- All MBS migrations applied automatically.
- The DEK provisioned via a dev-default file mount (gitignored real file,
  committed example placeholder + README).
- `mbs:8082` reachable from `gateway` (and only from `gateway` — no host
  port required in chunk 1; we'll expose `8082`/`9092` for direct
  inspection during dev).
- `/livez` and `/readyz` returning 200 on `:9092`.
- Clean shutdown via `docker compose down`.

This chunk does **not** introduce a prod compose, a prod Dockerfile, or any
secret-management hardening beyond "DEK file is mounted from a gitignored
dev path." Those land in chunks 2-3.

---

## 2. Contracts

### 2.1 Environment contract (dev compose → hermes-mbs)

The compose `mbs` service block must export the following env. Names and
defaults verified against `internal/mbs/config/config.go::Load`.

| Env var | Value in dev compose | Source / rationale |
|---|---|---|
| `PORT` | `"8082"` | gRPC server port. Default matches `MBS_ADDR` consumer in gateway. |
| `METRICS_PORT` | `"9092"` | Diag HTTP server. `/livez` + `/readyz` + `/metrics` per cmd/mbs/main.go boot doc. |
| `POD_ID` | `"hermes-mbs-dev"` | Static in dev. Used by pod_id claim row (migration 000003). |
| `DATABASE_URL` | `postgres://hermes:hermes_dev@postgres:5432/hermes?sslmode=disable` | Same DSN pattern as wa/inbox. Plaintext password is intentional dev-only. |
| `DB_SSLMODE` | `"disable"` | Compose-internal; no TLS for local PG. |
| `NATS_URL` | `nats://nats:4222` | Compose-internal hostname. |
| `HERMES_MBS_DEK_FILE` | `/run/secrets/mbs_dek` | File mount path. Compose `secrets:` block maps it from host. |
| `MBS_BRIDGE_MAX_CONCURRENT` | `"5"` | Dev cap (mautrix-meta is RAM-hungry; safer ceiling than prod's 10). |
| `MBS_STREAM_REPLICAS` | `"1"` | Single-node NATS in compose. |
| `LOG_LEVEL` | `"debug"` | Mirrors other dev services. |

Env vars that hermes-mbs reads but we **do not** set in dev compose
(defaults apply):

- `DB_SSLROOTCERT` (empty — PG is plaintext local).
- `NATS_CREDS_FILE` (empty — no JWT auth on local NATS).
- `MBS_REFRESH_INTERVAL`, `MBS_REFRESH_THRESHOLD`, `MBS_REFRESH_CONCURRENCY`
  (defaults `1h`, `30d`, `5` are dev-appropriate).
- `MBS_BRIDGE_TIMEOUT`, `MBS_BRIDGE_2FA_TIMEOUT` (defaults `3m`, `2m`).
- `MAUTRIX_DISABLE_TLS` (default `false` — must stay false even in dev to
  avoid the documented blast-radius pitfall).
- `MBS_IMPORT_LEGACY_ON_STARTUP` (default `false`).
- `MBS_ENCRYPT_REWRITE_ON_STARTUP` (default `false`).
- `MBS_SHUTDOWN_DRAIN_TIMEOUT` (default `30s`).
- `GRPC_MAX_CONCURRENT_STREAMS`, `GRPC_KEEPALIVE_TIME`, `GRPC_KEEPALIVE_TIMEOUT`
  (defaults are fine).
- `MBS_ENABLE_PPROF` (default `false`; enable per-debug if needed).

### 2.2 Dependency contract

```
mbs.depends_on:
  postgres:  service_healthy
  nats:      service_healthy
  migrate:   service_completed_successfully
```

Note: **no dependency on other backend services.** mbs is a pure provider
(like wa). Other services may eventually depend on mbs being healthy, but
none currently do.

```
gateway.depends_on:
  + mbs: service_started     # added this chunk
```

Reasoning: gateway's `MBS_ADDR=mbs:8082` becomes a real connect target.
`service_started` (not `service_healthy`) is consistent with how gateway
depends on wa/campaign/inbox in the existing compose — gateway tolerates
backends still warming up because gRPC reconnect handles it. We do not
escalate to `service_healthy` until chunk 4 introduces real probes
everywhere.

### 2.3 Healthcheck contract

```yaml
healthcheck:
  test: ["CMD-SHELL", "wget --spider -q http://localhost:9092/readyz 2>&1 || nc -z localhost 8082"]
  interval: 10s
  timeout: 5s
  retries: 5
  start_period: 15s
```

The `|| nc -z` fallback exists because chunk 4 has not yet aligned every
probe. Once `/readyz` reliably returns 200 from chunk-4 work, the fallback
is removed.

### 2.4 Migration contract

Existing migrate init container's inline shell loop:

```yaml
command: |
  -c '
  for svc in gateway wa campaign inbox contacts proxy notify; do
    ...
  done
  '
```

Becomes:

```yaml
command: |
  -c '
  for svc in gateway wa mbs campaign inbox contacts proxy notify; do
    ...
  done
  '
```

`mbs` inserted between `wa` and `campaign` to match the alphabetised order
in `Makefile::migrate`. Order is irrelevant for correctness (each service
has its own `schema_migrations_<svc>` table) but matters for log
diffability.

### 2.5 DEK file contract

**Format:** raw 32 bytes (256 bits) of cryptographically random data. No
hex encoding, no PEM, no newline. Matches `pkg/crypto`'s file-mode loader.

**Permissions:** `chmod 400` on the host file. Compose mounts read-only.

**Generation:**

```sh
#!/usr/bin/env bash
# scripts/dek-generate.sh — generate a 32-byte raw DEK at $1.
set -euo pipefail
target="${1:?usage: dek-generate.sh <path>}"
mkdir -p "$(dirname "$target")"
openssl rand -out "$target" 32
chmod 400 "$target"
echo "Wrote 32-byte DEK to $target (0400)."
```

**Dev path:** `deploy/secrets/dev/mbs-dek.bin`. Gitignored. Generated by
each developer on first checkout.

**Example placeholder:** `deploy/secrets/dev/mbs-dek.bin.example` is 32
bytes of NULs, committed, used by README as a "this is what shape it
should be" reference. The compose service refuses to use this file —
the volume mount points at the real `.bin`, not the `.example`.

**Mount path in container:** `/run/secrets/mbs_dek`. We use compose
`secrets:` block (not bind mount) so the prod migration in chunk 3 is a
zero-cost path swap:

```yaml
secrets:
  mbs_dek:
    file: ./deploy/secrets/dev/mbs-dek.bin

services:
  mbs:
    secrets:
      - source: mbs_dek
        target: mbs_dek
        mode: 0400
```

In prod (chunk 3) the secret source flips to either an external Docker
secret or a different file path; the service block stays identical.

### 2.6 Port contract

| Port | Purpose | Exposed to host? |
|---|---|---|
| `8082` | gRPC | yes (`8082:8082`) — eases dev inspection with `grpcurl` |
| `9092` | diag HTTP (`/livez`, `/readyz`, `/metrics`, future `/debug/pprof`) | yes (`9092:9092`) |

Both ports remain reachable inside `hermes-net` by container name without
exposing.

### 2.7 Network contract

Service joins `hermes-net` bridge network (same as every other backend).

---

## 3. Implementation steps

1. Write `scripts/dek-generate.sh` and `chmod +x` it.
2. Create `deploy/secrets/dev/.gitignore` (`*.bin` + `!*.bin.example`).
3. Create `deploy/secrets/dev/mbs-dek.bin.example` (32 zero bytes).
4. Create `deploy/secrets/README.md` (explains the file layout, generation,
   what's committed vs ignored, prod vs dev paths).
5. Generate the real dev DEK locally:
   `./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin`
   (not committed; left on each dev's machine).
6. Patch `docker-compose.dev.yml`:
   a. Add top-level `secrets:` block declaring `mbs_dek`.
   b. Insert `mbs:` service definition between `inbox:` and the `gateway:`
      block (so it boots before gateway and is visible when reading
      top-to-bottom alongside other backends).
   c. Add `mbs: service_started` to `gateway.depends_on`.
   d. Add `MBS_ADDR: mbs:8082` to gateway env (it's currently absent —
      gateway falls back to its `localhost:8082` default, which inside the
      compose container would loopback the gateway and fail).
   e. ~~Replace `***` placeholder PG passwords with `hermes_dev`~~ —
      **not needed**. Discovered during build: the on-disk file already
      contains `hermes_dev` correctly; the Hermes terminal tool's output
      scrubber rewrites it as `***`. See R7 in master plan.
   f. Update the migrate command's `for svc in ...` list to include `mbs`.
7. Write `docs/runbooks/compose-deploy.md` with the dev section only
   (chunks 3+5 extend it for prod).
8. Build + boot end-to-end. Verify acceptance criteria.
9. Write hostile audit. Resolve all P0/P1.
10. Commit.

---

## 4. Files inventory (anticipated diff shape)

```
NEW:
  scripts/dek-generate.sh                                      [+15 LOC]
  deploy/secrets/README.md                                     [+~60 LOC]
  deploy/secrets/dev/.gitignore                                [+2 LOC]
  deploy/secrets/dev/mbs-dek.bin.example                       [32 bytes of NUL]
  docs/runbooks/compose-deploy.md                              [+~150 LOC]
  .hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md  [already written]
  .hermes/plans/2026-05-29_stage-f-chunk1-mbs-in-dev-compose.md [this file]
  docs/research/mbs-f-chunk1-hostile-audit-2026-05-29.md       [+~200 LOC, post-build]

MODIFIED:
  docker-compose.dev.yml                                       [+~55 LOC, ~3 LOC]
```

Net: no Go code touched, no proto touched, no migrations touched, no
frontend touched. Pure ops surface.

---

## 5. Acceptance gates (re-stated from master plan, with verification commands)

| # | Gate | Command |
|---|---|---|
| 1 | Clean clone boots | `./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin && docker compose -f docker-compose.dev.yml up --build -d` |
| 2 | `mbs` reports healthy | `docker compose -f docker-compose.dev.yml ps mbs` (status `Up (healthy)`) |
| 3 | Diag endpoints alive | `curl -fsS http://localhost:9092/readyz && curl -fsS http://localhost:9092/healthz` |
| 4 | mbs migrations applied | `docker compose -f docker-compose.dev.yml exec postgres psql -U hermes -d hermes -c "SELECT version FROM schema_migrations_mbs;"` (returns 4) |
| 5 | gateway dials mbs | `docker compose -f docker-compose.dev.yml logs gateway 2>&1 | grep -i mbs` (no `connection refused` lines) |
| 6 | Clean shutdown | `docker compose -f docker-compose.dev.yml down --remove-orphans` exits 0 |
| 7 | Test suite still green | `go test -race -count=1 ./...` |
| 8 | Web build still green | `cd web && npm run build` |
| 9 | No DEK in git | `git diff --cached -- deploy/secrets/dev/mbs-dek.bin` returns empty (file is gitignored) |
| 10 | DEK example file is 64 hex zeros + newline | `wc -c deploy/secrets/dev/mbs-dek.bin.example` returns `65`; `head -c 64 deploy/secrets/dev/mbs-dek.bin.example` is all `0` chars |

---

## 6. Hostile-audit checklist (to be filled at chunk close)

Categories from prior stage audits:

- **Secrets leakage:** any DEK bytes in git, in `docker inspect`, in logs?
- **Boot order:** does mbs come up before gateway tries to dial it? Does
  migrate finish before mbs reads its tables?
- **Restart behaviour:** what happens if mbs crashes mid-boot (DEK file
  missing / corrupt / wrong-size)? Does the rest of the stack stay up?
- **Networking:** can a malicious container on `hermes-net` reach `mbs`
  gRPC? (Dev tolerance: yes. Prod tolerance: yes inside the network,
  blocked at the host firewall — out of chunk-1 scope.)
- **Migrations:** does the migrate init container correctly use the
  per-service `schema_migrations_<svc>` table, or does it collide?
- **Env var validation:** does mbs fail-closed at boot if `DATABASE_URL`
  is empty? (Verified at `cmd/mbs/main.go`, config-loader returns ""
  and main.go's pool ping fails the boot.)
- **Pod_id collisions:** if a dev runs two `docker compose up` from two
  clones, does the static `hermes-mbs-dev` POD_ID cause migration-003
  pod-claim chaos? (Mitigation: document in runbook that dev compose is
  single-instance; if running parallel clones, override `POD_ID` per
  shell.)
- **DEK permission divergence:** does the `0400` host file survive the
  docker secret pipeline? (Compose docs say yes; verify with `docker
  compose exec mbs ls -l /run/secrets/mbs_dek`.)
- **Resource limits:** none set in dev compose; document risk of
  mautrix-meta OOM-ing the dev box if 5+ concurrent bridge sessions
  spawn.

---

## 7. Out of scope reminders

- Multi-arch image builds (`docker buildx`).
- BIZAPP_CLIENT_TOKEN env (not consumed by config.go; bridge driver
  reads it elsewhere — audit during chunk 2 / 3).
- Web SPA build container in prod compose (chunk 3).
- Image versioning labels (chunk 2).
- Restart policies (chunk 3).
- Reverse proxy (chunk 5).

---

## 8. Rollback

If chunk 1 destabilises the dev loop:

```sh
git revert <commit-sha>
docker compose -f docker-compose.dev.yml down --remove-orphans
docker compose -f docker-compose.dev.yml up --build -d
```

No data migrations are run that touch existing service schemas; the
`migrations/mbs/` migrations only operate on `mbs_*` tables which the
other services do not touch. Reverting is safe at any point.
