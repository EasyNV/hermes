# Hermes mbs Bootstrap Runbook

**Last updated:** 2026-05-30 (Stage F chunk 5)
**Audience:** Operators bringing up a brand-new Hermes deployment for the first time.
**Scope:** Secret provisioning → stack boot → tenant + admin creation → first JWT login → first MBS bridge login.

Read this **top to bottom on your first deploy**. After that you only revisit individual sections (secret rotation, new tenant onboarding, etc.).

---

## 0. Prerequisites

- VPS provisioned with Docker ≥20.10 and Docker Compose v2 (or v1 ≥1.29).
- Domain (`HERMES_DOMAIN`) pointing at the VPS public IP. Required for ACME if you front the stack with Caddy/nginx per `compose-deploy.md` §6.
- Stack repo cloned: `git clone <repo> hermes && cd hermes`.
- This runbook assumes you're running from the repo root.

---

## 1. Generate the Data Encryption Key (DEK)

The mbs service encrypts BizApp session cookies at rest. The DEK is a 32-byte key stored at `deploy/secrets/prod/mbs-dek.bin`.

```bash
# Chunk-3 helper script — 32 random bytes, mode 0400
./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin
```

Verify:

```bash
ls -l deploy/secrets/prod/mbs-dek.bin
# -r-------- 1 you you 32 May 30 09:00 deploy/secrets/prod/mbs-dek.bin
```

**This is the most operationally sensitive secret in the stack.** Losing the DEK means every `mbs_cookie_blobs` row is unreadable (per `backup-restore.md` §3). Back it up the moment you generate it:

```bash
# Copy to a safe location IMMEDIATELY — NOT just on this disk
tar -C deploy/secrets/prod -czf ~/hermes-dek-backup-$(date +%Y%m%d).tgz mbs-dek.bin
# Then: encrypt to a hardware key, mail it to your password manager, etc.
```

---

## 2. Generate the JWT signing key

Gateway issues JWTs signed with `deploy/secrets/prod/jwt-signing-key`. Rotating this invalidates all currently-issued sessions.

```bash
openssl rand -base64 32 > deploy/secrets/prod/jwt-signing-key
chmod 0400 deploy/secrets/prod/jwt-signing-key
```

Verify:

```bash
ls -l deploy/secrets/prod/jwt-signing-key
# -r-------- 1 you you 45 May 30 09:00 deploy/secrets/prod/jwt-signing-key
```

The 45-byte file is the base64-encoded 32-byte key + trailing newline. Don't strip the newline; `pkg/config.LoadSecret` handles it.

---

## 3. Generate the Postgres password

```bash
printf '%s' "$(openssl rand -base64 24)" > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/postgres-password
```

Use `printf '%s'` (not `echo`) so there's no trailing newline — `pg_isready` and the chunk-3 entrypoint substitute this verbatim into `DATABASE_URL`.

Verify:

```bash
wc -c deploy/secrets/prod/postgres-password
# 32 deploy/secrets/prod/postgres-password
```

---

## 4. Fill in `.env.prod`

```bash
cp .env.prod.example .env.prod
```

Edit `.env.prod`:

```dotenv
# Database — the postgres-password file is read by the compose entrypoint
# and interpolated into DATABASE_URL. You only set the URL template here.
DATABASE_URL=postgres://hermes:${POSTGRES_PASSWORD}@postgres:5432/hermes?sslmode=disable

# Domain + ACME (required if you front the stack with Caddy per
# deploy/caddy/Caddyfile.example).
HERMES_DOMAIN=hermes.example.com
HERMES_ACME_EMAIL=ops@example.com

# Pod IDs — change if you ever scale wa or mbs horizontally
POD_ID_WA=hermes-wa-prod-0
POD_ID_MBS=hermes-mbs-prod-0

# Resource knobs (chunk-3 master plan §7)
MBS_BRIDGE_MAX_CONCURRENT=10
MBS_STREAM_REPLICAS=1
LOG_LEVEL=info

# Image version — pin to a specific tag in production, NOT `latest`
HERMES_VERSION=v0.5.0
```

Don't commit `.env.prod`. The `.gitignore` from chunk 3 already excludes it.

---

## 5. Boot the stack

```bash
make deploy-prod-up
```

The pre-flight checks (chunk 3) confirm every secret file exists; if anything's missing you get a fail-closed error message pointing at which step in this runbook to revisit.

Watch the boot:

```bash
make deploy-prod-logs
```

Expected sequence:

