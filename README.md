# Hermès

Multi-tenant WhatsApp automation platform. Manages bulk campaigns with anti-ban controls and provides a web-based agent inbox for handling replies.

## Architecture

```
┌──────────┐     ┌──────────────────┐     ┌��─────────┬──────────┬──────────┐
│ hermes-  │────▶│ hermes-gateway   │────▶│ wa       │ campaign │ inbox    │
│ web      │ WS  │ (API + Auth +    │gRPC │ proxy    │ contacts │ notify   │
│ (React)  │◀────│  RBAC + WS hub)  │◀────│          │          │          │
└──────────┘     └──────────────────���     └──────────┴──────────┴──────────┘
                         ↕ NATS JetStream (8 event subjects)
                    PostgreSQL 17  ·  Redis 7  ·  NATS 2
```

**8 services:**

| Service | Purpose | Port |
|---------|---------|------|
| `hermes-gateway` | API gateway, JWT auth, RBAC, WebSocket hub | 8080 (gRPC), 8081 (WS) |
| `hermes-wa` | WhatsApp session management via whatsmeow | 9104 |
| `hermes-campaign` | Bulk send engine with anti-ban controls | 9105 |
| `hermes-inbox` | Agent conversation view, message search | 9106 |
| `hermes-contacts` | Contact CRUD + CSV import | 9102 |
| `hermes-proxy` | SOCKS5/HTTP proxy pool management | 9101 |
| `hermes-notify` | Webhook + push notification dispatch | 9103 |
| `hermes-web` | React SPA (Vite + TypeScript) | 5173 |

**75 gRPC RPCs** across 11 domains. See [docs/API.md](docs/API.md) for the complete API reference.

## Prerequisites

- Go 1.22+
- Node.js 20+
- Docker + Docker Compose
- `buf` CLI: `go install github.com/bufbuild/buf/cmd/buf@latest`
- `protoc-gen-go` + `protoc-gen-go-grpc` (installed via `make tools`)

## Quickstart

```bash
# 1. Start infrastructure (PostgreSQL, Redis, NATS)
docker compose up -d

# 2. Install Go tools
make tools

# 3. Generate proto stubs
make proto-gen

# 4. Run database migrations
export DATABASE_URL="postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable"
make migrate

# 5. Build all services
make build

# 6. Start services (each in a separate terminal, or use make dev)
make dev

# 7. Start frontend
cd web && npm install && npm run dev
```

The frontend will be available at `http://localhost:5173`.

## Environment Variables

### All Services

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable` | PostgreSQL connection string |
| `NATS_URL` | `nats://localhost:4222` | NATS server URL |
| `PORT` | service-specific | gRPC listen port |

### Gateway Only

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `hermes-dev-jwt-secret-change-in-prod` | HMAC secret for JWT signing |
| `WA_ADDR` | `localhost:9104` | hermes-wa gRPC address |
| `CAMPAIGN_ADDR` | `localhost:9105` | hermes-campaign gRPC address |
| `INBOX_ADDR` | `localhost:9106` | hermes-inbox gRPC address |
| `CONTACTS_ADDR` | `localhost:9102` | hermes-contacts gRPC address |
| `PROXY_ADDR` | `localhost:9101` | hermes-proxy gRPC address |
| `NOTIFY_ADDR` | `localhost:9103` | hermes-notify gRPC address |

### WA Service

| Variable | Default | Description |
|----------|---------|-------------|
| `PROXY_SERVICE_ADDR` | `localhost:9101` | hermes-proxy gRPC address |
| `REDIS_URL` | `redis://localhost:6380` | Redis for session caching |

## Docker Compose

```bash
# Start all infrastructure
docker compose up -d

# Check service health
docker compose ps

# View logs
docker compose logs -f nats
```

Infrastructure services:
- **PostgreSQL 17**: `localhost:5433` (user: `hermes`, pass: `hermes_dev`, db: `hermes`)
- **Redis 7**: `localhost:6380`
- **NATS JetStream 2**: `localhost:4222` (monitoring: `localhost:8222`)

## Directory Structure

