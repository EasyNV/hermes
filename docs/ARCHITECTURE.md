# Hermes Architecture

Hermes is a multi-service WhatsApp automation stack. It combines:

- Public gateway APIs for frontend and operator clients.
- gRPC microservices for WA, MBS, campaign, inbox, contacts, proxy, and notify domains.
- PostgreSQL-backed persistence.
- NATS/JetStream eventing and send queues.
- Redis-backed runtime/session support.
- A React/Vite operator frontend.
- Private MBS bridge/native integration modules under `third_party/`.

`re/**` reverse-engineering workspaces are intentionally outside normal service architecture and build/test status.

## Service graph

```text
                         ┌────────────────────┐
                         │ React/Vite frontend │
                         └─────────┬──────────┘
                                   │ REST + WS
                                   ▼
┌──────────────────────────────────────────────────────────────┐
│ gateway                                                       │
│ - gRPC HermesGateway on :8080                                 │
│ - REST/JSON + WebSocket on :8081                              │
│ - JWT auth, claim injection, RBAC middleware                  │
│ - direct gRPC clients to backend services                     │
└────┬─────────┬─────────┬──────────┬────────┬────────┬────────┘
     │         │         │          │        │        │
     ▼         ▼         ▼          ▼        ▼        ▼
   proxy    contacts   notify       wa      mbs    campaign ───► inbox
     │         │         │          │        │        │          │
     └─────────┴─────────┴──────────┴────────┴────────┴──────────┘
                                   │
                            PostgreSQL / NATS / Redis
```

Service entrypoints:

- `cmd/gateway`
- `cmd/proxy`
- `cmd/contacts`
- `cmd/notify`
- `cmd/wa`
- `cmd/mbs`
- `cmd/campaign`
- `cmd/inbox`
- `cmd/mbs-import`

Each primary service has private implementation under `internal/<service>/` and migrations under `migrations/<service>/`.

## API contracts

Protobuf definitions live under `proto/hermes/v1/`.

Current service contracts:

- `HermesGateway`: 75 RPCs.
- `HermesMbs`: 9 RPCs.
- `HermesCampaign`: 17 RPCs.
- `HermesInbox`: 14 RPCs.
- `HermesContacts`: 11 RPCs.
- `HermesProxy`: 11 RPCs.
- `HermesWa`: 8 RPCs.
- `HermesNotify`: 6 RPCs.

The gateway REST adapter mounts 89 routes total. It is an in-process JSON adapter around the gateway handler, not grpc-gateway/envoy/connect-go.

## Gateway architecture

Gateway responsibilities:

- Authenticate user login/refresh/logout flows.
- Issue and parse JWTs.
- Inject user, tenant, workspace, and role claims into handler contexts.
- Apply gRPC auth/RBAC middleware.
- Expose REST JSON endpoints for the frontend.
- Expose WebSocket endpoints:
  - `/ws` for general event fan-out.
  - `/ws/mbs/bridge-login` for bidirectional MBS bridge login.
- Hold gRPC clients to backend services.
- Subscribe to NATS/JetStream events for frontend WebSocket fan-out.

Gateway gRPC runs on `:8080`. Gateway REST/WS runs on `:8081`.

### Authentication and authorization

- REST endpoints under `/api/v1/auth/login` and `/api/v1/auth/refresh` are unauthenticated.
- Most REST endpoints are wrapped in JWT auth middleware.
- gRPC requests are protected by auth/RBAC interceptors.
- Current review status: REST RBAC parity with gRPC RBAC should remain a hardening priority.
- MBS unary gateway methods force tenant from JWT-derived context before calling `hermes-mbs`.
- The MBS bridge-login WebSocket validates the JWT inline and overwrites any client-supplied tenant with the JWT tenant.

### CORS / origins

The REST/WS server currently has permissive CORS/origin posture suitable for development. Production deployments should front the stack with a reverse proxy and enforce explicit allowed origins.

## Eventing architecture

NATS/JetStream carries service events and async send work.

Important event/work subjects include:

