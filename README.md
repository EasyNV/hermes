# Hermes

Hermes is a multi-tenant WhatsApp automation platform for operating WhatsApp Web sessions and Meta Business Suite (MBS) sessions from one backend/API surface.

The current codebase is a Go 1.25 service stack with a React/Vite frontend, protobuf contracts, PostgreSQL persistence, NATS/JetStream events, Redis-backed runtime state, and two private third-party integration modules:

- `third_party/mbs-native` — native Meta Business Suite / BizApp client library used by `hermes-mbs`.
- `third_party/mautrix-meta-patched` — Hermes-maintained patch of `go.mau.fi/mautrix-meta` used by the MBS bridge login path.

> Security note: this repository handles account credentials, JWTs, bridge envelopes, cookies, access tokens, TOTP secrets, and message data. Keep secrets out of docs, logs, commits, screenshots, and bug reports. Use `[REDACTED]` placeholders in examples.

## Architecture

```
┌──────────────┐     ┌────────────────────────────┐     ┌──────────┬──────────┬──────────┐
│  hermes-web  │────▶│     hermes-gateway         │────▶│ wa       │ mbs      │ campaign │
│  (React SPA) │ WS  │  gRPC :8080                │gRPC │ inbox    │ contacts │ proxy    │
│  :5173       │◀────│  REST + WS :8081           │◀────│ notify   │          │          │
└──────────────┘     └────────────────────────────┘     └──────────┴──────────┴──────────┘
                              ↕ NATS JetStream (WA + MBS + campaign/contacts/notify subjects)
                         PostgreSQL 17  ·  Redis 7  ·  NATS 2
                                   │
                   third_party/ submodules (replace directives):
                   mbs-native (BizApp client) · mautrix-meta-patched (bridge login)
```

**9 backend services + 1 operator tool:**

| Service | Port (dev) | Description |
|---------|------------|-------------|
| `hermes-gateway` | 8080 (gRPC), 8081 (REST + WS) | API gateway, JWT auth, RBAC, WebSocket hub, REST adapter |
| `hermes-wa` | gRPC + HTTP pair | WhatsApp session management via whatsmeow |
| `hermes-mbs` | 8082 (gRPC), 9092 (health) | Meta Business Suite sessions, bridge login, assets, native send, inbound poll/listen |
| `hermes-campaign` | gRPC | Bulk send engine with anti-ban controls (WA + MBS) |
| `hermes-inbox` | gRPC | Agent conversation view, message search (WA + MBS) |
| `hermes-contacts` | gRPC | Contact CRUD + CSV import |
| `hermes-proxy` | gRPC | SOCKS5/HTTP proxy pool management |
| `hermes-notify` | gRPC | Webhook + push notification dispatch |
| `hermes-web` | 5173 | React SPA (Vite + TypeScript) |
| `mbs-import` | — | One-shot operator import tool (not a long-running service) |

## Current stack

### Backend services

Hermes builds nine Go services plus one operator/import tool (see the service
table above). Service entrypoints live in `cmd/`. Private implementation lives
in `internal/`. Generated protobuf Go bindings live in `gen/go/hermes/v1/`.

### Frontend

The frontend lives under `web/` and is a React 19 + TypeScript + Vite SPA using:

- TanStack Router / Query
- Zustand
- Radix UI primitives
- Tailwind CSS
- Lucide icons

Dev compose exposes the Vite server on `http://localhost:5173`. Production compose serves a prebuilt SPA through the `hermes-web` image.

### Data/event infrastructure

- PostgreSQL 17 for service data.
- Redis 7 for runtime/session support.
- NATS 2 with JetStream for events and work queues.
- `migrate/migrate` for per-service migrations.
- Docker Compose dev/prod stacks for single-host deployment.

### Tech Stack

