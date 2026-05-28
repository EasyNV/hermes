# AGENTS.md — Hermès Project Context

## Project Overview

**Hermès** is a multi-tenant WhatsApp automation platform for internal use (~20-30 users). It manages 1,000–30,000 concurrent WhatsApp sessions via the whatsmeow library, sends bulk campaigns with anti-ban controls, and provides a web-based agent inbox for handling replies.

**Tech stack:** Go backend (monorepo, microservices), React frontend (Vite), PostgreSQL, Redis, NATS JetStream.

---

## Architecture

8 microservices communicating via **gRPC** (sync) and **NATS JetStream** (async events):

```
hermes-web (React SPA)
    ↕ gRPC-Web + WebSocket
hermes-gateway (API + Auth + WS hub)
    ↕ gRPC
┌────────────┬────────────┬────────────┬────────────┬────────────┬────────────┐
│ hermes-wa  │ hermes-    │ hermes-    │ hermes-    │ hermes-    │ hermes-    │
│ (sessions) │ campaign   │ inbox      │ contacts   │ proxy      │ notify     │
└────────────┴────────────┴────────────┴────────────┴────────────┴────────────┘
    ↕ NATS JetStream (async events between services)
```

| Service | Responsibility | Proto File |
|---|---|---|
| `hermes-gateway` | API gateway, JWT auth, RBAC, WebSocket hub | `gateway.proto` |
| `hermes-wa` | WhatsApp session management, message send/receive | `wa.proto` |
| `hermes-campaign` | Campaign orchestration, templates, throttling | `campaign.proto` |
| `hermes-inbox` | Conversation management, agent inbox, canned responses | `inbox.proto` |
| `hermes-contacts` | Contact CRUD, CSV import, ban checking | `contacts.proto` |
| `hermes-proxy` | Proxy pool management, health checks, assignment | `proxy.proto` |
| `hermes-notify` | Notification config and webhook/push dispatch | `notify.proto` |
| `hermes-web` | React frontend (SPA) | — |

---

## Contracts (Source of Truth)

**⚠️ READ THE RELEVANT PROTO FILE BEFORE IMPLEMENTING ANY SERVICE.**

- `docs/contracts/proto/*.proto` — gRPC service definitions for every RPC and message type
- `docs/contracts/proto/common.proto` — shared enums, resource messages, pagination types
- `docs/contracts/proto/events.proto` — NATS event payload messages
- `docs/contracts/EVENTS.md` — NATS subjects, stream configs, consumer configs, processing semantics
- `docs/contracts/WEBSOCKET.md` — WebSocket event schemas, connection lifecycle
- `docs/contracts/README.md` — contract index, conventions, verification checklist

**The contracts are the API boundary.** Every RPC, message, and field is already defined. Do not invent new endpoints — implement what the proto defines.

---

## Architecture Reference

`docs/research/ARCHITECTURE.md` contains:
- WhatsApp library choice: **whatsmeow** (Go, native proxy + typing indicators)
- Database schema ERD (all tables, columns, relationships)
- Session sharding strategy (Controller + StatefulSet, 500 sessions/pod)
- Anti-ban architecture (5 layers: message variation → timing → number rotation → proxy → session hygiene)
- NATS subject hierarchy
- Observability strategy (Prometheus + Grafana + Loki)

**Read this before making design decisions.**

---

## Directory Structure