- WA messages/lifecycle:
  - `hermes.wa.message.inbound.<tenant_id>`
  - `hermes.wa.message.outbound.<tenant_id>`
  - `hermes.wa.connection.<tenant_id>`
  - `hermes.wa.ban.<tenant_id>`
  - `hermes.wa.send.campaign.<tenant_id>`
  - `hermes.wa.send.manual.<tenant_id>`
- Campaign progress/status:
  - `hermes.campaign.progress.<tenant_id>`
  - `hermes.campaign.status.<tenant_id>`
- Contacts imports:
  - `hermes.contacts.import.done.<tenant_id>`
- Notify dispatch:
  - `hermes.notify.dispatch.<tenant_id>`
- MBS messages/lifecycle/work:
  - `hermes.mbs.message.inbound.<tenant_id>`
  - `hermes.mbs.message.outbound.<tenant_id>`
  - `hermes.mbs.session.<event>.<tenant_id>`
  - `hermes.mbs.send.campaign.<tenant_id>`
  - `hermes.mbs.send.manual.<tenant_id>`

The `mbs` service owns two JetStream streams:

- `HERMES_MBS` — `hermes.mbs.message.>` and `hermes.mbs.session.>`, limits retention, 7-day max age.
- `HERMES_MBS_SEND` — `hermes.mbs.send.>`, work-queue retention, 24-hour max age.

MBS send consumers use durable subscriptions:

- `mbs-campaign-send` on `hermes.mbs.send.campaign.*`
- `mbs-manual-send` on `hermes.mbs.send.manual.*`

The consumer parses the tenant from the subject suffix, injects it into the context, converts `MbsCampaignSendTask` to `MbsSendMessageRequest`, and calls the same `SendMessage` handler used by direct RPC sends.

## WhatsApp (`wa`) architecture

The WA service uses `go.mau.fi/whatsmeow` for QR/phone-paired WhatsApp sessions. Gateway routes WA number registration, QR code retrieval, disconnect/reconnect, send, typing, and status calls to `hermes-wa`.

`wa` depends on:

- PostgreSQL for persisted session/domain state.
- Redis for runtime/session support.
- NATS for messages, lifecycle, campaign/manual send subjects.
- Proxy service for proxy assignment/health behavior.

## MBS architecture

The MBS stack is split into three layers:

1. `internal/mbs` / `cmd/mbs` — Hermes service layer.
2. `third_party/mbs-native` — native BizApp/MBS client library.
3. `third_party/mautrix-meta-patched` — patched mautrix-meta bridge dependency that mints/exposes login payload/device identity needed by `mbs-native`.

### Bridge login flow

```text
browser BridgeLoginDialog
  │
  │ WebSocket /ws/mbs/bridge-login?token=<jwt>
  ▼
gateway REST adapter
  │ validates JWT, forces tenant metadata
  │ opens HermesMbs.BridgeLogin bidi stream
  ▼
hermes-mbs BridgeLogin handler
  │ runs bridge driver with email/password + optional TOTP secret
  │ emits prompt/progress/success/failure frames
  ▼
patched mautrix-meta driver
  │ exposes login payload + device identity
  ▼
mbs-native auth / web / transport / mqtt client
  │ materializes native BizApp session + discovers assets
  ▼
PostgreSQL encrypted session/cookie/blob rows + mbs_session_assets
```

Current frontend dialog inputs:

- Email or phone.
- Password.
- Optional base32 TOTP secret.

Prompt frames support live 2FA/checkpoint input if auto-TOTP is not available.

The bridge login handler enforces:

- First stream message must be `BridgeLoginStart`.
- Tenant from stream metadata is required.
- Body tenant must match metadata tenant when present.
- Concurrent bridge attempts are limited by semaphore.
- Secret-bearing persistence is encrypted field-by-field with AAD before DB write.
- Cross-tenant UID overwrite attempts fail closed.

### MBS session state

Current MBS session states:

- `ACTIVE`
- `SUSPENDED`
- `BURNED`
- `BRIDGING`

Session metadata includes UID, tenant, display name, last CONNACK result, device/app diagnostics, primary asset, timestamps, and login identifier.