| Component | Choice |
|-----------|--------|
| Backend | Go 1.25 (monorepo, 9 services + `mbs-import`) |
| Frontend | React 19 + Vite + TypeScript |
| UI | Tailwind CSS + shadcn/ui + Radix + Lucide |
| State | Zustand (client) + TanStack Query (server) |
| Routing | TanStack Router |
| Database | PostgreSQL 17 (shared cluster, per-service migrations) |
| Cache | Redis 7 |
| Message Broker | NATS JetStream 2 |
| WA Library | whatsmeow (Go native, identifies as MacOS Desktop) |
| MBS Client | `third_party/mbs-native` (native BizApp/Lightspeed MQTToT) |
| MBS Bridge | `third_party/mautrix-meta-patched` (patched `go.mau.fi/mautrix-meta`) |
| Proto Codegen | buf |
| Dev Infra | Docker Compose (dev + prod stacks) |

## Project Structure

```
hermes/
├── cmd/                          # Service entry points (main.go per service)
│   ├── gateway/                  # API gateway + REST adapter + WS hub
│   ├── wa/                       # WhatsApp sessions + NATS consumers
│   ├── mbs/                      # MBS service + send consumers + JetStream streams
│   ├── mbs-import/               # One-shot operator import tool
│   ├── campaign/                 # Campaign dispatch engine
│   ├── inbox/                    # Conversation management + NATS consumers
│   ├── contacts/                 # Contact CRUD
│   ├── proxy/                    # Proxy pool
│   └── notify/                   # Notification dispatch
├── internal/                     # Service-specific code
│   ├── gateway/
│   │   ├── handler/              # 75 RPC handler implementations
│   │   ├── middleware/           # JWT auth + RBAC interceptors
│   │   ├── rest/                 # REST-to-gRPC adapter (89 mounted routes)
│   │   └── websocket/            # WebSocket hub + NATS→WS event bridge (incl. mbs_new_message)
│   ├── wa/
│   │   ├── handler/              # 8 RPC handlers
│   │   ├── session/              # whatsmeow session manager + event publisher
│   │   └── sender/               # Message send + typing indicators
│   ├── mbs/                      # 9 RPC handlers (HermesMbs)
│   │   ├── handler/              # bridge-login, lifecycle, resolve-phone, send
│   │   ├── bridge/               # mautrix-meta-patched driver + login envelope
│   │   ├── session/              # connection manager + listener + inbound snapshot parser
│   │   ├── store/                # session/asset/thread persistence
│   │   ├── importer/             # operator import
│   │   └── refresh/              # cookie/session freshness ticker
│   ├── campaign/
│   │   ├── handler/              # 17 RPC handlers
│   │   ├── engine/               # Dispatch engine + number rotation
│   │   └── spintax/              # Spintax resolver
│   ├── inbox/
│   │   ├── handler/              # 14 RPC handlers (WA + MBS conversations)
│   │   └── conversation/         # State machine
│   ├── contacts/handler/         # 11 RPC handlers
│   ├── proxy/handler/            # 11 RPC handlers
│   └── notify/
│       ├── handler/              # 6 RPC handlers
│       └── dispatch/             # Webhook dispatch
├── pkg/                          # Shared packages (db, nats, config, logger)
├── proto/hermes/v1/              # Proto source files (10 files)
├── gen/go/hermes/v1/             # Generated Go stubs (DO NOT EDIT)
├── migrations/                   # DB migrations per service (golang-migrate, 8 services)
├── third_party/                  # Private submodules (replace directives — clone --recurse-submodules)
│   ├── mbs-native/               # Native MBS/BizApp Lightspeed client (consumed by hermes-mbs)
│   └── mautrix-meta-patched/     # Patched go.mau.fi/mautrix-meta for bridge login
├── web/                          # React frontend
│   └── src/
│       ├── api/                  # Typed API client (per-domain modules incl. mbs.ts)
│       ├── pages/                # 12 page components
│       ├── components/           # Layout + shared + shadcn/ui + mbs/
│       ├── hooks/                # useAuth, useWebSocket, useDebounce
│       └── stores/               # Zustand stores (auth, inbox, campaigns, mbs, websocket)
├── deploy/                       # Deployment support, proxy config, secret file locations
├── docker-compose.dev.yml        # Full local dev stack
├── docker-compose.prod.yml       # Image-based production compose
├── Dockerfile / Dockerfile.dev / Dockerfile.web
└── docs/
    ├── API.md                    # Complete REST API reference
    ├── ARCHITECTURE.md           # Deep technical documentation
    ├── DEPLOYMENT.md             # Docker Compose setup + env vars + troubleshooting
    ├── BUILD-STATUS.md           # Latest audited build/test/security status
    └── runbooks/                 # Operator runbooks (compose-deploy, mbs-bootstrap, secrets)
```

