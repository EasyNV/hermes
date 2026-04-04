# Hermès — Build Status

## Phase 1 MVP: ✅ COMPLETE

| Layer | Service | RPCs | Tests | Status | Commit |
|---|---|---|---|---|---|
| **0** | Scaffolding (go.mod, proto, docker, migrations) | — | — | ✅ Done | `bab8ee5` |
| **1** | hermes-proxy | 11/11 | 17 pass | ✅ Done | `5456f9e` |
| **1** | hermes-contacts | 11/11 | 17 pass | ✅ Done | `5456f9e` |
| **1** | hermes-notify | 6/6 | 26 pass | ✅ Done | `5456f9e` |
| **2** | hermes-wa | 8/8 | 28 pass | ✅ Done | `cb8f9e9` |
| **2** | hermes-campaign | 17/17 | 42 pass | ✅ Done | `cb8f9e9` |
| **3** | hermes-inbox | 14/14 | 29 pass | ✅ Done | `acea288` |
| **4** | hermes-gateway | 75/75 | 23 pass | ✅ Done | `f5617ee` |
| **4** | hermes-web | 11 pages | 0 (TypeScript strict) | ✅ Done | `f5617ee` |

## Infrastructure

- PostgreSQL 17: `localhost:5433` ✅
- Redis 7: `localhost:6380` ✅
- NATS JetStream 2: `localhost:4222` ✅
- Proto codegen (buf): 16 Go files from 9 protos ✅
- Database migrations: 20 application tables (18 + refresh_tokens) ✅

## Test Summary

| Layer | Total Tests | Pass | Fail |
|---|---|---|---|
| Layer 1 | 60 | 60 | 0 |
| Layer 2 | 70 | 70 | 0 |
| Layer 3 | 29 | 29 | 0 |
| Layer 4 | 23 | 23 | 0 |
| **Total** | **182** | **182** | **0** |

*Full test run: 68 top-level tests, 208 subtests, 276 total assertions — all pass.*

## Service Ports (Local Dev)

| Service | gRPC Port | Notes |
|---|---|---|
| hermes-proxy | 9101 | |
| hermes-contacts | 9102 | |
| hermes-notify | 9103 | |
| hermes-wa | 9104 | |
| hermes-campaign | 9105 | |
| hermes-inbox | 9106 | |
| hermes-gateway | 8080 | gRPC API |
| hermes-gateway (WS) | 8081 | WebSocket hub |
| hermes-web | 5173 | Vite dev server |

## Frontend Pages

| Page | Route | Description |
|---|---|---|
| Login | `/login` | Email + password auth |
| Dashboard | `/` | Workspace stats overview |
| Numbers | `/numbers` | WA number management + QR linking |
| Proxies | `/proxies` | Proxy pool management |
| Contacts | `/contacts` | Contact list + CSV import |
| Templates | `/templates` | Message template editor (spintax) |
| Campaign List | `/campaigns` | Campaign overview + status |
| Campaign Create | `/campaigns/new` | Campaign creation wizard |
| Campaign Detail | `/campaigns/:id` | Campaign progress + controls |
| Inbox | `/inbox` | Agent conversation view |
| Settings | `/settings` | Notification config |

## What's Deferred to Phase 2

- Production Dockerfiles (multi-stage builds)
- Kubernetes manifests (StatefulSet for WA pods)
- Prometheus + Grafana observability
- whatsmeow session persistence (SQLite → PostgreSQL)
- Number rotation engine (anti-ban layer 4)
- Media message support (images, documents)
- Contact group management
- Advanced campaign scheduling (time-zone aware)
- Rate limiting middleware
- Audit logging