```
hermes/
├── AGENTS.md                   # This file
├── README.md                   # Project overview + quickstart
├── go.mod                      # Go module root
├── go.sum
├── buf.yaml                    # Buf configuration for proto codegen
├── buf.gen.yaml                # Buf code generation config
├── Makefile                    # Build targets (proto-gen, migrate, dev, test, build)
├── docker-compose.yml          # Local dev: all services + infra
├── cmd/                        # Service entry points
│   ├── gateway/main.go
│   ├── wa/main.go
│   ├── campaign/main.go
│   ├── inbox/main.go
│   ├── contacts/main.go
│   ├── proxy/main.go
│   └── notify/main.go
├── internal/                   # Service-specific code (not importable by other modules)
│   ├── gateway/
│   │   ├── handler/            # gRPC handler implementations
│   │   ├── middleware/         # Auth, RBAC, rate limiting
│   │   ├── websocket/          # WebSocket hub + event fan-out
│   │   └── config/
│   ├── wa/
│   │   ├── handler/
│   │   ├── session/            # whatsmeow session manager
│   │   ├── sender/             # Message send with typing indicators
│   │   └── config/
│   ├── campaign/
│   │   ├── handler/
│   │   ├── engine/             # Campaign orchestration, throttling, rotation
│   │   ├── spintax/            # Spintax resolver
│   │   └── config/
│   ├── inbox/
│   │   ├── handler/
│   │   ├── conversation/       # Conversation state machine
│   │   └── config/
│   ├── contacts/
│   │   ├── handler/
│   │   ├── importer/           # CSV import + dedup
│   │   └── config/
│   ├── proxy/
│   │   ├── handler/
│   │   ├── health/             # Proxy health checker
│   │   └── config/
│   └── notify/
│       ├── handler/
│       ├── dispatch/           # Webhook + push dispatch
│       └── config/
├── pkg/                        # Shared packages (importable by all services)
│   ├── auth/                   # JWT generation, validation
│   ├── db/                     # PostgreSQL connection, migration runner
│   ├── nats/                   # NATS JetStream client helpers
│   ├── grpc/                   # gRPC server/client helpers, interceptors
│   ├── config/                 # Env-based config loading
│   └── logger/                 # Structured logging (zerolog)
├── proto/                      # Proto source files (copied from docs/contracts/proto/)
│   └── hermes/v1/
│       ├── common.proto
│       ├── gateway.proto
│       ├── wa.proto
│       ├── campaign.proto
│       ├── inbox.proto
│       ├── contacts.proto
│       ├── proxy.proto
│       ├── notify.proto
│       └── events.proto
├── gen/                        # Generated code (DO NOT EDIT)
│   ├── go/hermes/v1/           # Go gRPC stubs
│   └── ts/                     # TypeScript connect-es stubs (for frontend)
├── migrations/                 # Database migrations (golang-migrate format)
│   ├── gateway/
│   ├── wa/
│   ├── campaign/
│   ├── inbox/
│   ├── contacts/
│   ├── proxy/
│   └── notify/
├── web/                        # React frontend (hermes-web)
│   ├── package.json
│   ├── vite.config.ts
│   ├── src/
│   │   ├── api/                # Auto-generated API client
│   │   ├── components/
│   │   ├── pages/
│   │   ├── hooks/
│   │   ├── stores/
│   │   └── lib/
│   └── public/
├── deploy/                     # Deployment configs
│   ├── docker/                 # Dockerfiles per service
│   └── k8s/                    # Kubernetes manifests (Phase 2)
└── docs/
    ├── research/
    │   └── ARCHITECTURE.md     # Gate 1: approved architecture
    └── contracts/
        ├── README.md           # Gate 2: contract index
        ├── EVENTS.md
        ├── WEBSOCKET.md
        └── proto/              # Contract proto files (source of truth)
```

---

## Go Code Standards

### Project Layout
- `cmd/<service>/main.go` — entry point. Parse config, connect to DB/NATS, register gRPC handlers, start server.
- `internal/<service>/` — all service-specific code. Not importable outside the service.
- `pkg/` — shared utilities importable by all services.

### Config
- Environment variables only. No config files.
- Struct-based config with `envconfig` or similar. Validate on startup.
- Example:
```go
type Config struct {
    Port        int    `env:"PORT" envDefault:"8080"`
    DatabaseURL string `env:"DATABASE_URL" required:"true"`
    NatsURL     string `env:"NATS_URL" envDefault:"nats://localhost:4222"`
    RedisURL    string `env:"REDIS_URL" envDefault:"redis://localhost:6379"`
}
```

### Database
- PostgreSQL via `pgx` (preferred) or `database/sql` + `pgx` driver.
- Migrations via `golang-migrate`. Each service has its own migration directory.
- Use connection pooling (`pgxpool`).
- Queries: prefer `sqlc` for type-safe generated queries, or hand-written with `pgx`.

### gRPC
- Use `google.golang.org/grpc` for server/client.
- Interceptors for: logging, auth (JWT validation), RBAC, error recovery.
- Return proper gRPC status codes (`codes.NotFound`, `codes.PermissionDenied`, etc.).
- Wrap errors with context: `status.Errorf(codes.Internal, "failed to create tenant: %v", err)`.

### NATS
- Use `github.com/nats-io/nats.go` with JetStream API.
- Durable consumers with explicit ACK.
- Set `Nats-Msg-Id` header for deduplication.
- Consumer names follow pattern: `<service>-<event>-{tenant_id}`.

### Logging
- Structured JSON via `zerolog`.
- Always include: `service`, `request_id`, `tenant_id` (when available).
- Log levels: `debug` (dev), `info` (prod default), `warn`, `error`.

