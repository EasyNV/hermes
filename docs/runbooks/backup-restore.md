# Hermes Backup & Restore Runbook

**Last updated:** 2026-05-30 (Stage F chunk 5)
**Audience:** Operators running `docker-compose.prod.yml` on a single-host VPS.
**Scope:** Manual `pg_dump`/`pg_restore` + NATS stream snapshot + cookie-blob DEK considerations + chaos-test verification.

This runbook covers **manual** procedures. Cron-driven automation, WAL archiving, PITR, and cross-region replication are Stage G or later.

---

## 0. Prerequisites

- Hermes stack running (`make deploy-prod-ps` shows all 12 services healthy).
- `backups/` directory exists at the repo root with write access from the operator user:
  ```bash
  mkdir -p backups && chmod 0700 backups
  ```
- Disk space: budget ~3× the live DB size for a single full dump (pg_dump is compressed but you want headroom).

---

## 1. Postgres backup

The dump captures **every Hermes table**: tenants, users, conversations, messages, contacts, campaigns, proxies, mbs sessions, mbs cookie blobs (encrypted), and the whatsmeow `whatsmeow_*` tables.

### 1.1 Take a dump

```bash
docker-compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres \
    pg_dump -U hermes -Fc --no-owner --no-acl hermes \
    > backups/hermes-$(date +%Y%m%d-%H%M%S).pgdump
```

Flags explained:
- `-Fc` — custom format. Smallest on disk, restorable with `pg_restore`. (Plain SQL via `-Fp` is human-readable but ~3× larger.)
- `--no-owner --no-acl` — strips owner/grant statements so the dump restores cleanly on a different cluster (different role names).
- `-T` (on docker exec) — disable TTY allocation, required so the stdout stream isn't corrupted by a pseudo-TTY.

Verify the dump:

```bash
ls -lh backups/hermes-*.pgdump
docker run --rm -v $(pwd)/backups:/backups postgres:17-alpine \
    pg_restore --list /backups/hermes-20260530-090000.pgdump | head -40
```

The `--list` output shows the TOC — tables, indexes, FKs, and the `mbs_cookie_blobs` row count. If you see actual table names, the dump is good.

### 1.2 Restore from a dump

⚠️ **Destructive.** This wipes whatever's currently in the `hermes` database.

```bash
docker-compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres \
    pg_restore -U hermes --clean --if-exists -d hermes \
    < backups/hermes-20260530-090000.pgdump
```

Flags:
- `--clean --if-exists` — drops then recreates each object. Safe to run against a non-empty database.
- The stack must be **stopped or at least drained** during restore. Run `make deploy-prod-down`, restore, `make deploy-prod-up`.

### 1.3 Restoring to a new host

When migrating to a new VPS or rebuilding from scratch:

1. Bring up the stack on the new host with **empty** DB (`make deploy-prod-up`; migrations create tables).
2. Stop the stack (`make deploy-prod-down`) but leave the postgres container running:
   ```bash
   docker compose -f docker-compose.prod.yml --env-file .env.prod up -d postgres
   ```
3. Run the `pg_restore` command from §1.2.
4. **Critical:** copy the original `deploy/secrets/prod/mbs-dek.bin` to the new host BEFORE bringing mbs back up. See §3 below.
5. `make deploy-prod-up` — full stack with restored data.

---

## 2. NATS stream backup

The streams `HERMES_WA`, `HERMES_CAMPAIGN`, `HERMES_INBOX`, `HERMES_CONTACTS`, `HERMES_NOTIFY`, `HERMES_MBS` hold transient event traffic. JetStream snapshots include the messages currently in the stream + the consumer offsets.

In most operational scenarios you do NOT need NATS backups — replaying a 7-day-old `HERMES_WA` snapshot would re-deliver every wa event from the snapshot window, which is usually wrong. Take these snapshots for:

- **DR rehearsal** — capture pre-test state so you can roll back consumer offsets.
- **Forensics** — capture state at the moment something went sideways.

### 2.1 Take a stream snapshot

```bash
docker run --rm \
    --network hermes_hermes-net \
    -v $(pwd)/backups:/backups \
    natsio/nats-box:latest \
    nats stream backup HERMES_MBS /backups/HERMES_MBS-$(date +%Y%m%d-%H%M%S) \
    --server nats://nats:4222
```

Repeat for every stream you care about. Output is a directory tree containing `meta.json`, `stream.json`, and the raw message blocks.

### 2.2 Restore a stream snapshot

⚠️ **Destructive.** This replaces the stream's current state, including consumer offsets.