1. `postgres` healthy (~10s).
2. `nats`, `redis` healthy (~5s each).
3. `migrate` runs all migrations (gateway, wa, campaign, inbox, contacts, proxy, notify, mbs), then exits 0.
4. Backend services start in parallel, each binding its METRICS_PORT (chunk 4) and reporting `metrics_port=NNNN` in its startup log.
5. Each backend's `/readyz` flips from 503 → 200 once DB + NATS are connected.
6. `gateway` starts AFTER all 7 backends report healthy (chunk-4 `depends_on: service_healthy`).
7. `web` starts after gateway.

Total cold-boot time: ~90s on a 4-vCPU VPS. Verify:

```bash
make deploy-prod-ps
# All 12 services should show (healthy).
```

If anything stays `(health: starting)` for more than 2 minutes, check the container's logs:

```bash
docker logs hermes-<service>-1 2>&1 | tail -50
```

Common gotchas:
- **mbs stuck starting** → DEK file not mounted. Check `docker inspect hermes-mbs-1 | grep -A3 Mounts`.
- **gateway stuck starting** → one backend is unhealthy. `make deploy-prod-ps` shows which.
- **migrate exited non-zero** → DB migration conflict. Check `docker logs hermes-migrate-1`.

---

## 6. Create your first tenant + admin user

The migration at `migrations/gateway/000003_seed_superadmin.up.sql` seeds a default superadmin with email `admin@hermes.local` / password `admin123`. **For production, change the password immediately or create a fresh superadmin and delete the seed.**

### 6.1 Option A — Use the seeded admin then rotate

```bash
# Log in as the seeded superadmin (use Caddy-fronted URL if behind reverse proxy)
curl -X POST http://localhost:8081/api/v1/auth/login \
    -H 'Content-Type: application/json' \
    -d '{"email":"admin@hermes.local","password":"admin123"}'
# Response: {"access_token":"eyJhbGc...","refresh_token":"..."}
```

Then via the web UI: log in as `admin@hermes.local`, navigate to **Users → Change Password**, and rotate to a strong password before doing anything else.

### 6.2 Option B — Replace the seeded admin

```bash
# Generate a bcrypt hash for your new password — cost 12 to match
# the chunk-1 gateway default. Adjust if your gateway is configured
# for a different cost.
docker run --rm -it python:3.12-alpine sh -c \
    'pip install -q bcrypt && python3 -c "import bcrypt; print(bcrypt.hashpw(b\"<your-new-password>\", bcrypt.gensalt(rounds=12)).decode())"'
# Copy the $2b$12$... output.
```

Replace the seeded user:

```sql
-- Open a psql session
-- docker compose -f docker-compose.prod.yml --env-file .env.prod exec postgres psql -U hermes hermes

BEGIN;

-- Insert a new tenant with a real name
INSERT INTO tenants (id, name, settings_json, max_numbers_per_proxy)
VALUES (gen_random_uuid(), 'Acme Corp', '{}', 10)
RETURNING id;
-- Note the returned UUID; you'll use it as <ACME_TENANT_ID> below.

-- Insert a workspace under that tenant
INSERT INTO workspaces (id, tenant_id, name, settings_json, daily_cap)
VALUES (gen_random_uuid(), '<ACME_TENANT_ID>', 'Production', '{}', 500)
RETURNING id;
-- Note the returned UUID; <ACME_WORKSPACE_ID>.

-- Insert your superadmin
INSERT INTO users (id, tenant_id, email, password_hash, role)
VALUES (
  gen_random_uuid(),
  '<ACME_TENANT_ID>',
  'you@acme.com',
  '$2b$12$<bcrypt-hash-from-above>',
  'superadmin'
)
RETURNING id;
-- Note <ACME_USER_ID>.

-- Bind your user to the workspace as workspace_admin
INSERT INTO workspace_members (user_id, workspace_id, role)
VALUES ('<ACME_USER_ID>', '<ACME_WORKSPACE_ID>', 'workspace_admin');

-- Delete the seeded admin
DELETE FROM users WHERE email = 'admin@hermes.local';

COMMIT;
```

Test the login:

```bash
curl -X POST http://localhost:8081/api/v1/auth/login \
    -H 'Content-Type: application/json' \
    -d '{"email":"you@acme.com","password":"<your-new-password>"}'
```

If the response is a JSON object with `access_token`, the login chain is working end-to-end.

---

## 7. First browser session

Behind a reverse proxy (Caddy or nginx per `compose-deploy.md` §6):

1. Browse to `https://<HERMES_DOMAIN>`.
2. Caddy/nginx terminates TLS, proxies `/api/v1/auth/login` to gateway:8081, returns the SPA static files for `/`.
3. Log in with `you@acme.com` / `<your password>`.
4. You land on the Dashboard. No WhatsApp numbers yet — that's the next step.

Without a reverse proxy (dev/staging):