### Testing
- Table-driven tests for all RPC handlers.
- Use `testcontainers-go` for integration tests (PostgreSQL, NATS, Redis).
- Mock gRPC clients for cross-service calls in unit tests.
- Minimum coverage: RPC handlers + core business logic (engine, importer, resolver).

### Error Handling
- Always wrap errors with context: `fmt.Errorf("creating campaign: %w", err)`.
- Map domain errors to gRPC status codes in the handler layer.
- Never expose internal errors to clients — use `ErrorDetail` from `common.proto`.

---

## Frontend Standards

### Stack
- **React 19** + TypeScript + Vite
- **Zustand** for client state (lightweight, no boilerplate)
- **TanStack Query** for server state (API calls with caching, retry, invalidation)
- **TanStack Router** for type-safe routing
- **Tailwind CSS** + **shadcn/ui** for styling/components
- **connect-es** for auto-generated gRPC-Web API client from proto files

### API Client
- Generated from proto files via `buf generate` with `connect-es` plugin.
- All API calls go through `hermes-gateway` only.
- Typed request/response — no manual fetch calls.

### WebSocket
- Single WebSocket connection to `wss://{gateway}/ws`.
- Reconnecting with exponential backoff (per WEBSOCKET.md spec).
- Events dispatched to Zustand stores for real-time UI updates.

### Pages
- Login
- Dashboard (stats overview)
- Number Management (list, QR login, status, health)
- Proxy Management (list, add, health, assignment)
- Contacts (list, import CSV, create, tags)
- Templates (list, create, preview with spintax)
- Campaigns (list, create, detail with real-time progress)
- Inbox (unassigned queue, assigned conversations, chat view)
- Settings (notifications, canned responses, workspace settings)

---

## Git Discipline

- Feature branches off `main`: `feat/<service>-<description>`, `fix/<description>`
- Conventional commits: `feat(proxy): implement GetBestProxy RPC`
- Each service buildable as independent Docker image.
- Docker Compose for local dev: `docker compose up` starts all services + infra.
- Merge to main only after verification (tests pass, service starts, basic smoke test).

---

## Patterns Established in Layer 1

These patterns were established during Layer 1 and MUST be followed by all subsequent services:

### Store Interface Pattern
Every service uses a `Store` interface in `internal/<service>/handler/store.go` that abstracts all DB operations. The handler depends on the interface, not `*pgxpool.Pool` directly. This enables:
- Unit tests with in-memory mock stores (no test containers needed)
- Sentinel errors (`ErrNotFound`) instead of leaking `pgx.ErrNoRows`
- Clean separation: SQL lives in `PgStore`, business logic in handler

### Error Handling
- Define sentinel errors in the handler package: `var ErrNotFound = errors.New("not found")`
- `PgStore` translates `pgx.ErrNoRows` → `ErrNotFound`
- Handler maps domain errors to gRPC status codes: `ErrNotFound` → `codes.NotFound`
- Always validate required fields at the top of each RPC, return `codes.InvalidArgument`

### Test Pattern
- Table-driven tests with mock stores (function-field mocks, not code-generated)
- Each test case seeds initial state and exercises one code path
- Test files live in the same package as the handler (access to unexported types)
- No external test dependencies (no testcontainers for unit tests)

### Config Pattern
- `internal/<service>/config/config.go` with `Load()` function
- Reads from env vars: `PORT`, `DATABASE_URL`, `NATS_URL` (minimum)
- Uses `pkg/config.GetEnv()` / `GetEnvInt()` helpers

### Service Entry Point Pattern
```go
// cmd/<service>/main.go
func main() {
    cfg := config.Load()
    log := logger.New("hermes-<service>")
    pool := db.NewPool(cfg.DatabaseURL)  // shared pkg/db
    js, nc := nats.NewJetStream(cfg.NatsURL)  // shared pkg/nats
    defer nc.Close()
    store := handler.NewPgStore(pool)
    h := handler.New(store, js, log)
    // Register gRPC server, start listening, handle signals
}
```

### NATS Event Publishing
- Use `Nats-Msg-Id` header (via `nats.MsgId()`) for deduplication
- Subject pattern: `hermes.<domain>.<event>.{tenant_id}`
- Nil-guard JetStream context (`if h.js != nil`) so tests skip publishing
- Consumers: durable names, explicit ACK, max retries per EVENTS.md

### Service Ports (Local Dev)
| Service | Port |
|---|---|
| hermes-proxy | 9101 |
| hermes-contacts | 9102 |
| hermes-notify | 9103 |
| hermes-wa | 9104 |
| hermes-campaign | 9105 |
| hermes-inbox | 9106 |
| hermes-gateway | 8080 |