```bash
docker run --rm \
    --network hermes_hermes-net \
    -v $(pwd)/backups:/backups \
    natsio/nats-box:latest \
    nats stream restore HERMES_MBS /backups/HERMES_MBS-20260530-090000 \
    --server nats://nats:4222
```

If the stream already exists, `restore` will refuse. Delete it first via `nats stream rm HERMES_MBS` if you really want to overwrite.

### 2.3 What's NOT covered

- Per-message edits (NATS isn't a database; treat snapshots as opaque).
- Cross-cluster mirroring (Stage G+).
- Snapshot retention policy — operator's call. A common scheme: daily snapshots, keep 7.

---

## 3. mbs cookie blob backup — DEK separation

The `mbs_cookie_blobs` table holds Meta BizApp session cookies encrypted with the mbs DEK (`deploy/secrets/prod/mbs-dek.bin`). The Postgres dump from §1 captures the encrypted blobs, but it does NOT capture the DEK.

### 3.1 What this means

- A `pg_restore` on a new host **without the original DEK** results in `mbs_cookie_blobs` rows that exist in the database but are **unreadable**. mbs will fail to bridge those sessions; users will have to re-login through the bridge flow per `docs/runbooks/mbs-bootstrap.md`.
- The fix is simple but easy to forget: **always copy `deploy/secrets/prod/mbs-dek.bin` alongside any Postgres backup that includes the `mbs_cookie_blobs` table**.

### 3.2 Backup checklist (DR-grade)

```bash
# 1. Postgres dump (per §1.1)
docker-compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres \
    pg_dump -U hermes -Fc --no-owner --no-acl hermes \
    > backups/hermes-$(date +%Y%m%d-%H%M%S).pgdump

# 2. Copy DEK alongside the dump (tar them together for atomicity)
TS=$(date +%Y%m%d-%H%M%S)
tar -C deploy/secrets/prod -czf backups/hermes-dek-$TS.tgz mbs-dek.bin
sha256sum backups/hermes-dek-$TS.tgz > backups/hermes-dek-$TS.tgz.sha256

# 3. Optional: bundle JWT signing key too (so /api/auth/refresh keeps issuing)
# Note: rotating JWT keys invalidates all current sessions — sometimes that's
# what you want. Skip this step if you'd rather force-rotate on restore.
tar -C deploy/secrets/prod -czf backups/hermes-jwt-$TS.tgz jwt-signing-key
```

Store the DEK tarball **separately** from the Postgres dump. The threat model: if a single attacker grabs the Postgres dump, they should NOT also grab the DEK. Off-host encrypted backup destination (e.g. age-encrypted to a hardware key) is the Stage G+ answer.

### 3.3 Restore checklist (DR-grade)

```bash
# 1. On the new host, bring up postgres only
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d postgres

# 2. Restore the DEK before anything else touches the secrets dir
TS=20260530-090000
tar -C deploy/secrets/prod -xzf backups/hermes-dek-$TS.tgz
chmod 0400 deploy/secrets/prod/mbs-dek.bin

# 3. Run the migrations + pg_restore
make deploy-prod-up   # migrations create schema
make deploy-prod-down # but stop everything else
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d postgres
docker-compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres \
    pg_restore -U hermes --clean --if-exists -d hermes \
    < backups/hermes-$TS.pgdump

# 4. Bring the rest of the stack up
make deploy-prod-up
```

After §4, sanity check by reading one cookie blob through mbs (or via the gateway's mbs proxy). If decryption succeeds, the DEK pairing is correct.

---

## 4. Chaos test — auto-restart verification

This is the chunk-3 F7 deferred check. Run it once on a fresh Linux VPS to confirm `restart: unless-stopped` actually does what the compose policy claims. macOS (OrbStack / Docker Desktop) does **not** consistently honour this policy on external kills, which is why the chunk-3 audit deferred verification.

### 4.1 Procedure

With the stack up:

```bash
# Kill proxy hard — bypass any in-container signal handling
docker kill -s KILL hermes-proxy-1

# Wait for Docker to notice + restart
sleep 5

# Verify restart
docker ps --format "table {{.Names}}\t{{.Status}}" | grep proxy
```

**Expected output on real Linux Docker:**
```
hermes-proxy-1   Up 3 seconds (health: starting)
```

The "Up <N> seconds" means Docker restarted the container after the kill. Subsequent `docker ps` should transition `health: starting` → `(healthy)` within ~30s as the `/readyz` probe (chunk 4) clears.

**If the container stays gone:** the restart policy isn't being honoured. Check:
1. `docker inspect hermes-proxy-1 | grep -A2 RestartPolicy` — should show `"Name": "unless-stopped"`.
2. Docker daemon version (`docker version`) — needs ≥20.10.
3. Whether you ran `docker stop` before (which sets the "stopped" flag that `unless-stopped` respects). If so, just `docker start hermes-proxy-1`.

### 4.2 What this proves

Combined with chunk-4 `/readyz`, this gate verifies that an OOM-killed or panicked service gets restarted by Docker AND that the gateway `depends_on: service_healthy` graph correctly waits for `/readyz` before resuming traffic. Operators who skip this gate are trusting compose policy without evidence.

---

## 5. Disaster scenarios — operator playbook

### 5.1 Lost the host

- Provision a new VPS.
- `git clone` the repo at the same SHA the old host was running (`git log -1` on a healthy DB dump's metadata if you have it).
- Follow §3.3 (restore checklist).

### 5.2 Lost the DEK but kept the DB

- Postgres data is intact except `mbs_cookie_blobs` (unreadable).
- Generate a new DEK: `scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin`.
- TRUNCATE `mbs_cookie_blobs` (the rows are dead weight without the original DEK):
  ```sql
  docker compose exec postgres psql -U hermes hermes -c 'TRUNCATE mbs_cookie_blobs;'
  ```
- All MBS users must re-login through the bridge flow (see `docs/runbooks/mbs-bootstrap.md` §8).

### 5.3 DB corruption (single table)

- Stop the stack: `make deploy-prod-down`.
- Bring postgres back up alone: `docker compose -f docker-compose.prod.yml --env-file .env.prod up -d postgres`.
- Restore the single damaged table from a dump:
  ```bash
  docker run --rm -i -v $(pwd)/backups:/backups postgres:17-alpine \
      pg_restore -t <table_name> --clean --if-exists \
      -h hermes-postgres-1 -U hermes -d hermes \
      < /backups/hermes-20260530-090000.pgdump
  ```
- Bring the rest up: `make deploy-prod-up`.

### 5.4 Stream consumer stuck

This is operational, not a backup scenario. Document for completeness:

```bash
docker run --rm --network hermes_hermes-net natsio/nats-box:latest \
    nats consumer info HERMES_MBS hermes-mbs --server nats://nats:4222

# To reset the consumer offset (CAREFUL — re-delivers messages):
docker run --rm --network hermes_hermes-net natsio/nats-box:latest \
    nats consumer rm HERMES_MBS hermes-mbs --force --server nats://nats:4222
# Then restart mbs to recreate the consumer at the head of the stream.
```

---

## 6. What's not in this runbook

- **Automated backup scheduling** — cron / systemd timer wrapping §1.1 + §3.2. Stage G.
- **Off-host backup destinations** — S3, Backblaze, rsync.net. Stage G.
- **Encrypted backup at rest** — age, gpg, sops. Stage G.
- **WAL archiving / PITR** — barman, pgBackRest. Stage G+.
- **Cross-region replication** — logical replication slots, pglogical. Stage G+.
- **Monitoring backup freshness** — Prometheus alert on `time() - backup_last_completed > 26h`. Stage G+.

When you outgrow manual backups, the right move is usually **a single TODO file in the repo** capturing what you actually do, and graduating that to a Stage G runbook when it's stable.

---

## Appendix — Quick reference

| Action | Command |
|---|---|
| Take Postgres dump | `docker-compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres pg_dump -U hermes -Fc --no-owner --no-acl hermes > backups/hermes-$(date +%Y%m%d-%H%M%S).pgdump` |
| List dump contents | `docker run --rm -v $(pwd)/backups:/backups postgres:17-alpine pg_restore --list /backups/<dump>` |
| Restore Postgres | `docker-compose ... exec -T postgres pg_restore -U hermes --clean --if-exists -d hermes < backups/<dump>` |
| Snapshot NATS stream | `docker run --rm --network hermes_hermes-net -v $(pwd)/backups:/backups natsio/nats-box:latest nats stream backup HERMES_MBS /backups/HERMES_MBS-$(date +%Y%m%d-%H%M%S) --server nats://nats:4222` |
| Restore NATS stream | `... nats stream restore HERMES_MBS /backups/<dir> ...` |
| Backup DEK | `tar -C deploy/secrets/prod -czf backups/hermes-dek-$(date +%Y%m%d-%H%M%S).tgz mbs-dek.bin` |
| Verify auto-restart | `docker kill -s KILL hermes-proxy-1 && sleep 5 && docker ps \| grep proxy` |
