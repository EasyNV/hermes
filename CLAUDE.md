# CLAUDE.md — Hermès Project Context

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
├── CLAUDE.md                   # This file
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
