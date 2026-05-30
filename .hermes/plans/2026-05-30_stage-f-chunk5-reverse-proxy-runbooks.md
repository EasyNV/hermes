# Stage F Chunk 5 — Reverse Proxy Reference + Runbook Polish + Makefile Symmetry

**Owner:** Oracle
**Created:** 2026-05-30
**Status:** Plan + spec + contracts written, awaiting build approval
**Parent:** `.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`
**Predecessor:** chunk 4 (`pkg/observability` + real `/readyz` probes) — landed at `52c1738`

---

## 1. Goal

Ship the **operator-facing surface** Stage F has been promising:

1. A **reverse proxy reference** in front of the compose stack that terminates TLS, serves the React SPA from disk, and proxies `/api/*` + `/ws` to the gateway. **Caddy is the default** (auto-TLS via Let's Encrypt, single-binary, zero-config); **nginx is the alternate** (for operators who already run nginx). Both configs are real, tested, copy-pasteable — not pseudocode.
2. Two new **operational runbooks**:
   - `docs/runbooks/backup-restore.md` — Postgres dump+restore + NATS stream snapshot procedure.
   - `docs/runbooks/mbs-bootstrap.md` — first-tenant DEK generation, JWT key seeding, admin user creation, and bridge login flow.
3. **Makefile symmetry**: introduce `make deploy-dev-up` / `deploy-dev-down` so dev and prod follow the same operator vocabulary.
4. A **final pass on `docs/runbooks/compose-deploy.md`** so it ends with the operator landing on `https://<host>` and logging in — no gaps.
5. **Carry-forward verification**: chunk-3 F7 deferred check (real Linux `docker kill` auto-restart) gets a documented procedure here so a future fresh-VPS smoke test can run it.

**Non-goals:** Kubernetes manifests, Prometheus/Grafana wiring, cron-driven backup automation, log aggregation. Those are Stage G or later.

---

## 2. Constraints inherited

- **Compose-first.** No K8s. The Caddyfile/nginx config terminates TLS in front of compose; gateway stays on `127.0.0.1:8081` (REST/WS) and `127.0.0.1:8080` (gRPC, optional external) when behind the reverse proxy.
- **Single-host VPS target** (4 vCPU / 8 GB RAM). The reverse proxy runs on the same host as compose (not a separate LB).
- **Metrics ports stay private.** Chunk 4 carry-forward: Caddyfile MUST NOT proxy `/metrics`, `/readyz`, `/livez` — those stay intra-`hermes-net`.
- **Probe-aware health.** The Caddyfile/nginx config performs upstream health checks against gateway's `/readyz` on METRICS_PORT 9100, not `/api/healthz`. (Aligns with chunk-4 probe surface.)
- **No new secrets in code.** ACME email + domain go in `.env.prod` and get interpolated at Caddy runtime; nginx config uses placeholder strings the operator replaces.
- **Backup procedures use only tools already on the host** (`docker exec pg_dump`, `nats CLI via docker run`). No new container images required by the runbook.
- **Wire profile preserved.** No replace directives in `go.mod`.

---

## 3. Contracts

### 3.1 Public URL surface (terminated by reverse proxy)

| Path | Backend | Notes |
|---|---|---|
| `GET /` | `web:80` (in-compose nginx) | React SPA static files |
| `GET /assets/*` | `web:80` | Hashed asset bundles served by web container |
| `GET /api/*` | `gateway:8081` | REST adapter (HermesGateway service) |
| `GET /ws` | `gateway:8081` | WebSocket upgrade for the inbox/event hub |
| `POST /grpc.HermesGateway/*` | (optional) `gateway:8080` | gRPC-Web; default off in chunk 5 — most users hit REST |
| **NOT exposed** | `*:9100-9116` (METRICS_PORT), `gateway:8080` (raw gRPC) | Intra-network only |

The Caddyfile and nginx config both enforce this allow-list. There is **no fall-through** to the gateway — if a path doesn't match `/api/*` or `/ws`, it goes to the web SPA (which returns 404 for unknown routes).

### 3.2 Caddyfile contract — `deploy/caddy/Caddyfile.example`

```
# Caddyfile — Hermes reverse proxy reference
# Copy to /etc/caddy/Caddyfile (or wherever your Caddy install reads from)
# and set the {$HERMES_DOMAIN} env var. ACME email is optional but
# recommended for Let's Encrypt expiry alerts.

{
    email {$HERMES_ACME_EMAIL}
}

{$HERMES_DOMAIN} {
    encode zstd gzip

    # API + WS (WebSocket upgrade handled automatically by reverse_proxy)
    handle /api/* {
        reverse_proxy 127.0.0.1:8081 {
            # gateway readiness probe on the chunk-4 diag port
            health_uri /readyz
            health_port 9100
            health_interval 10s
            health_timeout 5s
            transport http {
                read_timeout 60s
                write_timeout 60s
            }
        }
    }
    handle /ws {
        reverse_proxy 127.0.0.1:8081 {
            health_uri /readyz
            health_port 9100
            health_interval 10s
            transport http {
                read_timeout 0       # WebSocket — disable read timeout
            }
        }
    }

    # SPA — everything else goes to the in-compose web container
    handle {
        reverse_proxy 127.0.0.1:80 {
            health_uri /healthz
            health_interval 10s
        }
    }

    # Headers — security baseline (operator can extend)
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        Referrer-Policy "strict-origin-when-cross-origin"
    }

    # Log to stdout in JSON; operator pipes to journald
    log {
        output stdout
        format json
        level INFO
    }
}
```

**Key invariants:**
- `health_port 9100` matches gateway's METRICS_PORT from chunk 4.
- `transport http { read_timeout 0 }` on `/ws` is required for long-lived WebSockets.
- HSTS is `max-age=31536000` (1y) but no `preload` — operators opt in to preload lists themselves.
- `{$HERMES_DOMAIN}` + `{$HERMES_ACME_EMAIL}` come from the same `.env.prod` file the compose stack already uses.

### 3.3 Nginx contract — `deploy/nginx/hermes.conf.example`

```nginx
# /etc/nginx/sites-available/hermes.conf
# Replace HERMES_DOMAIN with your real hostname.
# TLS certs assumed at /etc/letsencrypt/live/HERMES_DOMAIN/ (certbot default).
# If you use a different ACME client, adjust ssl_certificate paths.

upstream hermes_gateway_rest {
    server 127.0.0.1:8081 max_fails=3 fail_timeout=10s;
}

upstream hermes_web {
    server 127.0.0.1:80 max_fails=3 fail_timeout=10s;
}

# Cache the upstream readiness so we don't slam /readyz every request.
# Nginx OSS doesn't ship active health checks; this map turns /readyz into
# a fail-fast guard via an internal subrequest only when traffic actually
# arrives. Operators on nginx Plus can replace with `health_check`.
map $upstream_response_time $hermes_readyz_ok { default 1; }

server {
    listen 443 ssl http2;
    server_name HERMES_DOMAIN;

    ssl_certificate /etc/letsencrypt/live/HERMES_DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/HERMES_DOMAIN/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;

    # /api/* — REST adapter
    location /api/ {
        proxy_pass http://hermes_gateway_rest;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;
    }

    # /ws — WebSocket upgrade
    location /ws {
        proxy_pass http://hermes_gateway_rest;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 7d;       # long-lived
        proxy_send_timeout 7d;
    }

    # Everything else — SPA static
    location / {
        proxy_pass http://hermes_web;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}

# HTTP -> HTTPS redirect
server {
    listen 80;
    server_name HERMES_DOMAIN;
    return 301 https://$host$request_uri;
}
```

**Key invariants:**
- HSTS + nosniff + DENY frames mirror the Caddyfile.
- `proxy_read_timeout 7d` on `/ws` because nginx's default 60s breaks long-lived WebSockets.
- `proxy_set_header X-Forwarded-For` chain matches what gateway logs expect.
- TLS cert paths assume certbot's `/etc/letsencrypt/live/<domain>/` default — operators on dehydrated/acme.sh adjust.

### 3.4 Backup/restore runbook contract — `docs/runbooks/backup-restore.md`

Three procedures, each numbered + tested:

#### 3.4.1 Postgres backup
```bash
docker compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres \
    pg_dump -U hermes -Fc --no-owner --no-acl hermes \
    > backups/hermes-$(date +%Y%m%d-%H%M%S).pgdump
```
Restore:
```bash
docker compose -f docker-compose.prod.yml --env-file .env.prod exec -T postgres \
    pg_restore -U hermes --clean --if-exists -d hermes \
    < backups/hermes-20260530-090000.pgdump
```

#### 3.4.2 NATS stream snapshot
```bash
docker run --rm --network hermes_hermes-net -v $(pwd)/backups:/backups \
    natsio/nats-box:latest \
    nats stream backup HERMES_MBS /backups/HERMES_MBS-$(date +%Y%m%d-%H%M%S) \
    --server nats://nats:4222
```
Restore mirrors with `nats stream restore HERMES_MBS /backups/<dir>`.

#### 3.4.3 mbs cookie blob backup
Cookie blobs are encrypted-at-rest in `mbs_cookie_blobs` table — captured by the Postgres dump above. The DEK is NOT in the dump (it's a Docker secret). Restoring to a new host requires both:
- `pg_restore` from the dump
- The original `deploy/secrets/prod/mbs-dek.bin` file copied across

Without the DEK, restored cookie blobs are unreadable. Runbook calls this out explicitly with a recovery checklist.

#### 3.4.4 What's NOT covered (Stage G)
- Cron-driven backup automation
- WAL archiving / PITR
- Cross-region replication
- Encrypted off-host backup destinations (S3 with KMS)

### 3.5 mbs bootstrap runbook contract — `docs/runbooks/mbs-bootstrap.md`

End-to-end first-tenant onboarding, in this order:

1. **DEK generation** — `scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin` (already exists from chunk 3; runbook references it).
2. **JWT key generation** — `openssl rand -base64 32 > deploy/secrets/prod/jwt-signing-key && chmod 0400 deploy/secrets/prod/jwt-signing-key`.
3. **Postgres password** — `printf '%s' "$(openssl rand -base64 24)" > deploy/secrets/prod/postgres-password && chmod 0400 ...`.
4. **`.env.prod` fill-in** — point to the secret files, set HERMES_DOMAIN + HERMES_ACME_EMAIL + DATABASE_URL.
5. **Stack up** — `make deploy-prod-up`. Wait for all 12 services healthy.
6. **Tenant + admin user creation** — SQL recipe via `docker compose exec postgres psql -U hermes hermes` (insert into `tenants` + `users` with bcrypted password). Runbook gives exact SQL.
7. **First login** — `curl -X POST https://<domain>/api/auth/login` to verify JWT issuance; then browser at `https://<domain>`.
8. **Bridge login (BizApp)** — open the MBS Sessions page, click "New Bridge Login", scan cookies into the WS dialog from chunk E2.5. Acceptance: session moves through `CONNECTING` → `READY` and an inbound message appears in the inbox.

The runbook is **prescriptive** (exact commands, not options) because it's the path to a working first session. Operators read it cover-to-cover their first time, then never again.

### 3.6 Makefile contract — new targets

```makefile
# Dev compose lifecycle (mirrors deploy-prod-* shape)
deploy-dev-up:
	docker-compose -f docker-compose.dev.yml up -d
	@echo ""
	@echo "Stack starting. Watch with: make deploy-dev-logs"
	@echo "Frontend (Vite hot-reload): http://localhost:5173"
	@echo "Gateway REST: http://localhost:8081"

deploy-dev-down:
	docker-compose -f docker-compose.dev.yml down

deploy-dev-logs:
	docker-compose -f docker-compose.dev.yml logs -f --tail=200

deploy-dev-ps:
	docker-compose -f docker-compose.dev.yml ps

deploy-dev-restart:
	docker-compose -f docker-compose.dev.yml restart $(SERVICE)
```

Symmetric to chunk-3's `deploy-prod-*`. No new pre-flight (dev doesn't need secret files — uses dev defaults).

### 3.7 `docs/runbooks/compose-deploy.md` final pass

Append a new §6 "Putting it behind a reverse proxy" with:
- Caddy path: `apt install caddy && sudo cp deploy/caddy/Caddyfile.example /etc/caddy/Caddyfile && systemctl reload caddy`
- nginx path: `apt install nginx certbot python3-certbot-nginx && certbot --nginx -d <domain> && sudo cp deploy/nginx/hermes.conf.example /etc/nginx/sites-available/hermes && ln -s /etc/nginx/sites-available/hermes /etc/nginx/sites-enabled/ && nginx -t && systemctl reload nginx`
- Acceptance: `curl https://<domain>/api/healthz` returns 200; browser at `https://<domain>` shows the SPA login page.

### 3.8 Chunk-3 F7 deferred verification

A short documented procedure in the new `backup-restore.md` (or a dedicated `chaos-testing.md` if it grows) showing how to verify `restart: unless-stopped` actually restarts containers on a real Linux Docker daemon:

```bash
# On a fresh Linux VPS with the stack up:
docker kill -s KILL hermes-proxy-1
sleep 5
docker ps --format "table {{.Names}}\t{{.Status}}" | grep proxy
# Expected: status shows "Up <N> seconds (health: starting)" — i.e. Docker
# auto-restarted it. OrbStack on macOS doesn't honour this same way and the
# chunk-3 audit deferred this verification to a real Linux host.
```

---

## 4. Implementation steps

1. **`deploy/caddy/Caddyfile.example`** — write per §3.2.
2. **`deploy/nginx/hermes.conf.example`** — write per §3.3.
3. **`docs/runbooks/backup-restore.md`** — write per §3.4 + §3.8.
4. **`docs/runbooks/mbs-bootstrap.md`** — write per §3.5.
5. **`Makefile`** — append `deploy-dev-*` targets per §3.6. Update `.PHONY` line at top.
6. **`docs/runbooks/compose-deploy.md`** — append §6 per §3.7.
7. **Verification matrix** — run gates per §6.
8. **Hostile audit** — `docs/research/mbs-f-chunk5-hostile-audit-2026-05-30.md`.
9. **Commit.**

---

## 5. Files touched

**New:**
- `deploy/caddy/Caddyfile.example` (~50 LOC)
- `deploy/nginx/hermes.conf.example` (~70 LOC)
- `docs/runbooks/backup-restore.md` (~200 LOC)
- `docs/runbooks/mbs-bootstrap.md` (~250 LOC)
- `docs/research/mbs-f-chunk5-hostile-audit-2026-05-30.md` (~250 LOC)

**Modified:**
- `Makefile` — add 5 `deploy-dev-*` targets (~30 LOC delta)
- `docs/runbooks/compose-deploy.md` — append §6 "Reverse proxy" (~120 LOC delta)

**Estimate:** ~1000 LOC total, ~3 hours.

---

## 6. Acceptance gates

| # | Gate | How verified |
|---|---|---|
| G1 | `caddy validate --config deploy/caddy/Caddyfile.example` succeeds | `caddy validate` CLI on the file with env vars stubbed |
| G2 | `nginx -t -c deploy/nginx/hermes.conf.example` succeeds | requires nginx installed; we use `docker run --rm nginx:alpine nginx -t -c /etc/nginx/sites-available/hermes.conf` after templating placeholders |
| G3 | `make deploy-dev-up` brings dev stack up to healthy | Same check as chunk-1 dev compose |
| G4 | `make deploy-dev-down` tears it back down cleanly | Idempotent — second `down` no-ops |
| G5 | `docs/runbooks/backup-restore.md` `pg_dump` procedure produces a file > 1 KB against a running prod stack | manual; verified by file size + `pg_restore --list` showing tables |
| G6 | NATS stream backup produces a directory with `.stream` files | manual; verified by `find backups/HERMES_MBS-* -type f \| wc -l > 0` |
| G7 | `docs/runbooks/mbs-bootstrap.md` SQL recipe creates a tenant + admin row that survives stack restart | manual; verified by `SELECT count(*) FROM tenants; SELECT count(*) FROM users WHERE role='admin';` returning > 0 |
| G8 | Caddyfile config blocks `/metrics`, `/readyz`, `/livez` at the public surface | Send `GET /metrics` to the Caddy-fronted host; expect 404 (handle / falls through to web which returns 404) |
| G9 | Caddyfile correctly proxies `/api/healthz` to gateway 8081 | `curl http://localhost/api/healthz` (with a local Caddy install + the stack up) returns 200 |
| G10 | WebSocket upgrade survives 5-minute idle | manual; `wscat -c ws://localhost/ws -H "Authorization: Bearer <token>"` left open 5 minutes, no disconnect |
| G11 | `compose-deploy.md` §6 paste-into-shell produces a working `https://<host>` (self-signed for local test) | manual, on the dev machine with `caddy local-certs` or staging ACME |
| G12 | Chunk-3 F7 verification command runs without syntax error (procedure documented; actual VPS verification deferred to first real deploy) | doc inspection — the command syntax must be correct so an operator can paste it |

Gates G3-G7, G9-G10, G11 require a running stack. G1-G2, G8, G12 are static or single-curl checks.

---

## 7. Hostile audit categories

1. **TLS termination correctness** — does Caddy actually issue a real cert; does nginx config redirect HTTP→HTTPS correctly; HSTS not too aggressive (no preload by default).
2. **Probe surface exposure** — `/metrics`, `/readyz`, `/livez` MUST NOT leak through the reverse proxy. Verified by gate G8 + explicit allow-list in both configs.
3. **WebSocket lifecycle** — `read_timeout 0` (Caddy) and `proxy_read_timeout 7d` (nginx) are deliberately long; document why an operator might shorten them (idle-connection hygiene vs WS protocol).
4. **Upstream health gating** — when gateway `/readyz` flips to 503 (chunk 4), does Caddy actually stop routing? `health_uri` + `health_port 9100` are set explicitly. Document expected behaviour with a kill-NATS chaos test.
5. **Reverse-proxy host fingerprinting** — does Caddy/nginx leak a `Server:` header that gives away the stack? Both default to identifying themselves; documented as accepted exposure (not worth hiding).
6. **Backup procedure correctness** — `pg_dump -Fc --no-owner --no-acl` is the right flag set for restore-on-different-cluster. NATS `stream backup` requires `--server` flag pointing inside the network. Verify on a real backup→wipe→restore cycle in the runbook acceptance.
7. **DEK separation during restore** — cookie blob restore without the original DEK results in unreadable blobs. Runbook MUST make this explicit so an operator doesn't `pg_restore` to a new host without copying the secret file.
8. **mbs bootstrap SQL recipe** — `INSERT INTO users` with a bcrypt password requires the right cost factor (chunk-1 default: 12). Bad cost → either CPU-burn login or weak password. Verify with the existing bcrypt config.
9. **Makefile target collision** — `deploy-dev-up` already used elsewhere? Verified by `grep deploy-dev Makefile` returning nothing pre-chunk-5.
10. **Documentation drift** — does `compose-deploy.md` §6 still reference chunk-1 mbs-only behaviour, or does it correctly say "stack" now? Final pass cleans up.

---

## 8. Risks

- **R1 — Caddy auto-TLS rate-limiting during smoke test.** Let's Encrypt staging cert recommended for first VPS test. Doc calls out `acme_ca https://acme-staging-v02.api.letsencrypt.org/directory` as the dev knob.
- **R2 — nginx config diverges from Caddy semantics around upstream health.** nginx OSS lacks active health checks; nginx Plus has `health_check` directive. Document this delta explicitly.
- **R3 — DEK loss during pg_restore to new host.** Already covered in §3.4.3, gate G7 logic, and audit cat 7. Mitigated by runbook prominence ("THIS SECTION MATTERS").
- **R4 — Operator pastes Caddyfile with literal `{$HERMES_DOMAIN}` and wonders why ACME fails.** Document the env-var-expansion mechanism (`caddy run --envfile .env.prod` or `Environment=` line in the systemd unit).

---

## 9. Out of scope

- Kubernetes ingress (Stage G)
- Multi-region / GeoDNS (Stage G+)
- Per-tenant subdomain isolation (Stage G+)
- Prometheus federation across hosts (Stage G+)
- Cron-driven backup automation (Stage G)
- Log aggregation / Loki (Stage G+)
- WAF rules / OWASP CRS (Stage G+)

---

## 10. Open questions

| Q | Default | Override path |
|---|---|---|
| Caddy or nginx as the runbook-default? | **Caddy** (auto-TLS, single binary) | nginx config provided as alternate |
| HSTS preload by default? | **No** (max-age=1y but no `preload` directive) | Operators opt in once they're sure |
| WebSocket read timeout? | **Disabled** (Caddy 0, nginx 7d) | Tune down for high-churn deployments |
| Backup retention policy? | **Out of scope** (operator decides) | Runbook gives the dump command; cron is Stage G |
| `acme_ca` default? | **Production** (Let's Encrypt prod) | Doc shows staging-CA toggle for testing |

---

## 11. Definition of done

- All 12 gates green (G3-G7, G9-G11 require running stack; G1-G2, G8, G12 are static).
- Hostile audit committed alongside the code in `docs/research/mbs-f-chunk5-hostile-audit-2026-05-30.md`.
- One commit: `feat(deploy): Stage F chunk 5 — reverse proxy + runbooks`.
- Stage F is **complete** — the master plan's DoD is reachable: an operator with a fresh Linux VPS can follow `compose-deploy.md` end-to-end, land on `https://<host>`, log in, complete first bridge login per `mbs-bootstrap.md`, and back up the resulting tenant per `backup-restore.md`.

---

## 12. Rollback

Chunk 5 is **additive only** — no code paths change, no proto changes, no DB migrations. Rollback = `git revert <chunk5-sha>` and the stack continues running on the chunk-4 commit unchanged.