```bash
# Vite dev server (dev compose) or in-compose web container (prod)
open http://localhost:8081/  # or http://localhost:5173/ for Vite hot reload
```

---

## 8. First MBS bridge login

This is the path that gets one **real Meta BizApp** session bridged into the stack.

### 8.1 Prerequisites

- A Meta Business Suite account with WhatsApp Business messaging enabled.
- The cookies from that browser session (collected via the cookie-extractor extension or manually from DevTools → Application → Cookies → `business.facebook.com`).
- Cookies must include at least: `c_user`, `xs`, `datr`, `sb`, `fr`. (See `re/mbs/` notes for the full set.)

### 8.2 Procedure

1. Browse to `https://<HERMES_DOMAIN>/mbs-sessions` (the MBS Sessions page from Stage E2 chunk 5).
2. Click **"New Bridge Login"**.
3. A dialog opens with a WebSocket-driven state machine. Paste the cookie blob (JSON-encoded) into the input.
4. The state transitions:
   - `IDLE` → `CONNECTING` (WS opens to gateway → mbs)
   - `CONNECTING` → `LOGGING_IN` (mbs hands cookies to mautrix-meta)
   - `LOGGING_IN` → `READY` (login succeeded; session row written to `mbs_sessions`, cookies encrypted to `mbs_cookie_blobs`)
5. The dialog closes; the new session appears in the MBS Sessions table.

### 8.3 Verify

```bash
# Inside the postgres container
docker compose -f docker-compose.prod.yml --env-file .env.prod exec postgres \
    psql -U hermes hermes -c \
    "SELECT id, tenant_id, status, created_at FROM mbs_sessions ORDER BY created_at DESC LIMIT 5;"
```

Expected: one row with `status = 'ready'`. Cookie blob is in `mbs_cookie_blobs` (encrypted; the row is opaque without the DEK).

Send a test message to the bridged WA Business number; it should arrive in the inbox UI within a few seconds (WebSocket-pushed via gateway → web).

---

## 9. Common follow-ups

| Task | Where |
|---|---|
| Add a second tenant | §6.2 with a new tenant_id |
| Add a `cs_agent` user to the workspace | Same SQL with `role='cs_agent'` + workspace_members row |
| Rotate the DEK | `docs/runbooks/secret-management.md` §3 |
| Rotate the JWT signing key | `docs/runbooks/secret-management.md` §4 (force-rotates all sessions) |
| Add a WhatsApp number (whatsmeow) | Out of scope for this runbook — that's the wa service's own bootstrap, not covered in Stage F |
| Back up everything you just did | `docs/runbooks/backup-restore.md` §1–§3 |

---

## 10. Troubleshooting

### "DEK file not found" at boot

`deploy/secrets/prod/mbs-dek.bin` doesn't exist or has wrong permissions. Re-run §1.

### "JWT signing key empty" at gateway boot

`deploy/secrets/prod/jwt-signing-key` is empty or unreadable. Re-run §2. If the file is 0 bytes you'll see `LoadSecret` falling back to the dev placeholder, which is a security regression — fix immediately.

### Login returns 401 even with the right password

bcrypt cost mismatch. Generate the hash with the same cost as the gateway expects (default 12). If you used cost 10 or 14, regenerate.

### MBS session stays in `CONNECTING` forever

mautrix-meta failed to authenticate with the cookie blob. Most common cause: cookies expired (`xs` and `c_user` rotate frequently). Re-extract from a fresh browser session.

### Caddy auto-TLS fails with "rate limit exceeded"

You hit Let's Encrypt's 5 certs / domain / week limit during smoke testing. Switch to staging:
```
# In the Caddyfile global block:
acme_ca https://acme-staging-v02.api.letsencrypt.org/directory
```
Re-deploy, verify the flow works (browsers will mark the staging cert untrusted; that's expected). Switch back to production once you're sure.

---

## Appendix — Quick reference

| Step | Command |
|---|---|
| Generate DEK | `./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin` |
| Generate JWT key | `openssl rand -base64 32 > deploy/secrets/prod/jwt-signing-key && chmod 0400 ...` |
| Generate PG password | `printf '%s' "$(openssl rand -base64 24)" > deploy/secrets/prod/postgres-password && chmod 0400 ...` |
| Boot stack | `make deploy-prod-up` |
| Watch boot | `make deploy-prod-logs` |
| Verify health | `make deploy-prod-ps` |
| Login test | `curl -X POST http://localhost:8081/api/v1/auth/login -H 'Content-Type: application/json' -d '{"email":"you@acme.com","password": "***"}'` |
| Backup everything | See `docs/runbooks/backup-restore.md` §3.2 |