`re/**` is reverse-engineering workspace material and is intentionally excluded from normal build/test/documentation status.

## Submodules / local replacements

The main module uses local replacements:

```text
replace mbs-native => ./third_party/mbs-native
replace go.mau.fi/mautrix-meta => ./third_party/mautrix-meta-patched
replace github.com/refraction-networking/utls => ./third_party/mbs-native/third_party/utls
```

Clone with submodules before building:

```bash
git clone --recurse-submodules <repo-url> hermes
cd hermes
# or after a normal clone:
git submodule update --init --recursive
```

Current submodule heads observed in this checkout:

- `third_party/mbs-native`: `361ac98` (`fb: authoritative per-thread inbound attribution + self-FBID hinting`)
- `third_party/mautrix-meta-patched`: `5db5641` (rebased onto upstream `v0.2605.1`; adds `LastLoginPayload` + `GetLoginIdentity` for the mbs-native bridge)

> Submodule heads move as the forks evolve. Treat the lines above as the
> last-documented pointers, not a lock — run `git submodule status` for the
> live SHAs.

## API surfaces

Hermes exposes:

- gRPC gateway service on port `8080`.
- REST JSON adapter + WebSocket surfaces on port `8081`.
- MBS gRPC service on port `8082`.
- Metrics/health ports per service (`9100`, `9111`-`9116`, `9092`, etc.).

Current protobuf services:

- `HermesGateway` — 75 RPCs.
- `HermesMbs` — 9 RPCs.
- `HermesCampaign` — 17 RPCs.
- `HermesInbox` — 14 RPCs.
- `HermesContacts` — 11 RPCs.
- `HermesProxy` — 11 RPCs.
- `HermesWa` — 8 RPCs.
- `HermesNotify` — 6 RPCs.

Current REST adapter route count: **89** mounted routes total, including the MBS bridge-login WebSocket.

Major REST groups:

- `/api/v1/auth/*`
- `/api/v1/dashboard/*`
- `/api/v1/tenants*`
- `/api/v1/workspaces*`
- `/api/v1/users*`
- `/api/v1/wa-numbers*`
- `/api/v1/proxies*`
- `/api/v1/contacts*`
- `/api/v1/templates*`
- `/api/v1/campaigns*`
- `/api/v1/conversations*`
- `/api/v1/messages/search`
- `/api/v1/agent-performance`
- `/api/v1/canned-responses*`
- `/api/v1/mbs-sessions*`
- `/api/v1/allowlist*`
- `/api/v1/notifications*`
- `/ws/mbs/bridge-login`

See `docs/API.md` for the route inventory and wire-shape notes.

## MBS integration status

Hermes MBS is now an in-stack service, not a detached RE tool.

Current MBS path:

1. Frontend opens `/ws/mbs/bridge-login?token=<jwt>`.
2. Gateway validates the JWT inline, forces tenant metadata from JWT claims, then opens the `HermesMbs.BridgeLogin` bidirectional gRPC stream.
3. `hermes-mbs` runs a mautrix-meta-backed bridge login driver using email/password and optional TOTP secret.
4. The bridge produces native credentials / bridge envelope material consumed by `mbs-native`.
5. `hermes-mbs` persists the session and assets, encrypting secret-bearing fields using the configured MBS DEK.
6. MBS session assets are available through `/api/v1/mbs-sessions/{uid}/assets`.
7. Manual sends go through gateway → `HermesMbs.SendMessage`; campaign/manual work queue sends use JetStream subjects under `hermes.mbs.send.*`.
8. Inbound replies are pulled from Meta's Lightspeed message store by the per-session listener and surfaced into the inbox as conversations/messages.

### Inbound listening and thread attribution

Lightspeed is **pull-not-push for message bodies**: the broker only returns an
`/ls_resp` envelope in response to an `/ls_req`. Each MBS session therefore runs
a listener goroutine that calls `SnapshotPoll("130")` on a fixed
`10s` interval (`internal/mbs/session/listener.go`) and drains new message
deltas. This poll loop — not the server-push `Inbox` channel — is the **single
authoritative inbound source**; the push path is intentionally inert for message
bodies (it cannot key inbound to a thread and previously polluted the inbox).

Attribution is done in `third_party/mbs-native/fb` (`ParseSnapshot` /
`ParseSnapshotWithSelf`) and consumed by `parseSnapshotPoll`:

- **Self/outbound detection.** The admin's *messaging* FBID is derived from the
  snapshot by intersecting participant sets across valid thread blocks
  (degenerate blocks with `<2` participants are ignored). Messages authored by
  self are dropped — they are outbound and owned by the outbound reconciliation
  consumer, not re-ingested as inbound.
- **Self-FBID hint cache.** The derived self-FBID is cached per session (atomic,
  survives reconnects) and fed back as a hint, so single-thread polls — where
  self cannot be derived by intersection — still classify direction correctly.
