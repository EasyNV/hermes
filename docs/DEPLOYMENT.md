# Hermès Deployment Guide

## Docker Compose (Local Dev)

```bash
# Start everything
docker compose -f docker-compose.dev.yml up --build

# Start in background
docker compose -f docker-compose.dev.yml up -d --build

# Check status
docker compose -f docker-compose.dev.yml ps

# View logs for a specific service
docker compose -f docker-compose.dev.yml logs wa -f

# Rebuild a single service after code changes
docker compose -f docker-compose.dev.yml up -d --build gateway
```

### Containers (12 total)

| Container | Image | Purpose |
|-----------|-------|---------|
| postgres | postgres:17-alpine | Database (port 5433→5432) |
| redis | redis:7-alpine | Cache (port 6380→6379) |
| nats | nats:2-alpine | Message broker (port 4222, monitoring 8222) |
| migrate | migrate/migrate:v4.18.2 | DB migrations (run-once init) |
| proxy | hermes-proxy | Proxy pool service (port 9101) |
| contacts | hermes-contacts | Contact management (port 9102) |
| notify | hermes-notify | Notification dispatch (port 9103) |
| wa | hermes-wa | WhatsApp sessions (port 9104, HTTP 9105) |
| campaign | hermes-campaign | Campaign engine (port 9105) |
| inbox | hermes-inbox | Conversation management (port 9106) |
| gateway | hermes-gateway | API gateway (port 8080 gRPC, 8081 REST+WS) |
| web | node:22-alpine | Frontend dev server (port 5173) |

### Startup Order

Managed by `depends_on` with health checks:
1. Infrastructure: postgres, redis, nats (parallel)
2. Migrations: init container runs after postgres healthy
3. Layer 1: proxy, contacts, notify (parallel, after migrations)
4. Layer 2: wa (after proxy + redis), campaign (after migrations)
5. Layer 3: inbox (after migrations)
6. Layer 4: gateway (after all backend services)
7. Frontend: web (after gateway)

## Environment Variables

### All Go Services

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `DATABASE_URL` | `postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable` | Yes* | PostgreSQL connection |
| `NATS_URL` | `nats://localhost:4222` | Yes | NATS server |
| `PORT` | service-specific | No | gRPC listen port |
| `LOG_LEVEL` | `info` | No | zerolog level (debug/info/warn/error) |

*contacts and notify services require DATABASE_URL explicitly (no default).

### Gateway Only

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | `hermes-dev-jwt-secret-change-in-prod` | HMAC signing key |
| `WA_ADDR` | `localhost:9104` | WA service gRPC address |
| `CAMPAIGN_ADDR` | `localhost:9105` | Campaign service |
| `INBOX_ADDR` | `localhost:9106` | Inbox service |
| `CONTACTS_ADDR` | `localhost:9102` | Contacts service |
| `PROXY_ADDR` | `localhost:9101` | Proxy service |
| `NOTIFY_ADDR` | `localhost:9103` | Notify service |

### WA Service Only

| Variable | Default | Description |
|----------|---------|-------------|
| `POD_ID` | `hermes-wa-0` | Pod identifier for session sharding |
| `PROXY_SERVICE_ADDR` | `localhost:9101` | Proxy service gRPC |
| `REDIS_URL` | `redis://localhost:6379` | Redis for session cache |

### Frontend (Vite)

| Variable | Default | Description |
|----------|---------|-------------|
| `VITE_API_URL` | `http://localhost:8081` | Gateway REST API URL |
| `VITE_WS_URL` | `ws://localhost:8081` | Gateway WebSocket URL |

In Docker Compose, these are set to `http://gateway:8081` and `ws://gateway:8081`.

## Infrastructure Requirements

| Component | Version | Notes |
|-----------|---------|-------|
| PostgreSQL | 17+ | Single shared cluster, ~20 app tables + whatsmeow tables |
| Redis | 7+ | Session cache (WA service) |
| NATS | 2+ | JetStream enabled, file storage |
| Go | 1.25+ | For building services |
| Node.js | 22+ | For frontend dev server |
| Docker | 24+ | Docker Compose v2 |

## Database

### Connection
```
Host: localhost:5433 (Docker) or postgres:5432 (inside Docker)
User: hermes
Password: hermes_dev
Database: hermes
```

### Migrations
Each service has its own migration directory and migration table:
```bash
# Run all migrations
DATABASE_URL="postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable" make migrate

# Migrations per service
migrate -path migrations/gateway -database "$DATABASE_URL?x-migrations-table=schema_migrations_gateway" up
migrate -path migrations/wa -database "$DATABASE_URL?x-migrations-table=schema_migrations_wa" up
# ... (campaign, inbox, contacts, proxy, notify)
```

### Seed Data
Migration `000003_seed_superadmin` creates:
- Default Tenant (`00000000-...-0001`)
- Default Workspace (`00000000-...-0010`)
- Superadmin user: `admin@hermes.local` / `admin123`

## Troubleshooting

### QR Code Linking
1. Register a number in the UI
2. QR appears in modal — scan from WhatsApp → Linked Devices → Link a Device
3. **Alternative**: Click "Link with phone number" → enter phone → enter 8-char code on phone
4. If QR shows as "Invalid": the QR may have expired (20s rotation). Try again quickly.
5. **Rate limiting**: WhatsApp rate-limits pairing attempts after many tries. Wait 30-60 min.

### Device Shows as "Other App"
The WA service sets device identity in `internal/wa/session/manager.go` `init()`. If the device was linked before this change, unlink from phone and re-pair.

### NATS Subject Configuration
Streams must not have overlapping subjects. Current config uses specific subjects for HERMES_WA (not `hermes.wa.>` which would overlap with campaign/inbox send subjects).

### Port Conflicts
Default config ports for proxy (8086), contacts (8084), notify (8086) don't match the documented ports. Docker Compose sets `PORT` explicitly. For local dev without Docker, set PORT env var.

### whatsmeow Session Lost After Restart
whatsmeow stores session keys in PostgreSQL (`whatsmeow_device` table). If the table is cleared, the number needs to be re-paired. The WA service auto-reconnects on startup for numbers with existing device store entries.

### Messages Stuck at "Pending"
Check: (1) Is the WA number connected? (2) Check WA service logs for "WA client not connected, NAKing". (3) The manual/campaign consumer NAKs tasks when the client isn't connected — they'll retry automatically once connected.

### Inbox Empty / Missing Messages
Check: (1) WA service logs for "inbound message received" (2) Inbox service logs for "contact not found" (3) Phone format mismatch — contacts stored with `+` prefix but whatsmeow sends without it.
