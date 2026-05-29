# Hermes Deployment Environment Reference

**Stage:** F — Deploy Hardening
**Status:** Living document — chunk 1 establishes structure for hermes-mbs;
chunks 2-5 extend with prod-specific overrides and other services.

This is the deployment contract. Every service's runtime configuration is
listed here. Anything not in this file is **not** a public contract — it's
internal and may change without notice.

---

## Conventions

- **Required:** boot fails if missing (`log.Fatal`).
- **Recommended:** default exists but is unsafe / inappropriate for prod.
- **Optional:** default exists and is fine for prod.
- All durations parse as Go `time.ParseDuration` strings (`30s`, `5m`,
  `1h`, `30d` is **not** supported — write `720h` for 30 days).
- Booleans accept the `strconv.ParseBool` set (`true`/`false`/`1`/`0`/
  `TRUE`/`FALSE`).

---

## `hermes-mbs` (cmd/mbs)

### Server
| Var | Default | Tier | Notes |
|---|---|---|---|
| `PORT` | `8082` | Optional | gRPC server port. |
| `METRICS_PORT` | `9092` | Optional | Diag HTTP server (`/livez`, `/readyz`, `/metrics`, `/debug/pprof` if `MBS_ENABLE_PPROF=true`). |
| `POD_ID` | `hermes-mbs` | Recommended | Identifies this process for the pod-claim row in `mbs_sessions`. **Set per-replica in prod** (e.g., `hermes-mbs-0`, `hermes-mbs-1`). Two pods sharing one POD_ID will fight over sessions. |

### Database
| Var | Default | Tier | Notes |
|---|---|---|---|
| `DATABASE_URL` | (empty) | **Required** | Postgres DSN. Boot pings on start; fail-closed. |
| `DB_SSLMODE` | `prefer` | Recommended | `disable` for compose-internal PG, `verify-full` for managed DBs. |
| `DB_SSLROOTCERT` | (empty) | Recommended | CA bundle path. Required when `DB_SSLMODE=verify-ca` or `verify-full`. |
| `DB_MAX_CONNS` | `20` | Optional | pgxpool cap. |
| `DB_CONN_MAX_LIFETIME` | `30m` | Optional | Connection recycle interval. |

### NATS JetStream
| Var | Default | Tier | Notes |
|---|---|---|---|
| `NATS_URL` | `nats://nats:4222` | Recommended | Compose-default works in dev; prod points at NATS cluster. |
| `NATS_CREDS_FILE` | (empty) | Recommended (prod) | JWT creds path. Empty in dev (no auth). |
| `MBS_STREAM_REPLICAS` | `1` | Recommended (prod) | `1` for single-node NATS (compose), `3` for clustered NATS. |

### DEK (Data Encryption Key)
**One of these two is required.** File takes precedence over hex.

| Var | Default | Tier | Notes |
|---|---|---|---|
| `HERMES_MBS_DEK_FILE` | (empty) | **Required** (either this or DEK_HEX) | Path to raw 32-byte DEK file. **Recommended path: `/run/secrets/mbs_dek`** (Docker secret tmpfs mount). |
| `HERMES_MBS_DEK_HEX` | (empty) | **Required** (either this or DEK_FILE) | 64-char hex string (32 bytes). Useful for ephemeral test envs; **not recommended in prod** (env vars are visible to `docker inspect` and process listings). |

**Generation:** `./scripts/dek-generate.sh <path>` (writes 32 bytes from
`openssl rand`, sets `chmod 400`).

**Rotation:** see `docs/runbooks/secret-management.md` (chunk 3).

### Cookie refresh cron
| Var | Default | Tier | Notes |
|---|---|---|---|
| `MBS_REFRESH_INTERVAL` | `1h` | Optional | How often the refresh ticker checks for cookies near expiry. |
| `MBS_REFRESH_THRESHOLD` | `720h` (30 days) | Optional | Cookie age past which refresh attempts begin. |
| `MBS_REFRESH_CONCURRENCY` | `5` | Optional | Max parallel refresh probes. |

