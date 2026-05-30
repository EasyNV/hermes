# Secret Management — Hermès Production

**Status:** Active runbook (Stage F chunk 3)
**Audience:** Hermès operators running the prod compose stack.

This runbook covers every secret the prod stack consumes, where it lives,
how to generate and rotate it, and the failure modes operators should
expect.

---

## 0. Secrets inventory

| Secret | Type | Host path | Container path | Consumer | Owner |
|---|---|---|---|---|---|
| **DEK** | 32-byte hex (64 chars + \n) | `deploy/secrets/prod/mbs-dek.bin` | `/run/secrets/mbs_dek` | `mbs` | Operator |
| **JWT signing key** | 32-byte hex (64 chars + \n) | `deploy/secrets/prod/jwt-signing-key` | `/run/secrets/jwt_signing_key` | `gateway` | Operator |
| **PG password** | high-entropy ASCII (no `\n`, no shell metachars) | `deploy/secrets/prod/postgres-password` | `/run/secrets/postgres_password` | `postgres`, `migrate` | Operator |
| **PG `DATABASE_URL`** | full Postgres URL with password inline | `.env.prod` (`DATABASE_URL=…`) | env var | every Go service | Operator |

The `DATABASE_URL` keeps the password inline (env-level) because the Go
services consume the connection string as a single field. The
`postgres_password` Docker secret feeds the `postgres:17-alpine` image via
`POSTGRES_PASSWORD_FILE` *and* the `migrate` init container reads it
directly. The two values must match — see §PG-PASSWORD §2.

---

## 1. DEK (Data Encryption Key)

The DEK encrypts every per-tenant secret in `cookie_blobs` (cookies, OTP
tokens, mautrix-meta session bytes). Loss = inability to decrypt
existing tenant data. Compromise = full takeover of every tenant.
**Single highest-value secret in the system.**

### 1.1 Generate

```sh
./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin
```

This writes 64 hex chars + a single trailing `\n` (65 bytes total) and
sets the file mode to `0400`. The hex contract matches
`pkg/crypto.LoadDEKFromFile`. The script refuses to overwrite an
existing file — rotation goes through §1.3 below.

### 1.2 Consumer

`internal/mbs/config/config.go` reads `HERMES_MBS_DEK_FILE` →
`/run/secrets/mbs_dek`. The compose secret mounts at uid/gid 65532 mode
0400 so the non-root `hermes` user in the chunk-2 Dockerfile can read
it. `cmd/mbs/main.go::loadDEK` fails closed at boot if the file is
missing or malformed.

### 1.3 Rotation (annual cadence recommended; quarterly for high-risk)

1. Generate a new DEK *next to* the existing one:
   ```sh
   ./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin.new
   ```
2. **WARNING:** the system does NOT currently support online dual-DEK
   re-encryption (this is a follow-up story — see open question §6.1).
   Until that ships, rotation requires a **maintenance window**:
   1. Stop the mbs container: `docker-compose -f docker-compose.prod.yml stop mbs`
   2. Export every encrypted blob to plaintext with the current DEK
      (operator tooling TBD — see §6.1).
   3. Replace `deploy/secrets/prod/mbs-dek.bin` with the new file.
   4. Re-encrypt every blob with the new DEK.
   5. Start mbs: `docker-compose -f docker-compose.prod.yml up -d mbs`
3. Securely destroy the old DEK on the host:
   ```sh
   shred -u deploy/secrets/prod/mbs-dek.bin.old
   ```

### 1.4 Compromise response

If the DEK leaks (e.g. host filesystem snapshot ends up in unintended
hands):

1. Rotate every per-tenant secret upstream (force every operator to
   re-bridge — invalidate every cookie blob).
2. Rotate the DEK per §1.3.
3. Audit every container that mounted `/run/secrets/mbs_dek` since the
   suspected leak window.

---

## 2. JWT signing key

The JWT signing key signs every gateway-issued JWT (operator login,
service-to-service auth tokens). Compromise = ability to mint valid
tokens for any tenant.

### 2.1 Generate

```sh
./scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key
```

Same hex format and `0400` mode as the DEK. (Both are HS256-shaped
32-byte secrets, so the generator script doubles for both.)

### 2.2 Consumer

`internal/gateway/config/config.go::Load` resolves `JWT_SECRET` then
`JWT_SECRET_FILE` (the latter wins when the former is empty). The prod
compose leaves `JWT_SECRET` unset and points `JWT_SECRET_FILE` at
`/run/secrets/jwt_signing_key`. The compose secret mounts at
uid/gid 65532 mode 0400.

`pkg/config.LoadSecret`:

- Reads the env value if non-empty.
- Otherwise reads the file path from the `_FILE` env var.
- Strips exactly one trailing `\n`.
- Returns empty on every other path; gateway treats empty as the dev
  default (in dev) or refuses-to-start (operators should set
  `JWT_SECRET_FILE` correctly in prod).

### 2.3 Rotation (quarterly recommended; immediately on compromise)

JWT rotation invalidates every existing operator session. Plan a brief
maintenance window during the rotation.

1. Generate new key alongside:
   ```sh
   ./scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key.new
   ```
2. Replace and restart gateway:
   ```sh
   mv deploy/secrets/prod/jwt-signing-key deploy/secrets/prod/jwt-signing-key.old
   mv deploy/secrets/prod/jwt-signing-key.new deploy/secrets/prod/jwt-signing-key
   docker-compose -f docker-compose.prod.yml --env-file .env.prod restart gateway
   ```