- **Per-thread customer attribution.** Each message is keyed to its thread via an
  exact `customerFBID → customer_id` index built from the snapshot (the customer
  FBID is the first participant scalar after each thread's `entity_id` anchor).
  When a sender FBID is ambiguous, the message is **quarantined** (not emitted)
  rather than filed into the wrong thread — wrong-inbox leakage is worse than a
  one-cycle delay, and the snapshot is re-polled every 10s so it re-attempts
  once the index stabilises.

Inbound persistence is idempotent on the global `mbs_mid` unique index. Re-polls
of already-seen messages are detected (`CreateMbsMessage` reports
`wasInserted`) and short-circuit before re-stamping `last_message_at` or
re-firing notifications, so conversation ordering reflects real message time.

Current MBS RPCs:

- `BridgeLogin`
- `ListSessions`
- `GetSessionStatus`
- `ListSessionAssets`
- `BurnSession`
- `RemoveSession`
- `ResolvePhone`
- `SendMessage`
- `Listen`

Current MBS JetStream streams:

- `HERMES_MBS` — lifecycle and inbound/outbound events on `hermes.mbs.message.>` and `hermes.mbs.session.>`.
- `HERMES_MBS_SEND` — work-queue stream for `hermes.mbs.send.>`.

Current MBS send subjects:

- `hermes.mbs.send.campaign.<tenant_id>`
- `hermes.mbs.send.manual.<tenant_id>`

Campaign MBS rotation strategies currently include:

- `round_robin`
- `least_used`

## Local development

Generate the MBS development DEK once:

```bash
./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin
```

Boot the full dev stack:

```bash
make deploy-dev-up
```

Useful local endpoints:

- Frontend: `http://localhost:5173`
- Gateway REST/WS: `http://localhost:8081`
- Gateway gRPC: `localhost:8080`
- MBS gRPC: `localhost:8082`
- MBS health/metrics: `http://localhost:9092`
- NATS monitoring: `http://localhost:8222`

Follow logs:

```bash
make deploy-dev-logs
```

Tear down:

```bash
make deploy-dev-down
```

## Production compose

Production compose is image-based and uses file-backed Docker secrets.

High-level bootstrap:

```bash
cp .env.prod.example .env.prod
./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin
./scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key
printf '%s' '[REDACTED_STRONG_POSTGRES_PASSWORD]' > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/*
# dek-generate.sh writes 64 hex chars + trailing newline (65-byte file, 32 bytes entropy).
make docker-build-all
docker compose -f docker-compose.prod.yml --env-file .env.prod up -d
```

Use `docs/runbooks/compose-deploy.md`, `docs/runbooks/secret-management.md`, and `docs/runbooks/mbs-bootstrap.md` for operator details. Do not commit `.env.prod` or files under `deploy/secrets/prod/`.

## Common commands

Install generator/build tools:

```bash
make tools
```

Generate protobuf output:

```bash
make proto-gen
```

Build all service binaries:

```bash
make build
```

Run backend tests:

```bash
go test -count=1 ./...
```

Run race tests:

```bash
go test -race -count=1 ./...
```

Build command packages:

```bash
go build ./cmd/...
```

Frontend typecheck/build:

```bash
cd web
npx --no-install tsc --noEmit
npm run build
```

Dependency audit:

```bash
cd web
npm audit --omit=dev --audit-level=moderate
npm audit --audit-level=moderate
```

## Latest audited quality-gate status

Scope: non-`re/**` code only.

- `go test -count=1`: passed across 50 non-`re` Go packages.
- `go test -race -count=1`: passed across 50 non-`re` Go packages.
- `go build ./cmd/...`: passed across 9 command packages.
- `gofmt -l` over tracked/untracked non-`re` Go files: passed.
- `npx --no-install tsc --noEmit` in `web`: passed.
- Vite production build in `web`: passed; emitted a chunk-size warning around `680 kB` for the main JS chunk.
- `npm audit --omit=dev --audit-level=moderate` in `web`: passed.
- Full `npm audit --audit-level=moderate` in `web`: failed because dev dependency advisories were present (`vite`, `postcss`).
- `go vet`: failed in `internal/mbs/session/listener_hook_test.go` because a range variable copies `sync/atomic.Int64`.
- `go mod verify`: failed for the local/replaced `mbs-native` module state with missing ziphash metadata.
- `buf lint`: not run in the audit environment because `buf` was unavailable.

These results document the current state only. They do not imply the failing gates were fixed.

## Current hardening priorities

- Enforce REST RBAC parity with gRPC RBAC.
- Keep object-level tenant/workspace/session authorization under review, especially MBS session and campaign paths.
- Restrict CORS/origin posture by deployment environment.
- Harden WebSocket token handling and origin policy.
- Validate MBS multi-page routing inputs such as `page_id_override` against owned session assets.
- Confirm every secret-bearing MBS bridge/session field is encrypted at rest and redacted in logs.
- Keep NATS subject tenant suffixes and payload tenant/session data cross-checked.
- Decide whether campaign progress should represent enqueue/send attempt or confirmed delivery.
- Fix the `go vet` atomic copy issue.
- Normalize local/replaced module verification behavior or document the expected `go mod verify` exception.
- Upgrade/audit dev frontend dependencies.
- Install/re-enable protobuf lint tooling (`buf`).

## Documentation

- `docs/ARCHITECTURE.md` — service graph, gateway, eventing, MBS architecture, security boundaries.
- `docs/API.md` — REST/gRPC/WebSocket surfaces.
- `docs/DEPLOYMENT.md` — dev/prod compose and operator workflow.
- `docs/BUILD-STATUS.md` — latest audited build/test/security status.
- `docs/runbooks/` — operational runbooks.
- `third_party/mbs-native/README.md` — native MBS client module details.
- `third_party/mautrix-meta-patched/README.md` — patched mautrix-meta dependency details.