---

## Patterns Established in Layer 2

### whatsmeow Containment
- Only `internal/wa/session/manager.go` and `internal/wa/session/events.go` import whatsmeow types.
- Handler and sender depend on abstract interfaces (`session.Manager`, `sender.WaClient`).
- `waClientAdapter` wraps `*whatsmeow.Client` to implement `sender.WaClient`, keeping whatsmeow types out of handler/tests.
- Tests run without any WhatsApp connection.

### Sender / Typing Delay Pattern
- `sender.Sender` interface with injectable `sleepFn` for testing.
- Typing sequence: `SendPresence(composing=true)` → `sleep(duration)` → `SendPresence(composing=false)` → send message.
- Tests use `NewWithSleep(mockSleep)` to verify timing without real delays.

### Campaign Dispatch Engine
- One goroutine per running campaign, managed by `engine.Engine`.
- Engine maintains `map[campaignID]cancelFunc` for start/stop lifecycle.
- Batch processing: fetches 100 PENDING contacts at a time.
- Anti-ban timing: `typing_duration_ms = clamp(len(body) * rand(50,80), 1500, 8000)`, `post_send_delay_ms = rand(delay_min, delay_max)`.
- Progress events published every 10 sends or 5 seconds.

### Spintax Resolver
- Regex-based: `\{([^{}]+)\}` resolves innermost braces first, iterating outward.
- `{{variable}}` placeholders protected with sentinels during resolution to prevent spintax matching.
- Variables resolved AFTER spintax: `resolve_spintax(body)` → `substitute_variables(result, vars)`.

### gRPC Inter-Service Calls
- wa→proxy: `hermesv1.NewHermesProxyClient(conn)` for GetProxy/GetBestProxy on session connect.
- campaign→wa: NATS `CampaignSendTask` (async, not gRPC) for send orchestration.
- Proxy client is optional/nil-guarded — service starts without it if proxy is unavailable.

### NATS Event Envelope
- All events include `EventMeta`: `event_id` (UUID, used as `Nats-Msg-Id`), `tenant_id`, `timestamp`, `source`.
- Subject pattern: `hermes.<domain>.<event>.{tenant_id}`.
- Publishing helper: marshal proto → `js.Publish(subject, data, nats.MsgId(eventID))`.
- `tenantFn` closure resolves `wa_number_id → tenant_id` (cached in memory to avoid DB hit per event).

---

## Patterns Established in Layer 3

### Inbox RBAC Scoping (CRITICAL for Gateway)
- `ListConversations` has an RBAC constraint: **CS agents see only UNASSIGNED + own assigned conversations.**
- Gateway MUST inject `assigned_to=self OR status=UNASSIGNED` filter when the caller is a cs_agent.
- Admins (workspace_admin, tenant_admin, superadmin) see ALL conversations.
- This is NOT enforced in the inbox service — it's the gateway's responsibility.

### Message Storage Strategy
- Campaign-sent messages: `template_id` + `resolved_vars_json` (not full text, reconstructable)
- Inbound replies: full `body` text
- Manual agent messages: full `body` text
- Media: `media_url` stored

### Conversation State Machine
```
UNASSIGNED  →  ASSIGNED  (on ClaimConversation)
ASSIGNED    →  CLOSED    (on CloseConversation)
CLOSED      →  UNASSIGNED (on new inbound message — auto-reopen)
```
- `ClaimConversation` fails if already ASSIGNED (must transfer instead)
- `TransferConversation` requires caller to be current assignee or admin

### FTS Search Pattern
- Uses PostgreSQL `to_tsvector('simple', body)` + `plainto_tsquery` matching the GIN index from migrations
- `ts_headline` returns highlighted snippets with `<mark>` tags for frontend display
- Search scoped to workspace_id, optionally to a single conversation