3. Every operator re-logs in.
4. Destroy the old key:
   ```sh
   shred -u deploy/secrets/prod/jwt-signing-key.old
   ```

A future stage will add a *next-key* (dual signature window) flow so
rotation doesn't kick everyone out simultaneously.

---

## 3. Postgres password (PG-PASSWORD)

The Postgres superuser password. Used by both `postgres:17-alpine`
(consumes `POSTGRES_PASSWORD_FILE`) and the `migrate` init container
(reads the file directly via `cat`).

### 3.1 Generate

```sh
openssl rand -base64 32 | tr -d '=+/' | head -c 32 > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/postgres-password
```

Constraints:

- ASCII printable, no `\n`, no `:`, no `@`, no `/`, no whitespace.
- Length ≥ 24 chars.
- `postgres-password` file: NO trailing newline.

### 3.2 Consumer

Two places read this:

1. **`postgres:17-alpine` boot.** Uses `POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password` and is fine with any byte sequence.
2. **`migrate` init container.** Reads the file via `cat
   /run/secrets/postgres_password` and substitutes into the connection
   string.
3. **Every Go service.** Reads `DATABASE_URL` from `.env.prod`, which
   contains the password inline.

**The password value in `deploy/secrets/prod/postgres-password` and the
password segment of `DATABASE_URL` in `.env.prod` MUST be identical.**
This is the day-1 trip-hazard. The runbook below codifies the
two-touchpoint workflow.

### 3.3 Initial provisioning workflow

```sh
# Generate once, store both places
PASSWORD=$(openssl rand -base64 32 | tr -d '=+/' | head -c 32)
printf '%s' "$PASSWORD" > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/postgres-password

# Edit .env.prod and set
# DATABASE_URL=postgres://hermes:$PASSWORD@postgres:5432/hermes?sslmode=disable
# (substitute $PASSWORD by hand or via envsubst)
```

### 3.4 Rotation

PG password rotation is the most disruptive of the four — every
service's open connection breaks and reconnects.

1. Connect to Postgres as superuser, set new password:
   ```sh
   docker-compose -f docker-compose.prod.yml exec postgres \
     psql -U hermes -c "ALTER USER hermes WITH PASSWORD '$NEW_PASSWORD';"
   ```
2. Update the host file:
   ```sh
   printf '%s' "$NEW_PASSWORD" > deploy/secrets/prod/postgres-password
   ```
3. Update `.env.prod` (`DATABASE_URL=…`).
4. Restart every Go service:
   ```sh
   docker-compose -f docker-compose.prod.yml --env-file .env.prod restart \
     gateway wa mbs campaign inbox contacts proxy notify
   ```
   (Postgres itself does NOT need a restart; the new password takes
   effect on next auth.)

### 3.5 Compromise response

If the password leaks:

1. Rotate per §3.4 immediately.
2. Review every `pg_stat_activity` snapshot from the suspected leak
   window for unfamiliar `client_addr`s.

---

## 4. Permission drift

Periodically (monthly or after any host file restore):

```sh
ls -l deploy/secrets/prod/
# Every file should be 0400 (owner read only).
chmod 0400 deploy/secrets/prod/*
```

`docker compose -f docker-compose.prod.yml up -d` then re-mounts the
secrets with the per-service `uid/gid/mode` declared in the compose
file. The host-side mode protects against accidental `cat` or `cp`
disclosure; the container-side `uid 65532 mode 0400` protects against
in-container compromise reading the file.

---

## 5. Backup and restore

The secret files MUST be backed up alongside the Postgres data dump —
a Postgres backup without the matching DEK is *worthless* (every
encrypted blob is unrecoverable).

```sh
# Backup
tar czf hermes-secrets-$(date -u +%Y%m%dT%H%M%SZ).tar.gz \
  deploy/secrets/prod/*.bin \
  deploy/secrets/prod/jwt-signing-key \
  deploy/secrets/prod/postgres-password
chmod 0400 hermes-secrets-*.tar.gz
# Move off-host immediately — encrypted offsite storage.
```

Restore: reverse the tar. Permissions should auto-restore from the
archive; verify with `ls -l deploy/secrets/prod/`.

---

## 6. Open questions / future work

### 6.1 Online DEK rotation

Currently rotation requires a maintenance window. The follow-up story
is:

1. Add a `dek_id` field to `cookie_blobs` rows.
2. mbs reads N DEKs from `/run/secrets/mbs_dek_<id>` instead of one.
3. Decrypt with the matching `dek_id`; re-encrypt opportunistically
   on next write under the newest `dek_id`.
4. Operator schedules a sweep job to re-encrypt cold rows.

Tracking: not yet ticketed. Stage F doesn't ship this.

### 6.2 JWT next-key rotation

Add a `JWT_SECRET_NEXT_FILE` env that signs new tokens while
`JWT_SECRET_FILE` is still accepted for verification. After all old
tokens expire, promote next to current and clear next. Stage F doesn't
ship this.

### 6.3 Managed secret store

Vault / AWS Secrets Manager / GCP Secret Manager integration is a
stage in itself. Stage F intentionally stops at file-based Docker
secrets — minimum viable production for single-VPS deploy.
