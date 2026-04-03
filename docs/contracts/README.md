# Hermès — Gate 2: Service Contracts

**Status:** ✅ APPROVED — all gaps remediated, proceed to Gate 3  
**Date:** 2026-04-03  
**Depends on:** [Architecture Research (Gate 1)](../research/ARCHITECTURE.md)

---

## What This Is

This directory contains the complete inter-service contracts for every microservice boundary in the Hermès platform. A developer should be able to implement any service by reading:

1. The service's `.proto` file (gRPC API definition)
2. `EVENTS.md` (NATS JetStream async events)
3. `WEBSOCKET.md` (frontend real-time events)

No other documents should be needed. If something is ambiguous, the contract is incomplete.

---

## File Index

### Protobuf Service Definitions

| File | Service | Description |
|---|---|---|
| [`proto/common.proto`](proto/common.proto) | — | Shared types: enums, resource messages, pagination, errors |
| [`proto/gateway.proto`](proto/gateway.proto) | `HermesGateway` | Frontend-facing API. The ONLY service the web app talks to. |
| [`proto/wa.proto`](proto/wa.proto) | `HermesWa` | WhatsApp session management and message sending |
| [`proto/campaign.proto`](proto/campaign.proto) | `HermesCampaign` | Campaign lifecycle, send orchestration, template CRUD |
| [`proto/inbox.proto`](proto/inbox.proto) | `HermesInbox` | Conversation management, messages, canned responses |
| [`proto/contacts.proto`](proto/contacts.proto) | `HermesContacts` | Contact CRUD, CSV import, ban checking |
| [`proto/proxy.proto`](proto/proxy.proto) | `HermesProxy` | Proxy pool management, health checks, assignment |
| [`proto/notify.proto`](proto/notify.proto) | `HermesNotify` | Notification config and delivery |
| [`proto/events.proto`](proto/events.proto) | — | NATS JetStream event payload messages |

### Documentation

| File | Description |
|---|---|
| [`EVENTS.md`](EVENTS.md) | Complete NATS event catalog with subjects, streams, consumers, semantics |
| [`WEBSOCKET.md`](WEBSOCKET.md) | WebSocket event schemas, connection lifecycle, client protocol |

---

## Architecture Summary

```
┌──────────┐        ┌──────────┐       ┌──────────┐
│ hermes-  │──gRPC──│ hermes-  │──gRPC─│ hermes-  │
│ web      │  +WS   │ gateway  │       │ wa       │
│ (React)  │◄───────│ (API+WS) │       │ (sessions│
└──────────┘        └────┬─────┘       │  +send)  │
                         │             └────┬─────┘
                    gRPC │                  │ NATS
                  ┌──────┼──────┐     ┌─────┴──────┐
                  │      │      │     │            │
             ┌────┴───┐┌─┴────┐│┌────┴───┐  ┌─────┴────┐
             │campaign││inbox │││contacts│  │  proxy   │
             │        ││      │││        │  │          │
             └────────┘└──────┘│└────────┘  └──────────┘
                               │
                          ┌────┴───┐
                          │ notify │
                          └────────┘
```

**Communication patterns:**
- **Frontend → Gateway:** gRPC-Web + WebSocket (gateway is the only public-facing service)
- **Gateway → Services:** gRPC (sync request-response)
- **Service → Service (async):** NATS JetStream (events + work queues)
- **Gateway → Frontend (real-time):** WebSocket (gateway subscribes to NATS and pushes to clients)

---

## How to Read the Protos

### Conventions

- **Package:** All protos use `hermes.v1`
- **UUIDs:** Represented as `string` (not `bytes`)
- **Timestamps:** Always `google.protobuf.Timestamp`
- **Pagination:** Every `List*` RPC includes `PageRequest` / `PageResponse` from `common.proto`
- **RBAC:** Each RPC has a comment documenting which roles can call it
- **Field comments:** Every non-obvious field has a comment

### Import Hierarchy

```
common.proto ← (imported by all other protos)
    ↑
    ├── gateway.proto
    ├── wa.proto
    ├── campaign.proto
    ├── inbox.proto
    ├── contacts.proto
    ├── proxy.proto
    ├── notify.proto
    └── events.proto
```

### Service Ownership

| Service | Owns Tables | Proto File |
|---|---|---|
| hermes-gateway | — (stateless) | gateway.proto |
| hermes-wa | wa_numbers, wa_sessions* | wa.proto |
| hermes-campaign | campaigns, campaign_numbers, campaign_contacts, templates | campaign.proto |
| hermes-inbox | conversations, messages, canned_responses | inbox.proto |
| hermes-contacts | contacts, contact_tags, contact_custom_fields | contacts.proto |
| hermes-proxy | proxies | proxy.proto |
| hermes-notify | notification_configs | notify.proto |

*wa_sessions managed by whatsmeow's native `sqlstore`

---

## Gate 2 Verification Checklist

Use this checklist to verify completeness before approving Gate 2.

### Completeness

- [ ] Every service from the architecture doc has a proto definition
- [ ] Every table from the schema has corresponding resource messages in `common.proto`
- [ ] Every table column is represented as a field (check against architecture ERD)
- [ ] Every enum value from the architecture doc exists in the proto enums
- [ ] Every NATS subject from the architecture doc has a corresponding event in `events.proto`
- [ ] Every NATS subject is documented in `EVENTS.md` with publisher + consumers

### gRPC Contracts

- [ ] Every RPC has a descriptive comment
- [ ] Every RPC has RBAC roles documented
- [ ] Every RPC has request + response message types
- [ ] All `List*` RPCs include pagination
- [ ] All timestamp fields use `google.protobuf.Timestamp`
- [ ] All ID fields use `string` type
- [ ] Gateway proto covers ALL frontend operations (auth, CRUD, campaigns, inbox, etc.)

### Event Contracts

- [ ] Every event has a defined protobuf payload message
- [ ] Every event documents: subject pattern, publisher, consumers
- [ ] Stream configs defined: retention, max age, replicas, dedup window
- [ ] Consumer configs defined: durable name, filter, ack policy, max deliver, ack wait
- [ ] Processing semantics described for every consumer
- [ ] Idempotency strategy documented

### WebSocket Contract

- [ ] Connection lifecycle documented (auth, heartbeat, reconnect)
- [ ] All real-time events have JSON schema examples
- [ ] Frontend behavior described for each event
- [ ] Error events documented
- [ ] Client-to-server messages documented
- [ ] Connection limits defined

### Quality

- [ ] A new developer can implement any service without asking questions
- [ ] No references to undefined messages or services
- [ ] No TODOs or placeholders in proto files
- [ ] All Phase 3 items clearly marked as "(Phase 3)"

---

## Next Steps

After Gate 2 approval:

1. **Gate 3 — Implementation:** Build services against these contracts
2. **Code generation:** Run `protoc` to generate Go server/client stubs
3. **Integration testing:** Verify service-to-service communication matches contracts
4. **Contract evolution:** Changes to contracts after implementation require a PR with migration notes