### Cross-Service Reads
- Inbox reads from contacts, wa_numbers, wa_number_workspaces, campaign_contacts, campaigns, templates
- This is a pragmatic shared-DB pattern — no gRPC calls needed for co-located data
- All cross-service reads are SELECT-only (no writes to other services' tables)

### first_response_time_secs
- Calculated as `time.Since(conversation.created_at)` on the first outbound message
- Guard: `WHERE first_response_time_secs = 0` prevents overwriting on subsequent messages
- Used by `GetAgentPerformance` for avg/median calculations

---

## Patterns Established in Layer 4

### Gateway Routing Architecture
- Gateway is the ONLY service the frontend talks to (75 RPCs).
- Owns: tenants, workspaces, users, auth (JWT + refresh tokens), RBAC — stored in its own PG tables.
- Routes: all other RPCs are forwarded to backend services via gRPC client connections.
- Backend clients are nil-guarded — gateway starts even if a backend service is down; affected RPCs return `codes.Unavailable`.
- Two listeners: gRPC (port 8080) + HTTP/WebSocket (port 8081).

### Auth + RBAC
- JWT access tokens (HS256, 15 min TTL) contain: `user_id`, `tenant_id`, `workspace_id`, `role`.
- Refresh tokens stored in `refresh_tokens` table (opaque, 30-day TTL).
- Auth interceptor extracts + validates JWT from `Authorization` metadata; skips `Login` and `RefreshToken`.
- RBAC interceptor checks role against `rpcRoles` map (75 entries). Superadmin bypasses all checks.
- Interceptor chain: `AuthInterceptor` → `RBACInterceptor` → handler.

### WebSocket Hub Architecture
- `Hub` manages all active clients in a `map[*Client]struct{}` with RWMutex.
- Client auth: JWT from `?token=` query param or `Authorization` header on upgrade.
- Max 3 connections per user.
- Heartbeat: server pings every 30s, closes after 3 missed pongs (90s).
- Client messages: `ping`, `auth` (re-auth), `subscribe_conversation`, `unsubscribe_conversation`.
- Non-blocking send: `select { case c.send <- data: default: }` — drops messages if client is slow.
- Read/write pumps as separate goroutines per client.

### NATS → WebSocket Event Bridge
- `EventSubscriber` subscribes to 8 NATS subjects and translates proto events to JSON WebSocket messages.
- Subject mapping:
  - `hermes.wa.message.inbound.*` → `new_message` (tenant-scoped)
  - `hermes.wa.message.outbound.*` → `message_status_updated` (tenant-scoped)
  - `hermes.wa.connection.*` → `number_status_changed` (tenant-scoped)
  - `hermes.wa.ban.*` → `ban_detected` (tenant-scoped)
  - `hermes.campaign.status.*` → `campaign_status_changed` (workspace-scoped)
  - `hermes.campaign.progress.*` → `campaign_progress` (workspace-scoped)
  - `hermes.contacts.import.done.*` → `import_complete` (user-scoped)
  - `hermes.wa.presence.*` → `typing_indicator` (tenant-scoped)
- Durable consumers with manual ACK.
- Proto → JSON conversion happens once per event, then fanned out to matching clients.

### NATS Stream Setup
- Gateway ensures 5 streams on startup:
  - `HERMES_WA` (7d retention) — WA events
  - `HERMES_CAMPAIGN` (30d retention) — campaign events + send tasks
  - `HERMES_INBOX` (24h retention) — manual sends
  - `HERMES_CONTACTS` (24h retention) — import events
  - `HERMES_NOTIFY` (1h retention) — notification events
- Streams are idempotent (`AddStream` is a no-op if exists with same config).

### Frontend State Management
- **Zustand** for client state (auth, WebSocket connection, campaign form state).
- **TanStack Query** for server state (cached API responses with automatic invalidation).
- **TanStack Router** for type-safe file-based routing.
- Auth store: JWT tokens in memory (not localStorage), auto-refresh on 401.
- WebSocket store: single connection, reconnect with exponential backoff, event dispatch to Zustand stores.

### API Client Generation
- Hand-written TypeScript API client in `web/src/api/` mapping to gateway RPCs.
- Each domain has its own module: `auth.ts`, `campaigns.ts`, `contacts.ts`, `inbox.ts`, etc.
- Shared types in `web/src/api/types.ts` mirror proto message types.
- All calls go through `web/src/api/client.ts` base client with JWT injection.

---

## Key Technical Decisions

| Decision | Choice | Reference |
|---|---|---|
| WA Library | whatsmeow (Go) | ARCHITECTURE.md §1 |
| Message Broker | NATS JetStream | ARCHITECTURE.md §2 |
| Frontend | Vite + React SPA | ARCHITECTURE.md §3 |
| Database | PostgreSQL (shared cluster) | ARCHITECTURE.md §4 |
| Inter-service sync | gRPC from the start | Gate 1 decisions |
| Inter-service async | NATS JetStream | ARCHITECTURE.md §5 |
| Session persistence | whatsmeow native sqlstore | Gate 1 decisions |
| Deployment (dev) | Docker Compose | Gate 1 decisions |
| Deployment (prod) | AWS EKS | Gate 1 decisions |
| Proto codegen | buf | — |
