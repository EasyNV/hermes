# Hermès — Architecture Research (Gate 1)

**Date:** 2026-04-03
**Status:** ✅ APPROVED — proceed to Gate 2 (contracts).

---

## Table of Contents

1. [WhatsApp Library Comparison](#1-whatsapp-library-comparison)
2. [Message Broker Recommendation](#2-message-broker-recommendation)
3. [Frontend Framework Recommendation](#3-frontend-framework-recommendation)
4. [Database Schema (High-Level ERD)](#4-database-schema-high-level-erd)
5. [Microservice Boundaries & Communication](#5-microservice-boundaries--communication)
6. [30K Session Sharding Strategy](#6-30k-session-sharding-strategy)
7. [Anti-Ban Architecture](#7-anti-ban-architecture)
8. [Observability Strategy](#8-observability-strategy)
9. [Open Questions](#9-open-questions)

---

## 1. WhatsApp Library Comparison

### Summary Matrix

| Criteria | whatsmeow (Go) | Baileys (TS/Node) | Zapo (TS/Node) | whatsapp-rust (Rust) |
|---|---|---|---|---|
| **Language** | Go | TypeScript | TypeScript | Rust |
| **Stars / Maturity** | ~5.5K ⭐, 700+ forks, active since 2021 | ~4K+ ⭐ (WhiskeySockets fork), huge community | New (<500 ⭐), independent impl | ~330 downloads/mo on crates.io, newer |
| **Maintainer** | tulir (mautrix/Matrix ecosystem) | Rajeh (WhiskeySockets) | vinikjkkj (solo) | jlucaso1 (solo) |
| **Multi-device** | ✅ Full | ✅ Full | ✅ Full | ✅ Full |
| **Send typing indicator** | ✅ `SendChatPresence(ctx, jid, ChatPresenceComposing)` | ✅ `sendPresenceUpdate('composing', jid)` | ✅ (coordinator-based) | ✅ Presence & chat state |
| **Receive typing indicator** | ✅ ChatPresence events (must subscribe) | ✅ Events | ✅ Events | ✅ Events |
| **Proxy support** | ✅ Native `SetSOCKSProxy()` + `SetHTTPProxy()` | ⚠️ Manual (pass agent to WebSocket) | ⚠️ Likely manual (transport layer) | ⚠️ Custom transport factory |
| **Session persistence** | ✅ SQL store (SQLite/Postgres) | ✅ File/custom auth store | ✅ SQLite store | ✅ SQLite store |
| **Reconnect logic** | ✅ Built-in auto-reconnect | ✅ Built-in | ✅ Built-in | ✅ Built-in |
| **Media (images/files)** | ✅ Upload/download with encryption | ✅ Full | ✅ Full | ✅ Full |
| **Memory per session** | ~2-5 MB (Go, no browser) | ~10-30 MB (Node.js heap overhead) | ~8-20 MB (optimized Node, zero-copy paths) | ~1-3 MB (Rust, minimal overhead) |
| **Concurrency model** | Go goroutines (lightweight) | Node event loop (single-threaded per worker) | Node event loop | Tokio async tasks |
| **Production battle-tested** | ✅ Powers mautrix-whatsapp bridge (thousands of users) | ✅ Widely used in WA bots ecosystem | ❌ Too new, no production reports | ❌ Early, "stable enough" per author |
| **Breaking changes** | Rare, stable API | Frequent (v7.0 had major breaking changes) | Expected ("frequent breaking changes until v1") | Pre-1.0, expect changes |
| **License** | MPL-2.0 | MIT | MIT | MIT |

### Deep Analysis

#### whatsmeow (Go) — **RECOMMENDED** ✅

**Strengths:**
- **Go-native** — matches our backend stack. No FFI, no sidecar process, no language boundary.
- **Battle-tested at scale** via mautrix-whatsapp (Matrix bridge handling thousands of concurrent WA sessions).
- **Native SOCKS5 + HTTP proxy support** — `client.SetSOCKSProxy(addr, auth)` and `client.SetHTTPProxy(url)`. This is critical for sticky proxy assignment per session.
- **Low memory footprint** — Go's goroutine model means ~2-5 MB per session including crypto state and WebSocket buffers. At 30K sessions: ~60-150 GB across the cluster (manageable with horizontal scaling).
- **PostgreSQL session store** — native support via `store/sqlstore`, fits our DB stack without adapters.
- **Active maintainer** (tulir) — 5.5K stars, regular commits through 2026, responsive on discussions.
- **Typing indicators** — `SendChatPresence(ctx, jid, ChatPresenceComposing)` for outbound; subscribe to `ChatPresence` events for inbound.
- **Read receipts, delivery receipts** — all supported.

**Weaknesses:**
- Limited ecosystem compared to Baileys (fewer community plugins/examples).
- No official "multi-session manager" — we build our own session orchestrator (which we need to do anyway for sharding).
- Group management events can be inconsistent in edge cases (per community reports).

**Risk:** Low. This is the most production-proven Go library for WA.

#### Baileys (TypeScript/Node.js)

**Strengths:**
- Largest community, most examples/tutorials, extensive feature set.
- Wiki documentation (baileys.wiki).
- Enterprise support available from maintainer.

**Weaknesses:**
- **Node.js** — doesn't match our Go backend. Would require either: (a) rewriting the entire backend in TS, or (b) running Baileys as a sidecar process communicating via gRPC/REST, adding latency and complexity.
- **Higher memory per session** (~10-30 MB in Node). At 30K sessions: 300-900 GB — significantly more infrastructure.
- **Frequent breaking changes** — v7.0 was a major rewrite. Unstable API surface for a long-lived platform.
- **Proxy support** — not native. Requires manually injecting a proxy agent into the WebSocket connection options. Doable but more fragile.
- **Single-threaded event loop** — Node worker threads help but Go goroutines are fundamentally more efficient for I/O-bound concurrent workloads like managing thousands of WebSocket connections.

**Verdict:** Strong library, wrong language for our stack. The sidecar approach adds too much operational complexity for the benefit.

#### Zapo (TypeScript/Node.js)

**Strengths:**
- Independent protocol implementation (not a fork). Claims performance-first design with zero-copy hot paths, Uint8Array over Buffer.
- Designed for multi-session scale.
- SQLite-backed stores.

**Weaknesses:**
- **Too new** — "frequent breaking changes expected until first major release." No production reports.
- **Solo maintainer** — bus factor of 1, no community.
- **Node.js** — same language mismatch as Baileys.
- **No stars/community** — search returned zero results for benchmarks or production usage.
- Zero-copy optimizations are nice but don't overcome the fundamental Node.js memory overhead at 30K sessions.

**Verdict:** Interesting technically but too immature and wrong language. Would not bet a production platform on this.

#### whatsapp-rust (Rust)

**Strengths:**
- Lowest memory per session (~1-3 MB). At 30K sessions: ~30-90 GB across the cluster.
- Modular architecture (pluggable storage, transport, HTTP client, async runtime).
- Active development, published on crates.io (334 downloads/mo).

**Weaknesses:**
- **Pre-1.0** — author says "stable enough for implemented features" but that's not production-grade confidence.
- **Rust in a Go ecosystem** — would require FFI bindings (cgo + Rust) or running as a sidecar, adding operational complexity.
- **Solo maintainer** (jlucaso1), small community.
- **Proxy support** — not natively documented. Would require custom transport factory implementation.
- **No production reports** at scale.

**Verdict:** Best theoretical performance, but too immature and Rust/Go interop adds complexity. Keep on radar for Phase 3 if whatsmeow hits scaling ceilings.

### Recommendation

**Use whatsmeow.** It's the only option that is:
1. Written in Go (our backend language)
2. Battle-tested at scale (mautrix-whatsapp)
3. Has native proxy support (SOCKS5 + HTTP)
4. Has native typing indicator support
5. Has PostgreSQL session persistence
6. Has an active, responsive maintainer

If whatsmeow hits performance ceilings at 30K sessions, the fallback path is either:
- Contributing patches upstream (tulir is responsive)
- Building a thin Rust sidecar using whatsapp-rust for the hot path (Phase 3 option)

---

## 2. Message Broker Recommendation

### Comparison

| Criteria | NATS JetStream | Redpanda | RabbitMQ |
|---|---|---|---|
| **Protocol** | Custom (NATS protocol) | Kafka-compatible | AMQP 0.9.1 |
| **Language** | Go | C++ | Erlang |
| **Persistence** | ✅ JetStream (file/memory-backed) | ✅ Log-based (like Kafka) | ✅ Queue-based |
| **Go client** | ✅ Official nats.go (excellent) | ✅ Kafka Go clients (franz-go, confluent-kafka-go) | ✅ amqp091-go |
| **Operational complexity** | Low — single binary, no JVM, no ZooKeeper | Medium — single binary but heavier | Medium — Erlang runtime, clustering config |
| **Message ordering** | ✅ Per-stream | ✅ Per-partition | ✅ Per-queue |
| **Consumer groups** | ✅ JetStream consumers | ✅ Kafka consumer groups | ✅ Competing consumers |
| **At-least-once delivery** | ✅ | ✅ | ✅ |
| **Exactly-once** | ✅ (dedupe window) | ✅ (idempotent producers) | ❌ (at-most-once or at-least-once) |
| **Throughput** | ~15M+ msg/s (single node) | ~1M+ msg/s (single node) | ~50K msg/s (single node) |
| **Latency** | Sub-millisecond | Low milliseconds | Low milliseconds |
| **Memory footprint** | ~50 MB base | ~500 MB base | ~150 MB base |
| **K8s integration** | Excellent (Helm chart, StatefulSet) | Good (Helm chart) | Good (operator) |
| **Request-reply pattern** | ✅ Native (core feature) | ❌ Not native (pub-sub only) | ✅ Via RPC pattern |
| **Wildcard subjects** | ✅ `campaign.>`, `wa.*.events` | ❌ Topic-based only | ✅ Topic exchange routing |

### Our Use Cases

1. **Campaign dispatch** — campaign service publishes send tasks → WA service consumes and sends. Needs ordering (per-campaign), persistence (don't lose tasks on restart), back-pressure (throttling).
2. **Event routing** — WA service publishes incoming messages → inbox service, campaign service (for reply tracking), notify service. Fan-out pattern.
3. **Inbox notifications** — low-latency pub-sub to push new messages to WebSocket hub.
4. **Ban events** — WA service detects ban → campaign service pauses, proxy service flags. Event-driven.
5. **Request-reply** — gateway needs sync responses from services occasionally (e.g., "get campaign status").

### Recommendation: **NATS JetStream**

**Why NATS over the others:**

1. **Go-native ecosystem** — NATS server is written in Go, the Go client (`nats.go`) is first-class. The API fits Go idioms perfectly (context-aware, no Java-isms).

2. **Operational simplicity** — single ~20 MB binary. No JVM, no Erlang, no ZooKeeper. On K8s, it's a StatefulSet with 3 nodes for HA. Compare to Redpanda which needs more memory and Kafka protocol overhead we don't need.

3. **JetStream for persistence** — covers campaign dispatch (persistent streams with consumers), at-least-once delivery with ack, replay capability. Message deduplication via `Nats-Msg-Id` header.

4. **Native request-reply** — NATS has built-in request-reply. Gateway can `nats.Request("campaign.status.{id}", payload, timeout)` and get a synchronous response. Redpanda cannot do this natively.

5. **Wildcard subjects** — `wa.{tenant_id}.{number_id}.events` allows fine-grained topic routing without creating thousands of Kafka topics. This is perfect for per-number event streams.

6. **Throughput is more than enough** — even at 30K sessions sending 200 msgs/day each = 6M msgs/day = ~70 msg/s sustained. NATS handles 15M+/s. We're orders of magnitude below the ceiling.

7. **Low memory** — ~50 MB base + stream storage. With Redis already in the stack for caching, NATS adds minimal infra overhead.

**Why not Redpanda:**
- Kafka protocol compatibility is irrelevant — we have no existing Kafka ecosystem.
- Higher memory footprint (~500 MB base) for a capability we don't need.
- No native request-reply pattern.
- Topic management overhead (would need per-tenant topics vs NATS subject hierarchy).

**Why not RabbitMQ:**
- Erlang runtime adds operational complexity.
- Lower throughput ceiling (though sufficient for our scale).
- No built-in stream replay (limited to queue semantics).
- More complex clustering and split-brain handling.

---

## 3. Frontend Framework Recommendation

### Comparison

| Criteria | Vite + React (SPA) | Next.js |
|---|---|---|
| **Rendering** | Client-side only | SSR + SSG + CSR |
| **WebSocket handling** | Native, straightforward | Requires careful SSR/CSR boundary handling |
| **Build speed** | Fast (Vite HMR) | Good but heavier (webpack/turbopack) |
| **Deployment** | Static files → CDN or nginx | Node.js server required |
| **Complexity** | Simple — one build artifact | More complex — server + client builds |
| **SEO** | Not needed (internal tool, behind auth) | Overkill for internal app |
| **Bundle size** | Smaller (no SSR framework overhead) | Larger |
| **Real-time dashboard** | Excellent (pure client-side state) | Works but SSR adds no value for real-time |

### Recommendation: **Vite + React (SPA)**

This is an internal tool behind auth with 20-30 users. There's zero SEO requirement. The entire UI is a real-time dashboard with WebSocket connections — SSR adds complexity without benefit.

**Stack details:**
- **Vite** — build tool (fast HMR, ESBuild-based)
- **React 19** — UI framework
- **TanStack Query** — server state management (API calls)
- **Zustand** — client state management (lightweight, no boilerplate)
- **TanStack Router** — type-safe routing
- **Tailwind CSS** — styling
- **shadcn/ui** — component library (based on Radix primitives)
- **native WebSocket** — for real-time inbox/dashboard updates (no Socket.IO overhead needed for 20-30 users)

**Build output:** Static files served by nginx or the API gateway. No Node.js server in production for the frontend.

---

## 4. Database Schema (High-Level ERD)

### Approach: Shared Database, Separate Schemas

At our scale (20-30 users, not thousands of tenants), a single PostgreSQL instance with logical separation by tenant_id foreign keys is simpler and more maintainable than per-service databases. Services own their tables but share the same PostgreSQL cluster.

Redis is used for: session state cache, campaign progress counters, rate limiting, pub-sub fanout to WebSocket hub.

```
┌─────────────────────────────────────────────────────────────────────┐
│ RBAC & AUTH │
├─────────────────────────────────────────────────────────────────────┤
│ │
│ tenants workspaces users │
│ ┌──────────────┐ ┌──────────────┐ ┌──────────────────┐ │
│ │ id (PK) │──┐ │ id (PK) │──┐ │ id (PK) │ │
│ │ name │ └──│ tenant_id(FK)│ ├──│ workspace_id(FK) │ │
│ │ created_at │ │ name │ │ │ email │ │
│ │ settings JSONB│ │ settings │ │ │ password_hash │ │
│ └──────────────┘ │ daily_cap │ │ │ role (enum) │ │
│ │ created_at │ │ │ created_at │ │
│ └──────────────┘ │ └──────────────────┘ │
│ │ │
│ roles: superadmin | tenant_admin │ │
│ workspace_admin | cs_agent │ │
│ │ │
│ workspace_members │ │
│ ┌──────────────────────┐ │ │
│ │ user_id (FK) │◄─┘ │
│ │ workspace_id (FK) │ │
│ │ role (enum) │ │
│ └──────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│ WHATSAPP NUMBERS & PROXIES │
├─────────────────────────────────────────────────────────────────────┤
│ │
│ wa_numbers proxies │
│ ┌───────────────────┐ ┌──────────────────┐ │
│ │ id (PK) │ │ id (PK) │ │
│ │ tenant_id (FK) │ │ tenant_id (FK) │ │
│ │ jid │ │ host │ │
│ │ phone │ │ port │ │
│ │ display_name │ │ username │ │
│ │ status (enum) │ │ password │ │
│ │ proxy_id (FK) │──────│ type (enum) │ │
│ │ session_data BYTEA │ │ status (enum) │ │
│ │ health_score │ │ ban_count │ │
│ │ daily_sent_count │ │ assigned_count │ │
│ │ total_sent │ │ last_health_check │ │
│ │ ban_count │ │ created_at │ │
│ │ last_ban_at │ └──────────────────┘ │
│ │ connected_at │ │
│ │ pod_id (shard key) │ proxy types: socks5 | http │
│ │ created_at │ proxy status: active | dead | flagged │
│ └───────────────────┘ │
│ │
│ wa_number_workspaces (M2M) │
│ ┌───────────────────┐ │
│ │ wa_number_id (FK) │ │
│ │ workspace_id (FK) │ │
│ └───────────────────┘ │
│ │
│ number status: active | banned | disconnected | cooldown │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│ CONTACTS │
├─────────────────────────────────────────────────────────────────────┤
│ │
│ contacts contact_tags │
│ ┌──────────────────┐ ┌───────────────┐ │
│ │ id (PK) │──────│ contact_id(FK)│ │
│ │ tenant_id (FK) │ │ tag (text) │ │
│ │ phone (unique/t) │ └───────────────┘ │
│ │ name │ │
│ │ custom_fields │ contact_custom_fields │
│ │ is_banned │ ┌───────────────────┐ │
│ │ created_at │──────│ contact_id (FK) │ │
│ │ updated_at │ │ key (text) │ │
│ └──────────────────┘ │ value (text) │ │
│ └───────────────────┘ │
│ │
│ Unique constraint: (tenant_id, phone) │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│ TEMPLATES & CAMPAIGNS │
├─────────────────────────────────────────────────────────────────────┤
│ │
│ templates │
│ ┌───────────────────────┐ │
│ │ id (PK) │ │
│ │ workspace_id (FK) │ │
│ │ name │ │
│ │ body (text, spintax) │ │
│ │ media_url │ │
│ │ media_type │ │
│ │ variables JSONB │ │
│ │ created_by (FK) │ │
│ │ created_at │ │
│ └───────────────────────┘ │
│ │
│ campaigns campaign_numbers (M2M) │
│ ┌───────────────────────┐ ┌───────────────────┐ │
│ │ id (PK) │──────│ campaign_id (FK) │ │
│ │ workspace_id (FK) │ │ wa_number_id(FK) │ │
│ │ template_id (FK) │ │ status (enum) │ │
│ │ name │ │ sent_count │ │
│ │ status (enum) │ │ failed_count │ │
│ │ schedule_at │ └───────────────────┘ │
│ │ daily_cap_per_num │ │
│ │ ban_pause_threshold │ │
│ │ rotation_strategy │ campaign_contacts │
│ │ delay_min_ms │ ┌───────────────────┐ │
│ │ delay_max_ms │ │ campaign_id (FK) │ │
│ │ total_contacts │ │ contact_id (FK) │ │
│ │ sent_count │ │ wa_number_id (FK) │ │
│ │ failed_count │ │ status (enum) │ │
│ │ replied_count │ │ sent_at │ │
│ │ banned_count │ │ delivered_at │ │
│ │ created_by (FK) │ │ failed_at │ │
│ │ created_at │ │ error │ │
│ │ started_at │ └───────────────────┘ │
│ │ completed_at │ │
│ └───────────────────────┘ │
│ │
│ campaign status: draft | scheduled | running | paused | │
│ completed | cancelled │
│ contact status: pending | sent | delivered | failed | skipped │
│ rotation: round_robin | least_used │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│ INBOX & MESSAGES │
├─────────────────────────────────────────────────────────────────────┤
│ │
│ conversations │
│ ┌───────────────────────┐ │
│ │ id (PK) │ │
│ │ workspace_id (FK) │ │
│ │ contact_id (FK) │ │
│ │ wa_number_id (FK) │ │
│ │ assigned_to (FK user) │ │
│ │ status (enum) │ │
│ │ last_message_at │ │
│ │ campaign_id (FK, null)│ │
│ │ created_at │ │
│ └───────────────────────┘ │
│ │
│ messages │
│ ┌───────────────────────┐ │
│ │ id (PK) │ │
│ │ conversation_id (FK) │ │
│ │ direction (enum) │ │
│ │ content_type (enum) │ │
│ │ body (text, nullable) │ │
│ │ media_url (nullable) │ │
│ │ template_id (FK, null)│ For campaign msgs: store template ref │
│ │ resolved_vars JSONB │ + resolved variables (not full text) │
│ │ wa_message_id │ │
│ │ status (enum) │ │
│ │ created_at │ │
│ └───────────────────────┘ │
│ │
│ conversation status: unassigned | assigned | closed │
│ message direction: inbound | outbound │
│ message status: pending | sent | delivered | read | failed │
│ content type: text | image | document | audio | video │
│ │
│ canned_responses │
│ ┌───────────────────────┐ │
│ │ id (PK) │ │
│ │ workspace_id (FK) │ │
│ │ shortcut (text) │ e.g., "/greeting" │
│ │ body (text) │ │
│ │ created_by (FK) │ │
│ └───────────────────────┘ │
│ │
│ INDEX: messages(conversation_id, created_at) │
│ INDEX: messages(created_at) — for retention purge │
│ FTS: messages(body) — using pg tsvector or pg_trgm │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│ NOTIFICATIONS │
├─────────────────────────────────────────────────────────────────────┤
│ │
│ notification_configs │
│ ┌───────────────────────┐ │
│ │ id (PK) │ │
│ │ workspace_id (FK) │ │
│ │ type (enum) │ browser_push | sound | webhook │
│ │ webhook_url │ │
│ │ webhook_type │ telegram | discord | custom │
│ │ enabled │ │
│ └───────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

### Key Schema Decisions

1. **Tenant isolation via FK** — every row has `tenant_id`. Row-level security (RLS) in PostgreSQL can enforce this at the DB level, but we'll also enforce at the application layer.

2. **WA numbers are tenant-scoped, workspace-assigned** — the M2M `wa_number_workspaces` table allows one number to be shared across workspaces within a tenant.

3. **Campaign messages store template_id + resolved_vars** (not the full resolved body) — saves significant storage at scale. The full text can be reconstructed on read by applying vars to the template.

4. **Message retention** — index on `messages(created_at)` enables efficient batch deletion via `DELETE FROM messages WHERE created_at < NOW() - INTERVAL '3 months'`. Run via pg_cron or a scheduled job.

5. **Full-text search** — `pg_trgm` extension with GIN index on `messages(body)` for fuzzy search. For higher volume, could migrate to a dedicated search index (Meilisearch/Typesense) in Phase 2.

6. **Session data** — `wa_numbers.session_data` stores the whatsmeow session as BYTEA. This is the encryption keys + device identity needed for reconnection. Alternative: use whatsmeow's built-in `sqlstore` with PostgreSQL, which manages its own tables. **Recommendation: use whatsmeow's native PostgreSQL store** — it handles the complex multi-table session state automatically.

7. **Pod assignment** — `wa_numbers.pod_id` tracks which hermes-wa pod owns this session. Used for shard routing and session migration on pod failure.

---

## 5. Microservice Boundaries & Communication

### Service Interaction Map

```
                            SYNC (REST/gRPC)
                     ┌─────────────────────────┐
                     │                         │
                     ▼                         │
┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
│ hermes-  │──▶│ hermes-  │──▶│ hermes-  │──▶│ hermes-  │
│ web      │   │ gateway  │   │ wa       │   │ proxy    │
│ (React)  │◀──│ (API+WS) │   │ (sessions│   │ (pool)   │
└──────────┘   └──────────┘   │  +send)  │   └──────────┘
                     │         └──────────┘
                     │              │
                     │    ASYNC (NATS JetStream)
                     │              │
               ┌─────┴─────┐       │
               │           │       │
          ┌──────────┐ ┌──────────┐│  ┌──────────┐  ┌──────────┐
          │ hermes-  │ │ hermes-  ││  │ hermes-  │  │ hermes-  │
          │ inbox    │ │ campaign ││  │ contacts │  │ notify   │
          │ (convos) │ │ (engine) │◀──│ (import) │  │ (alerts) │
          └──────────┘ └──────────┘   └──────────┘  └──────────┘
```

### Communication Pattern Per Interaction

| From → To | Pattern | Channel | Why |
|---|---|---|---|
| **web → gateway** | REST + WebSocket | HTTP/WS | Frontend API calls + real-time updates |
| **gateway → any service** | REST (gRPC Phase 2) | HTTP | Gateway proxies API requests. REST for simplicity in Phase 1, migrate hot paths to gRPC in Phase 2 for type safety. |
| **campaign → wa** | Async (NATS) | `campaign.send.{tenant_id}` | Campaign publishes send tasks. WA service consumes at its own rate (back-pressure). Persistent stream. |
| **wa → inbox** | Async (NATS) | `wa.message.inbound.{tenant_id}` | Incoming messages fan out to inbox service. |
| **wa → campaign** | Async (NATS) | `wa.message.inbound.{tenant_id}` | Same stream — campaign service tracks replies to active campaigns. |
| **wa → notify** | Async (NATS) | `wa.message.inbound.{tenant_id}` | Notify service triggers browser push / webhook on new unassigned message. |
| **wa → proxy** | Async (NATS) | `wa.ban.{tenant_id}` | Ban detection → proxy service flags the associated proxy. |
| **wa → campaign** | Async (NATS) | `wa.ban.{tenant_id}` | Ban detection → campaign service checks if auto-pause threshold reached. |
| **campaign → wa** | Async (NATS) | `wa.ban.redistribute.{tenant_id}` | Campaign redistributes banned number's remaining contacts. |
| **inbox → wa** | Async (NATS) | `wa.send.manual.{tenant_id}` | Agent sends a manual reply → WA service sends it. |
| **gateway → wa** | REST | HTTP | QR login, session status checks (synchronous). |
| **contacts → campaign** | Async (NATS) | `contacts.import.done.{tenant_id}` | Contact import completion event. |

### NATS Subject Hierarchy

```
hermes.
├── wa.
│   ├── message.
│   │   ├── inbound.{tenant_id}     # Incoming WA messages
│   │   └── outbound.{tenant_id}    # Outbound message status updates
│   ├── send.
│   │   ├── campaign.{tenant_id}    # Campaign send tasks (from campaign svc)
│   │   └── manual.{tenant_id}      # Manual agent replies (from inbox svc)
│   ├── ban.{tenant_id}             # Number ban events
│   ├── connection.{tenant_id}      # Number connect/disconnect events
│   └── presence.{tenant_id}        # Typing indicators (Phase 3)
├── campaign.
│   ├── status.{tenant_id}          # Campaign state changes
│   └── progress.{tenant_id}        # Real-time progress updates
├── contacts.
│   └── import.done.{tenant_id}     # Import completion
└── notify.
    └── dispatch.{tenant_id}        # Notification dispatch requests
```

### Service Ownership

| Service | Owns Tables | Publishes Events | Consumes Events |
|---|---|---|---|
| **hermes-gateway** | — (stateless) | — | — |
| **hermes-wa** | wa_numbers, wa_sessions* | wa.message.inbound, wa.message.outbound, wa.ban, wa.connection | wa.send.campaign, wa.send.manual |
| **hermes-campaign** | campaigns, campaign_numbers, campaign_contacts, templates | campaign.status, campaign.progress, wa.send.campaign | wa.message.inbound (reply tracking), wa.ban |
| **hermes-inbox** | conversations, messages, canned_responses | wa.send.manual, notify.dispatch | wa.message.inbound |
| **hermes-contacts** | contacts, contact_tags, contact_custom_fields | contacts.import.done | — |
| **hermes-proxy** | proxies | — | wa.ban |
| **hermes-notify** | notification_configs | — | notify.dispatch |

*wa_sessions managed by whatsmeow's native `sqlstore`

---

## 6. 30K Session Sharding Strategy

### The Problem

Each WA session is a persistent WebSocket connection to WhatsApp's servers. One Go process can handle hundreds to low thousands of concurrent WebSocket connections before CPU/memory becomes a bottleneck. We need to distribute 30K sessions across multiple pods.

### Memory Budget

Per whatsmeow session (estimated):
- WebSocket connection: ~15 KB
- Signal Protocol crypto state: ~500 KB
- Message buffers: ~500 KB
- Go runtime overhead per goroutine: ~8 KB (2-3 goroutines per session)
- **Total: ~1-2 MB per session**

At 30K sessions:
- **Total memory: 30-60 GB** across the cluster
- **Per pod (500 sessions/pod): ~0.5-1 GB RAM**
- **Pod count: 60 pods** for 30K sessions (with 500/pod)
- Buffer to 100 pods for headroom (300 sessions/pod)

### Architecture

```
┌─────────────────────────────────────────────────────┐
│ hermes-wa-controller (Deployment, 1 replica)        │
│ ┌─────────────────────────────────────────────────┐ │
│ │ Session Registry (PostgreSQL-backed)            │ │
│ │ - Owns the mapping: wa_number_id → pod_id       │ │
│ │ - Assigns new sessions to least-loaded pod      │ │
│ │ - Detects pod failure → reassigns sessions      │ │
│ │ - Exposes REST API for session routing           │ │
│ └─────────────────────────────────────────────────┘ │
└──────────────────────┬──────────────────────────────┘
                       │ REST (assign/revoke)
        ┌──────────────┼──────────────────────┐
        ▼              ▼                      ▼
┌──────────────┐┌──────────────┐    ┌──────────────┐
│ hermes-wa-0  ││ hermes-wa-1  │... │ hermes-wa-N  │
│ (StatefulSet)││ (StatefulSet)│    │ (StatefulSet)│
│              ││              │    │              │
│ Sessions:    ││ Sessions:    │    │ Sessions:    │
│ 001-500      ││ 501-1000     │    │ ...          │
│              ││              │    │              │
│ NATS consumer││ NATS consumer│    │ NATS consumer│
│ (filtered by ││ (filtered by │    │ (filtered by │
│  pod_id)     ││  pod_id)     │    │  pod_id)     │
└──────────────┘└──────────────┘    └──────────────┘
```

### Sharding Approach: **Controller + StatefulSet**

1. **hermes-wa pods** run as a **StatefulSet** (not Deployment). Each pod has a stable hostname (`hermes-wa-0`, `hermes-wa-1`, ...) and stable storage for session recovery.

2. **Session assignment** is stored in PostgreSQL: `wa_numbers.pod_id = "hermes-wa-{N}"`. The controller assigns sessions using least-loaded strategy.

3. **Send routing** — when campaign service needs to send a message, it publishes to NATS with the `wa_number_id`. The target pod's NATS consumer filters by its assigned numbers. Alternatively, the gateway routes sends to the correct pod via the controller's registry.

4. **Pod failure handling:**
   - Controller detects pod is down (health check failure / K8s events)
   - Marks all sessions on that pod as `needs_reassign`
   - Redistributes to surviving pods
   - Surviving pods load the session state from PostgreSQL (whatsmeow `sqlstore`) and reconnect to WA
   - Reconnection is automatic — whatsmeow re-establishes the WebSocket using persisted session keys

5. **Scaling up:**
   - Increase StatefulSet replicas
   - Controller detects new pods
   - Gradually migrates sessions from overloaded pods to new ones (graceful rebalancing)
   - Migration: old pod disconnects session → controller reassigns → new pod reconnects

6. **Scaling down:**
   - Cordon the target pod (stop accepting new sessions)
   - Migrate all sessions to other pods
   - Then scale down

### Why StatefulSet over Deployment

- **Stable pod identity** — `hermes-wa-0` always refers to the same logical shard. Simplifies logging, debugging, dashboards.
- **Stable storage** — PVCs survive pod restarts. Can cache session state locally for faster reconnect.
- **Ordered scaling** — scale down removes the highest-numbered pod, making migration predictable.
- **Not needed for session state persistence** — that's in PostgreSQL. But stable identity helps with shard-aware NATS consumers and operational clarity.

### Session Routing for Sends

Two options:

**Option A: NATS subject-based routing (recommended)**
```
# Campaign sends to a specific number
Subject: hermes.wa.send.campaign.{tenant_id}
Header: X-WA-Number-ID: 12345
# Each wa pod has a NATS consumer with a filter: only process messages for numbers assigned to this pod
```

**Option B: Direct pod-to-pod routing via controller**
```
# Gateway asks controller: "which pod owns number 12345?"
# Controller responds: "hermes-wa-3"
# Gateway forwards send request directly to hermes-wa-3 via K8s service
```

Recommendation: **Option A** for campaign sends (async, better for back-pressure), **Option B** for QR login/status checks (synchronous, low-frequency).

### Capacity Planning

| Scale | Sessions/Pod | Pod Count | RAM/Pod | Total RAM | NATS Streams |
|---|---|---|---|---|---|
| 1K | 200 | 5 | 0.4 GB | 2 GB | 5 |
| 5K | 300 | 17 | 0.6 GB | 10 GB | 17 |
| 10K | 400 | 25 | 0.8 GB | 20 GB | 25 |
| 30K | 500 | 60 | 1.0 GB | 60 GB | 60 |

---

## 7. Anti-Ban Architecture

### Signals WhatsApp Uses for Ban Detection

Based on research and field experience:

1. **Send velocity** — messages sent too fast from one number
2. **Message similarity** — identical or near-identical messages to many recipients
3. **Contact freshness** — messaging contacts who haven't saved your number
4. **Typing behavior** — messages appearing without typing indicators = bot
5. **IP reputation** — datacenter IPs, known proxy ranges
6. **Account age** — new numbers banned faster than aged ones
7. **Report rate** — recipients reporting as spam
8. **Behavioral patterns** — online 24/7, no "reading" activity, no profile picture

### Hermès Anti-Ban Layers

```
Layer 1: Message Variation (hermes-campaign)
├── Spintax resolution (unique text per message)
├── Variable substitution (personalized)
└── Random media variation (if applicable)

Layer 2: Timing Simulation (hermes-campaign)
├── Calculate typing time: len(message) * (50-80ms per char) + jitter
├── Send typing indicator (composing) for calculated duration
├── Wait calculated duration
├── Send message
├── Post-message delay: 3-15s random between consecutive sends from same number
└── Daily send cap enforcement (per-number, configurable)

Layer 3: Number Management (hermes-campaign + hermes-wa)
├── Round-robin rotation across number pool
├── Daily cap per number (default 200, configurable)
├── Ban detection → immediate removal from rotation
├── Auto-pause if banned numbers hit threshold
└── Redistribute remaining contacts to surviving numbers

Layer 4: Proxy Isolation (hermes-proxy + hermes-wa)
├── Sticky proxy assignment (number ↔ proxy for lifetime)
├── Residential/mobile proxies only (no datacenter)
├── Ban-rate tracking per proxy → flag/retire bad proxies
└── Max N numbers per proxy (configurable ratio)

Layer 5: Session Hygiene (hermes-wa)
├── Set profile picture on new sessions
├── Random online/offline presence cycling
├── Occasional "read" receipt activity
└── Gradual ramp-up for new numbers (warmup, Phase 2)
```

### Typing Indicator Flow (Per Message)

```
1. Campaign service dequeues next send task
2. Calculate typing_duration = len(resolved_message) * random(50, 80) ms
3. Clamp to: min 1.5s, max 8s
4. Publish to NATS: {action: "typing_start", jid, wa_number_id}
5. WA pod calls: client.SendChatPresence(ctx, jid, ChatPresenceComposing)
6. Wait typing_duration
7. Publish to NATS: {action: "send_message", jid, wa_number_id, message}
8. WA pod sends the actual message
9. WA pod calls: client.SendChatPresence(ctx, jid, ChatPresencePaused)
10. Wait random(3000, 15000) ms before next send from this number
```

---

## 8. Observability Strategy

### Metrics (Prometheus + Grafana)

**hermes-wa metrics:**
- `hermes_wa_sessions_total{pod, status}` — gauge of connected/disconnected/banned sessions per pod
- `hermes_wa_messages_sent_total{tenant_id, number_id}` — counter
- `hermes_wa_messages_received_total{tenant_id}` — counter
- `hermes_wa_send_duration_seconds` — histogram (time to send one message)
- `hermes_wa_reconnect_total{pod}` — counter of reconnection events
- `hermes_wa_session_memory_bytes{pod}` — gauge per pod

**hermes-campaign metrics:**
- `hermes_campaign_active{tenant_id}` — gauge of running campaigns
- `hermes_campaign_send_rate{campaign_id}` — messages/minute
- `hermes_campaign_ban_rate{campaign_id}` — bans/hour during campaign
- `hermes_campaign_progress{campaign_id}` — percentage complete

**hermes-proxy metrics:**
- `hermes_proxy_pool_total{tenant_id, status}` — active/dead/flagged
- `hermes_proxy_ban_rate{proxy_id}` — bans per proxy over time

**Infrastructure metrics:**
- Standard K8s metrics via kube-state-metrics + node-exporter
- Pod CPU, memory, restart counts
- NATS stream lag, consumer pending count
- PostgreSQL connection pool, query latency
- Redis memory, hit/miss rate

### Logging (Loki)

- Structured JSON logs from all services
- Correlation via `trace_id` header passed through NATS messages and REST calls
- Key log events: session connect/disconnect, message sent/failed, ban detected, campaign state change
- Retention: 30 days

### Dashboards (Grafana)

1. **System Overview** — pod count, total sessions, messages/minute, ban rate
2. **Per-Pod Health** — sessions, memory, CPU, reconnect rate
3. **Campaign Monitor** — real-time progress, send rate, ban detection
4. **Proxy Health** — pool status, ban rate per proxy
5. **Number Health** — per-number send rate, delivery rate, ban history
6. **NATS** — stream lag, consumer pending, publish rate

### Alerting

| Alert | Condition | Severity |
|---|---|---|
| Pod down | hermes-wa pod not ready > 2 min | Critical |
| High ban rate | > 5 bans in 1 hour across tenant | Warning |
| Campaign auto-paused | Ban threshold reached | Warning |
| NATS lag | Consumer pending > 10K messages | Warning |
| Session disconnect spike | > 10% sessions disconnect in 5 min | Critical |
| Proxy pool depleted | Active proxies < 20% of total | Critical |

---

## 9. Open Questions

These need decisions before Gate 2 (contracts):

### Decisions (Approved 2026-04-03)

| # | Question | Decision |
|---|---|---|
| 1 | Inter-service sync comms | **gRPC from the start.** All service-to-service calls use protobuf contracts. No REST between services. |
| 2 | Database hosting | **Dev:** Docker Compose with self-hosted PostgreSQL. **Prod:** AWS with managed DB (RDS) as an option. |
| 3 | WA session store | **whatsmeow native `sqlstore`** (PostgreSQL backend). We add our own `wa_numbers` table on top for metadata. |
| 4 | Number warmup | **Phase 2.** Phase 1 assumes all registered numbers are already warmed up. Warmup logic will be provided by users later. |
| 5 | Infrastructure | **AWS.** No budget constraints. Single-region K8s (EKS). |
| 6 | Contact ban-check on upload | **Phase 1: Level 1 only** — check against internal ban list (DB lookup, instant). **Phase 2: Level 2+3** — WhatsApp `IsOnWhatsApp()` validation + external blacklists. |



---

## Summary of Recommendations

| Decision | Recommendation |
|---|---|
| WA Library | **whatsmeow** (Go, battle-tested, native proxy + typing support) |
| Message Broker | **NATS JetStream** (Go-native, simple ops, built-in request-reply, wildcard subjects) |
| Frontend | **Vite + React SPA** (no SSR needed for internal tool) |
| Database | **PostgreSQL** (shared cluster, tenant isolation via FK + optional RLS) |
| Cache/State | **Redis** (session cache, rate limiting, campaign progress) |
| Session Sharding | **Controller + StatefulSet** (500 sessions/pod, least-loaded assignment) |
| Inter-Service Sync | **gRPC from the start** (protobuf contracts) |
| Inter-Service Async | **NATS JetStream** (persistent streams per tenant) |
| Session Persistence | **whatsmeow native sqlstore** (PostgreSQL backend) |
| Observability | **Prometheus + Grafana + Loki** (non-negotiable from day 1) |

---

**Next step:** Gate 2 — write protobuf service definitions, gRPC contracts, NATS event schemas, and frontend API contracts in `docs/contracts/`.