### Bridge driver (mautrix-meta embedded)
| Var | Default | Tier | Notes |
|---|---|---|---|
| `MBS_BRIDGE_TIMEOUT` | `180s` | Optional | Overall bridge-login budget per attempt. |
| `MBS_BRIDGE_2FA_TIMEOUT` | `120s` | Optional | User has this long to enter 2FA code before the attempt fails. |
| `MBS_BRIDGE_MAX_CONCURRENT` | `10` | Recommended | OOM blast-radius cap. mautrix-meta carries a sizable per-session in-process footprint; tune to host RAM. |
| `MAUTRIX_DISABLE_TLS` | `false` | **DANGER — keep false** | Process-wide TLS-verify disable. Unrecoverable until restart. Single-tenant dev capture only. Logs a stark WARN at boot if true. |

### Legacy importer (dev only)
| Var | Default | Tier | Notes |
|---|---|---|---|
| `MBS_IMPORT_LEGACY_ON_STARTUP` | `false` | Optional | One-shot legacy CSV import at boot. Prefer `cmd/mbs-import` standalone in prod. |
| `MBS_IMPORT_LEGACY_DIR` | (empty) | — | Required if previous is true. |
| `MBS_IMPORT_LEGACY_TENANT_ID` | (empty) | — | Required if previous is true. |

### Encryption rewrite (one-shot gate)
| Var | Default | Tier | Notes |
|---|---|---|---|
| `MBS_ENCRYPT_REWRITE_ON_STARTUP` | `false` | Optional | Drop after first deploy. Rewrites existing cookies with current DEK if envelope drifted. |

### Graceful shutdown
| Var | Default | Tier | Notes |
|---|---|---|---|
| `MBS_SHUTDOWN_DRAIN_TIMEOUT` | `30s` | Optional | Hard ceiling on NATS drain → gRPC GracefulStop → mgr.Shutdown sequence. |

### gRPC server tuning
| Var | Default | Tier | Notes |
|---|---|---|---|
| `GRPC_MAX_CONCURRENT_STREAMS` | `1000` | Optional | Per-connection HTTP/2 stream cap. |
| `GRPC_KEEPALIVE_TIME` | `30s` | Optional | Keepalive ping interval. |
| `GRPC_KEEPALIVE_TIMEOUT` | `10s` | Optional | Ping ACK deadline. |

### Debug
| Var | Default | Tier | Notes |
|---|---|---|---|
| `MBS_ENABLE_PPROF` | `false` | Optional | Mounts `/debug/pprof` on the diag HTTP server. |

---

## Other services

To be filled in chunks 2-5. Currently each service's `internal/<svc>/config/`
package is authoritative. The chunk-4 audit will enumerate every env var and
this file will be the consolidated output.

Placeholder stubs:

### `hermes-gateway` (cmd/gateway)
- `PORT=8080` (gRPC) + grpc-web/REST port (currently `8081` exposed).
- `MBS_ADDR=localhost:8082` — must be set to `mbs:8082` in compose.
- `JWT_SECRET` — **Required**. Chunk 3 moves to Docker secret.
- `WA_ADDR`, `PROXY_ADDR`, `CONTACTS_ADDR`, `CAMPAIGN_ADDR`, `INBOX_ADDR`,
  `NOTIFY_ADDR` — gRPC connect addresses for downstream services.

### `hermes-wa` (cmd/wa)
- TBD chunk 4.

### `hermes-inbox` (cmd/inbox)
- TBD chunk 4.

### `hermes-campaign` (cmd/campaign)
- TBD chunk 4.

### `hermes-contacts` (cmd/contacts)
- TBD chunk 4.

### `hermes-proxy` (cmd/proxy)
- TBD chunk 4.

### `hermes-notify` (cmd/notify)
- TBD chunk 4.

---

## Lookup helpers

- For a service's config struct: `internal/<svc>/config/config.go`.
- For shared helpers: `pkg/config/`.
- For DEK file format and rotation: `docs/runbooks/secret-management.md`
  (chunk 3).
- For full compose env file shape: `.env.prod.example` (chunk 3).
