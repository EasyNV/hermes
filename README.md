# Hermès

Multi-tenant WhatsApp automation platform. Manages bulk campaigns with anti-ban controls and provides a web-based agent inbox for handling replies.

## Architecture

```
┌──────────────┐     ┌────────────────────────────┐     ┌──────────┬──────────┬──────────┐
│  hermes-web  │────▶│     hermes-gateway         │────▶│ wa       │ campaign │ inbox    │
│  (React SPA) │ WS  │  gRPC :8080                │gRPC │ proxy    │ contacts │ notify   │
│  :5173       │◀────│  REST + WS :8081           │◀────│          │          │          │
└──────────────┘     └────────────────────────────┘     └──────────┴──────────┴──────────┘
                              ↕ NATS JetStream (8 event subjects)
                         PostgreSQL 17  ·  Redis 7  ·  NATS 2
```

**8 services:**

| Service | Port | Description |
|---------|------|-------------|
| `hermes-gateway` | 8080 (gRPC), 8081 (REST + WS) | API gateway, JWT auth, RBAC, WebSocket hub, REST adapter |
| `hermes-wa` | 9104 (gRPC), 9105 (HTTP pair) | WhatsApp session management via whatsmeow |
| `hermes-campaign` | 9105 | Bulk send engine with anti-ban controls |
| `hermes-inbox` | 9106 | Agent conversation view, message search |
| `hermes-contacts` | 9102 | Contact CRUD + CSV import |
| `hermes-proxy` | 9101 | SOCKS5/HTTP proxy pool management |
| `hermes-notify` | 9103 | Webhook + push notification dispatch |
| `hermes-web` | 5173 | React SPA (Vite + TypeScript) |

## Quick Start

```bash
# Start everything (infrastructure + all services + frontend)
docker compose -f docker-compose.dev.yml up --build

# Wait for all containers to be healthy (~30s), then open:
# http://localhost:5173
```

**Login:** `admin@hermes.local` / `admin123`

## API

The gateway exposes three protocols:

| Protocol | Port | Use |
|----------|------|-----|
| REST/JSON | 8081 | Frontend API — 76 endpoints under `/api/v1/` |
| gRPC | 8080 | Internal service-to-service communication |
| WebSocket | 8081 `/ws` | Real-time events (messages, campaign progress, number status) |

See [docs/API.md](docs/API.md) for the complete REST API reference.

## Project Structure

```
hermes/
├── cmd/                      # Service entry points (main.go per service)
│   ├── gateway/              # API gateway + REST adapter + WS hub
│   ├── wa/                   # WhatsApp sessions + NATS consumers
│   ├── campaign/             # Campaign dispatch engine
│   ├── inbox/                # Conversation management + NATS consumers
│   ├── contacts/             # Contact CRUD
│   ├── proxy/                # Proxy pool
│   └── notify/               # Notification dispatch
├── internal/                 # Service-specific code
│   ├── gateway/
│   │   ├── handler/          # 75 RPC handler implementations
│   │   ├── middleware/       # JWT auth + RBAC interceptors
│   │   ├── rest/             # REST-to-gRPC adapter (76 HTTP routes)
│   │   └── websocket/        # WebSocket hub + NATS→WS event bridge
│   ├── wa/
│   │   ├── handler/          # 8 RPC handlers
│   │   ├── session/          # whatsmeow session manager + event publisher
│   │   └── sender/           # Message send + typing indicators
│   ├── campaign/
│   │   ├── handler/          # 17 RPC handlers
│   │   ├── engine/           # Dispatch engine + number rotation
│   │   └── spintax/          # Spintax resolver
│   ├── inbox/
│   │   ├── handler/          # 14 RPC handlers
│   │   └── conversation/     # State machine
│   ├── contacts/handler/     # 11 RPC handlers
│   ├── proxy/handler/        # 11 RPC handlers
│   └── notify/
│       ├── handler/          # 6 RPC handlers
│       └── dispatch/         # Webhook dispatch
├── pkg/                      # Shared packages (db, nats, config, logger)
├── proto/hermes/v1/          # Proto source files (9 files)
├── gen/go/hermes/v1/         # Generated Go stubs (DO NOT EDIT)
├── migrations/               # DB migrations per service (golang-migrate)
├── web/                      # React frontend
│   └── src/
│       ├── api/              # Typed API client (per-domain modules)
│       ├── pages/            # 11 page components
│       ├── components/       # Layout + shared + shadcn/ui
│       ├── hooks/            # useAuth, useWebSocket, useDebounce
│       └── stores/           # Zustand stores (auth, inbox, campaigns, websocket)
├── docker-compose.dev.yml    # Full local dev stack (12 containers)
├── Dockerfile.dev            # Multi-stage Go builder
└── docs/
    ├── API.md                # Complete REST API reference (76 endpoints)
    ├── ARCHITECTURE.md       # Deep technical documentation
    ├── DEPLOYMENT.md         # Docker Compose setup + env vars + troubleshooting
    └── BUILD-STATUS.md       # Phase 1 complete, Phase 2 roadmap
```

## Testing

```bash
# Run all tests
go test ./... -count=1

# Run tests for a specific service
go test ./internal/gateway/... -v -count=1

# Build all binaries
make build
```

**Current:** 12 packages, 276 test assertions, all passing.

## Tech Stack

| Component | Choice |
|-----------|--------|
| Backend | Go 1.25 (monorepo, 8 microservices) |
| Frontend | React 19 + Vite + TypeScript |
| UI | Tailwind CSS + shadcn/ui + Radix |
| State | Zustand (client) + TanStack Query (server) |
| Routing | TanStack Router |
| Database | PostgreSQL 17 (shared cluster, per-service migrations) |
| Cache | Redis 7 |
| Message Broker | NATS JetStream 2 |
| WA Library | whatsmeow (Go native, identifies as MacOS Desktop) |
| QR Code | go-qrcode (server-side PNG generation) |
| Proto Codegen | buf |
| Dev Infra | Docker Compose (12 containers) |

## Documentation

- [API Reference](docs/API.md) — 76 REST endpoints grouped by domain
- [Architecture](docs/ARCHITECTURE.md) — service graph, NATS events, DB schema, auth flow
- [Deployment](docs/DEPLOYMENT.md) — Docker Compose setup, env vars, troubleshooting
- [Build Status](docs/BUILD-STATUS.md) — Phase 1 complete, Phase 2 roadmap

## License

Private. Internal use only.
