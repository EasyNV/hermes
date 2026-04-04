# Hermès — Build Status

## Phase 1 MVP: ✅ COMPLETE + STABILIZED (2026-04-04)

22 commits. All services operational end-to-end.

| Layer | Service | RPCs | Tests | Status | Commit |
|---|---|---|---|---|---|
| **0** | Scaffolding | — | — | ✅ Done | `bab8ee5` |
| **1** | hermes-proxy | 11 | 17 | ✅ Done | `5456f9e` |
| **1** | hermes-contacts | 11 | 17 | ✅ Done | `5456f9e` |
| **1** | hermes-notify | 6 | 26 | ✅ Done | `5456f9e` |
| **2** | hermes-wa | 8 | 28 | ✅ Done | `cb8f9e9` |
| **2** | hermes-campaign | 17 | 42 | ✅ Done | `cb8f9e9` |
| **3** | hermes-inbox | 14 | 29 | ✅ Done | `acea288` |
| **4** | hermes-gateway | 75 | 23 | ✅ Done | `f5617ee` |
| **4** | hermes-web | 11 pages | TS strict | ✅ Done | `f5617ee` |
| **—** | REST adapter | 76 routes | — | ✅ Done | `8b0e611` |
| **—** | Stabilization | 15 bugs | — | ✅ Done | `3f829b5` |

## Test Summary

| Metric | Count |
|---|---|
| Test packages | 12 |
| Top-level tests | 68 |
| Subtests | 208 |
| Total assertions | 276 |
| Failures | 0 |

## Codebase

| Language | Lines |
|---|---|
| Go | 21,309 |
| TypeScript/TSX | 7,060 |
| Proto | 7,410 |
| SQL migrations | 263 |
| **Total** | **~36K** |

## Verified End-to-End Flows

- ✅ Login → JWT → RBAC → all 76 REST endpoints
- ✅ Register WA number → QR pairing → phone pairing
- ✅ Device identity: "MacOS" in Linked Devices
- ✅ Create campaign → assign contacts + numbers → start → dispatch → delivered
- ✅ Inbound message → NATS → inbox → DB → WebSocket → frontend
- ✅ Inbox reply → NATS → WA service → whatsmeow → delivered to phone
- ✅ Canned responses with newlines preserved
- ✅ All 12 Docker containers healthy

## What's Deferred to Phase 2

- Production Dockerfiles (multi-stage builds)
- Kubernetes manifests (StatefulSet for WA pods)
- Prometheus + Grafana observability
- whatsmeow session persistence (SQLite → PostgreSQL migration)
- Number rotation engine (anti-ban layer 4)
- Media message support (images, documents)
- Contact group management
- Advanced campaign scheduling (time-zone aware)
- Rate limiting middleware
- Audit logging
- Campaign-only inbox filter (Settings toggle: All / Campaign Only / Direct Only, per-workspace)
- Contact name randomization (Settings toggle for privacy/demo: hash-based pseudonyms, phone masking)