```
hermes/
├── cmd/                  # Service entry points (main.go per service)
│   ├── gateway/          # API gateway + WS hub
│   ├── wa/               # WhatsApp sessions
│   ├── campaign/         # Campaign engine
│   ��── inbox/            # Conversations
│   ├── contacts/         # Contact management
│   ├── proxy/            # Proxy pool
│   └── notify/           # Notifications
├── internal/             # Service-specific code
│   ├── gateway/
│   │   ├── handler/      # 75 RPC handlers (auth, routing, gateway-owned CRUD)
│   │   ├── middleware/    # JWT auth interceptor + RBAC interceptor
│   ���   ├── websocket/    # WebSocket hub + NATS→WS event bridge
│   │   └── config/
│   ├── wa/
│   │   ├── handler/      # 8 RPC handlers
│   │   ���── session/      # whatsmeow session manager
│   │   └── sender/       # Message send + typing indicators
���   ├── campaign/
│   ��   ├── handler/      # 17 RPC handlers
│   │   ├── engine/       # Dispatch engine + number rotation
│   │   └── spintax/      # Spintax resolver
│   ├── inbox/
│   │   ├── handler/      # 14 RPC handlers
│   │   └── conversation/ # State machine
│   ├── contacts/
│   │   ├── handler/      # 11 RPC handlers
│   │   └── importer/     # CSV parser + dedup
│   ├─�� proxy/
│   ���   ├── handler/      # 11 RPC handlers
│   │   └── health/       # Proxy health checker
│   └── notify/
│       ├── handler/      # 6 RPC handlers
│       └── dispatch/     # Webhook + push dispatch
├── pkg/                  # Shared packages
│   ├── db/               # PostgreSQL pool + migration helpers
��   ├── nats/             # NATS JetStream client
│   ├── config/           # Env-based config loading
│   └── logger/           # Structured logging (zerolog)
├── proto/                # Proto source files
├── gen/                  # Generated Go stubs (DO NOT EDIT)
├── migrations/           # DB migrations per service (golang-migrate)
├���─ web/                  # React frontend (hermes-web)
│   ├── src/
│   │   ├��─ api/          # API client (typed, per-domain modules)
│   │   ├── pages/        # 11 page components
│   │   ├── components/   # Layout + shared + shadcn/ui
│   │   ├── hooks/        # useAuth, useWebSocket, useDebounce
│   │   └── stores/       # Zustand stores (auth, inbox, campaigns, websocket)
│   └── dist/             # Production build output
└── docs/
    ├── API.md            # Complete API reference (75 RPCs)
    ├── BUILD-STATUS.md   # Layer-by-layer build progress
    ├── research/
    │   └── ARCHITECTURE.md
    └── contracts/
        ├── proto/        # Contract proto files (source of truth)
        ├── EVENTS.md     # NATS event schemas
        └── WEBSOCKET.md  # WebSocket event schemas
```

## Testing

```bash
# Run all tests
make test

# Run tests for a specific service
go test ./internal/gateway/... -v -count=1
go test ./internal/campaign/... -v -count=1

# Build all binaries (compile check)
make build
```

**Current status:** 182 tests across 12 test files, all passing. See [docs/BUILD-STATUS.md](docs/BUILD-STATUS.md).

## Documentation

- [API Reference](docs/API.md) — 75 RPCs grouped by domain with RBAC matrix
- [Architecture](docs/research/ARCHITECTURE.md) — library choices, DB schema, anti-ban strategy
- [Service Contracts](docs/contracts/README.md) — proto definitions, NATS events, WebSocket events
- [Build Status](docs/BUILD-STATUS.md) — layer-by-layer progress
- [CLAUDE.md](CLAUDE.md) — full project context for AI agents

## Tech Stack

| Component | Choice |
|-----------|--------|
| Backend | Go 1.22 (monorepo, microservices) |
| Frontend | React 19 + Vite + TypeScript |
| UI | Tailwind CSS + shadcn/ui + Radix |
| State | Zustand (client) + TanStack Query (server) |
| Routing | TanStack Router |
| Database | PostgreSQL 17 |
| Cache | Redis 7 |
| Message Broker | NATS JetStream 2 |
| WA Library | whatsmeow |
| Proto Codegen | buf |
| Dev Infra | Docker Compose |
| Prod Deploy | AWS EKS (Phase 2) |

## License

Private. Internal use only.
