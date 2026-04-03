# Hermès

Multi-tenant WhatsApp automation platform. Manages bulk campaigns with anti-ban controls and provides a web-based agent inbox for handling replies.

## Architecture

```
┌──────────┐     ┌──────────┐     ┌─────────┬─────────┬──────────┐
│ hermes-  │────▶│ hermes-  │────▶│ wa      │campaign │ inbox    │
│ web      │ WS  │ gateway  │gRPC │ proxy   │contacts │ notify   │
│ (React)  │◀────│ (API+WS) │◀────│         │         │          │
└──────────┘     └──────────┘     └─────────┴─────────┴──────────┘
                                       ↕ NATS JetStream
                                  PostgreSQL  ·  Redis
```

**8 services:** gateway (API + auth), wa (WhatsApp sessions), campaign (bulk send engine), inbox (agent conversations), contacts (import + CRUD), proxy (pool management), notify (webhooks + push), web (React SPA).

## Prerequisites

- Go 1.22+
- Node.js 20+
- Docker + Docker Compose
- `buf` CLI (proto codegen): `go install github.com/bufbuild/buf/cmd/buf@latest`
- `protoc-gen-go` + `protoc-gen-go-grpc` (installed via `make tools`)

## Quickstart

```bash
# Start infrastructure (PostgreSQL, Redis, NATS)
docker compose up -d postgres redis nats

# Generate proto stubs
make proto-gen

# Run database migrations
make migrate

# Start all services (dev mode)
make dev

# Start frontend
cd web && npm install && npm run dev
```

## Directory Structure

```
hermes/
├── cmd/                  # Service entry points (main.go per service)
├── internal/             # Service-specific code
├── pkg/                  # Shared packages (auth, db, nats, grpc, logger)
├── proto/hermes/v1/      # Proto source files
├── gen/                  # Generated Go + TypeScript stubs (DO NOT EDIT)
├── migrations/           # Database migrations per service
├── web/                  # React frontend
├── deploy/               # Dockerfiles + K8s manifests
└── docs/
    ├── research/ARCHITECTURE.md   # Architecture decisions
    └── contracts/                 # Proto contracts + event schemas
```

## Documentation

- [Architecture Research](docs/research/ARCHITECTURE.md) — library choices, DB schema, sharding, anti-ban
- [Service Contracts](docs/contracts/README.md) — proto definitions, NATS events, WebSocket events
- [CLAUDE.md](CLAUDE.md) — full project context for AI agents

## Tech Stack

| Component | Choice |
|---|---|
| Backend | Go (monorepo, microservices) |
| Frontend | React + Vite + TypeScript |
| Database | PostgreSQL |
| Cache | Redis |
| Message Broker | NATS JetStream |
| WA Library | whatsmeow |
| Proto Codegen | buf |
| Deployment (dev) | Docker Compose |
| Deployment (prod) | AWS EKS |
