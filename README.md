# Hermes

Hermes is a multi-tenant WhatsApp automation platform for operating WhatsApp Web sessions and Meta Business Suite (MBS) sessions from one backend/API surface.

The current codebase is a Go 1.25 service stack with a React/Vite frontend, protobuf contracts, PostgreSQL persistence, NATS/JetStream events, Redis-backed runtime state, and two private third-party integration modules:

- `third_party/mbs-native` — native Meta Business Suite / BizApp client library used by `hermes-mbs`.
- `third_party/mautrix-meta-patched` — Hermes-maintained patch of `go.mau.fi/mautrix-meta` used by the MBS bridge login path.

> Security note: this repository handles account credentials, JWTs, bridge envelopes, cookies, access tokens, TOTP secrets, and message data. Keep secrets out of docs, logs, commits, screenshots, and bug reports. Use `[REDACTED]` placeholders in examples.

## Current stack

### Backend services

Hermes builds eight primary Go services plus one operator/import tool:

- `gateway` — public gRPC, REST, WebSocket, auth, JWT, RBAC, service fan-out.
- `proxy` — proxy inventory, assignment, health, and ban-flag support.
- `contacts` — contacts, tags, imports, ban checks, campaign history support.
- `notify` — notification configuration and test dispatch.
- `wa` — whatsmeow-backed WhatsApp sessions paired through QR/phone pairing.
- `mbs` — Meta Business Suite sessions, bridge login, session assets, phone resolution, native sends, inbound listen.
- `campaign` — template/campaign lifecycle and WA/MBS send orchestration.
- `inbox` — conversations, messages, agent assignment, canned responses.
- `mbs-import` — one-shot operator import tooling.

Service entrypoints live in `cmd/`. Private implementation lives in `internal/`. Generated protobuf Go bindings live in `gen/go/hermes/v1/`.

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

## Repository layout

```text
.
├── cmd/                         # Service entrypoints + mbs-import operator tool
├── deploy/                      # Deployment support, proxy config, secret file locations
├── docs/                        # Architecture, API, deployment, status, runbooks, contracts
├── gen/go/hermes/v1/            # Generated protobuf Go bindings
├── internal/                    # Private service implementations
├── migrations/                  # Per-service DB migrations
├── proto/hermes/v1/             # Protobuf API/event definitions
├── scripts/                     # Operator/build helper scripts
├── third_party/
│   ├── mautrix-meta-patched/    # Hermes-patched go.mau.fi/mautrix-meta submodule
│   └── mbs-native/              # Hermes MBS native client submodule
├── web/                         # React/Vite frontend
├── docker-compose.dev.yml       # Local full-stack compose
├── docker-compose.prod.yml      # Image-based production compose
├── go.mod
└── Makefile
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
- `third_party/mautrix-meta-patched`: `316e495` (patched on top of upstream `2313d20`; adds `LastLoginPayload` + `GetLoginIdentity` for the mbs-native bridge)

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