Assets include page/WABA/WEC data:

- `page_id`, `page_name`
- `waba_id`
- `wec_mailbox_id`, `wec_phone_number`
- `business_presence_node_id`
- optional IG account ID
- business manager ID/name
- primary flag
- WEC registration flag

### MBS sending and listening

MBS sends use `MbsSendMessageRequest`:

- `uid` selects the MBS session.
- Recipient is either `thread_id` or `phone`.
- `text` is required.
- `client_dedupe_id` supports idempotent retry/redelivery handling.
- `page_id_override` supports multi-page accounts and must be treated as authorization-sensitive.

Phone sends resolve to a thread through cache or live BizInbox WhatsApp customer mutation before native send.

Inbound listening is exposed through `HermesMbs.Listen` and NATS message events.

## Campaign architecture

Campaigns own templates, recipients, selected senders, lifecycle, and send scheduling. They can dispatch through WA or MBS paths.

MBS campaign dispatch publishes work to:

```text
hermes.mbs.send.campaign.<tenant_id>
```

The MBS consumer redelivers transient failures up to the configured max delivery count and terminates permanent failures (invalid arguments, not found, permission/credential failures, burned sessions, etc.).

Current MBS sender rotation strategies:

- `round_robin`
- `least_used`

Review note: campaign progress semantics should be kept explicit. Enqueue/send attempt status is not the same as confirmed downstream delivery.

## Persistence

PostgreSQL is the source of truth. Each service has its own migration directory and migration table name:

```text
schema_migrations_gateway
schema_migrations_wa
schema_migrations_mbs
schema_migrations_campaign
schema_migrations_inbox
schema_migrations_contacts
schema_migrations_proxy
schema_migrations_notify
```

Production compose uses file-backed secrets. The MBS data encryption key is mounted into the `mbs` container and used for encrypted bridge/session material. Losing the DEK makes encrypted MBS rows unrecoverable.

## Deployment architecture

### Development compose

`docker-compose.dev.yml` builds local backend services from `Dockerfile.dev`, starts infrastructure, runs migrations, starts all eight services, and starts the frontend Vite dev server.

Default dev ports:

- Postgres host port: `${HERMES_PG_HOST_PORT:-5433}`
- Redis: `6380`
- NATS client: `4222`
- NATS monitor: `8222`
- Gateway gRPC: `8080`
- Gateway REST/WS: `8081`
- MBS gRPC: `8082`
- MBS metrics/health: `9092`
- Frontend Vite: `5173`

### Production compose

`docker-compose.prod.yml` uses prebuilt images:

- `hermes-proxy:${HERMES_VERSION}`
- `hermes-contacts:${HERMES_VERSION}`
- `hermes-notify:${HERMES_VERSION}`
- `hermes-wa:${HERMES_VERSION}`
- `hermes-campaign:${HERMES_VERSION}`
- `hermes-inbox:${HERMES_VERSION}`
- `hermes-mbs:${HERMES_VERSION}`
- `hermes-gateway:${HERMES_VERSION}`
- `hermes-web:${HERMES_VERSION}`

Production posture includes restart policies, resource limits, read-only service containers for MBS/gateway, tmpfs `/tmp`, file-backed Docker secrets, and health gates.

## Security boundaries and open hardening items

Important trust boundaries:

- Browser → gateway REST/WS.
- Gateway → backend gRPC services.
- NATS subjects/payloads → service consumers.
- MBS bridge driver/native client → encrypted persistence.
- Frontend local token storage → WebSocket query-token transport.
- Multi-tenant object access in gateway, store, MBS session, and campaign paths.

Current hardening priorities:

- REST RBAC parity with gRPC RBAC.
- Strict origin/CORS configuration for production.
- WebSocket token handling and log scrubbing.
- Object-level tenant/workspace/session checks.
- Validation of MBS `page_id_override` against session-owned assets.
- Subject/payload tenant consistency for NATS work queues.
- Encryption/redaction review for all MBS bridge/session material.
- Dev dependency advisory cleanup.
