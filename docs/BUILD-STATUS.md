# Hermès — Build Status

## Phase 1 MVP Progress

| Layer | Service | RPCs | Tests | Status | Commit |
|---|---|---|---|---|---|
| **0** | Scaffolding (go.mod, proto, docker, migrations) | — | — | ✅ Done | `bab8ee5` |
| **1** | hermes-proxy | 11/11 | 17 pass | ✅ Done | `5456f9e` |
| **1** | hermes-contacts | 11/11 | 17 pass | ✅ Done | `5456f9e` |
| **1** | hermes-notify | 6/6 | 26 pass | ✅ Done | `5456f9e` |
| **2** | hermes-wa | 8/8 | 28 pass | ✅ Done | `cb8f9e9` |
| **2** | hermes-campaign | 17/17 | 42 pass | ✅ Done | `cb8f9e9` |
| **3** | hermes-inbox | 0/12 | — | ⬜ Pending | — |
| **4** | hermes-gateway | 0/75 | — | ⬜ Pending | — |
| **4** | hermes-web | — | — | ⬜ Pending | — |

## Infrastructure

- PostgreSQL 17: `localhost:5433` ✅
- Redis 7: `localhost:6380` ✅
- NATS JetStream 2: `localhost:4222` ✅
- Proto codegen (buf): 16 Go files from 9 protos ✅
- Database migrations: 18 application tables ✅

## Test Summary

| Layer | Total Tests | Pass | Fail |
|---|---|---|---|
| Layer 1 | 60 | 60 | 0 |
| Layer 2 | 70 | 70 | 0 |
| **Total** | **130** | **130** | **0** |

## Service Ports (Local Dev)

| Service | Port |
|---|---|
| hermes-proxy | 9101 |
| hermes-contacts | 9102 |
| hermes-notify | 9103 |
| hermes-wa | 9104 |
| hermes-campaign | 9105 |
| hermes-inbox | 9106 |
| hermes-gateway | 8080 |
| hermes-web | 5173 |
