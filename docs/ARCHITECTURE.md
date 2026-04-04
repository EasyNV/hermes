# Hermès Architecture

## Service Dependency Graph

```
                    ┌─────────────────────┐
                    │   hermes-gateway     │
                    │  gRPC:8080 REST:8081 │
                    └──────────┬──────────┘
                               │ gRPC
        ┌──────────┬───────────┼───────────┬──────────┬──────────┐
        ▼          ▼           ▼           ▼          ▼          ▼
   ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐
   │ wa      │ │ campaign│ │ inbox   │ │contacts │ │ proxy   │ │ notify  │
   │ :9104   │ │ :9105   │ │ :9106   │ │ :9102   │ │ :9101   │ │ :9103   │
   └────┬────┘ └─────────┘ └─────────┘ └─────────┘ └────┬────┘ └─────────┘
        │ gRPC                                           │
        └────────────────────────────────────────────────┘
                        wa → proxy (GetProxy/GetBestProxy on session connect)
```

All services share a single PostgreSQL cluster with per-service migration namespaces.

## REST-to-gRPC Adapter

The gateway serves pure gRPC on port 8080. The frontend needs REST/JSON. The adapter (`internal/gateway/rest/`) bridges this gap:

- 76 HTTP routes registered on the existing port 8081 HTTP server (alongside `/ws`)
- Each route decodes JSON via `protojson.Unmarshal`, calls the gRPC handler method directly (in-process, zero network hops), encodes the response via `protojson.Marshal`
- Auth: JWT extracted from `Authorization: Bearer` header, validated via `middleware.ParseJWT`, context populated with `CtxUserID/TenantID/WorkspaceID/Role`
- CORS: permissive in dev (`Access-Control-Allow-Origin: *`)
- gRPC status codes mapped to HTTP: `InvalidArgument→400`, `Unauthenticated→401`, `PermissionDenied→403`, `NotFound→404`, `Unimplemented→501`

## Auth Flow

```
Login(email, password)
  → bcrypt verify against users table
  → Generate JWT (HS256, 15min TTL)
    Claims: {uid, tid, wid, role, exp, iat, jti}
  → Generate refresh token (UUID, stored in refresh_tokens table, 7d TTL)
  → Return {accessToken, refreshToken, expiresIn, user}

Every authenticated request:
  → AuthInterceptor extracts JWT from metadata/header
  → Validates signature + expiry
  → Injects uid/tid/wid/role into context
  → RBACInterceptor checks method against 73-entry rpcRoles map
  → Superadmin bypasses all RBAC checks
```

## NATS JetStream Events

### Streams (no subject overlap)

| Stream | Subjects | Retention |
|--------|----------|-----------|
| HERMES_WA | `hermes.wa.message.>`, `hermes.wa.ban.>`, `hermes.wa.connection.>`, `hermes.wa.presence.>` | 7 days |
| HERMES_CAMPAIGN | `hermes.campaign.>`, `hermes.wa.send.campaign.>` | 30 days |
| HERMES_INBOX | `hermes.wa.send.manual.>` | 24 hours |
| HERMES_CONTACTS | `hermes.contacts.>` | 24 hours |
| HERMES_NOTIFY | `hermes.notify.>` | 1 hour |

### Event Map

| Subject | Publisher | Consumers | Durable Name |
|---------|-----------|-----------|--------------|
| `hermes.wa.message.inbound.{tid}` | wa | inbox, campaign, gateway | inbox-inbound, campaign-inbound, gateway-inbound |
| `hermes.wa.message.outbound.{tid}` | wa | inbox, gateway | inbox-outbound, gateway-outbound |
| `hermes.wa.ban.{tid}` | wa | campaign, proxy, gateway | campaign-ban, proxy-ban, gateway-ban |
| `hermes.wa.connection.{tid}` | wa | gateway | gateway-connection |
| `hermes.wa.presence.{tid}` | wa | gateway | gateway-presence |
| `hermes.wa.send.campaign.{tid}` | campaign | wa | wa-campaign-send |
| `hermes.wa.send.manual.{tid}` | inbox | wa | wa-manual-send |
| `hermes.campaign.status.{tid}` | campaign | gateway | gateway-campaign-status |
| `hermes.campaign.progress.{tid}` | campaign | gateway | gateway-campaign-progress |
| `hermes.contacts.import.done.{tid}` | contacts | gateway | gateway-import-done |
| `hermes.notify.dispatch.{tid}` | inbox, campaign | notify | notify-dispatch |

All events include `EventMeta` envelope: `event_id` (UUID, used as `Nats-Msg-Id` for dedup), `tenant_id`, `timestamp`, `source`.

**Critical: Consumers must NAK (not ACK) on processing failure.** ACKing an unprocessed task = permanent data loss.

## Database Schema (20 tables)

### Gateway (5 tables)
- `tenants` — top-level accounts
- `workspaces` — organizational units within tenants
- `users` — authenticated users with email/password/role
- `workspace_members` — user↔workspace assignment with role
- `refresh_tokens` — JWT refresh token store

### WA (2 tables + whatsmeow auto-creates ~16 internal tables)
- `wa_numbers` — registered WhatsApp numbers with status/health/proxy assignment
- `wa_number_workspaces` — number↔workspace assignment (many-to-many)

### Campaign (4 tables)
- `templates` — message templates with spintax + variable placeholders
- `campaigns` — campaign definitions with status lifecycle
- `campaign_numbers` — WA numbers assigned to campaigns
- `campaign_contacts` — contacts assigned to campaigns with per-contact send status

### Inbox (3 tables)
- `conversations` — 1:1 chat threads (unique per workspace+contact+wa_number)
- `messages` — individual messages with direction, status, wa_message_id
- `canned_responses` — quick-reply shortcuts

### Contacts (3 tables)
- `contacts` — contact records with phone, name, ban status
- `contact_tags` — tag assignments (many-to-many)
- `contact_custom_fields` — key-value custom fields (separate table, NOT a column)

### Proxy (1 table)
- `proxies` — SOCKS5/HTTP proxy pool with health/ban tracking

### Notify (1 table)
- `notification_configs` — webhook/push notification targets per workspace

## Campaign Dispatch Flow

```
CreateCampaign (draft) → assign numbers + contacts → StartCampaign
  ↓
Campaign engine starts goroutine for this campaign
  ↓
Loop: fetch 100 PENDING contacts
  ↓
For each contact:
  1. Select WA number (round_robin or least_used rotation)
  2. Resolve template: spintax → variable substitution
  3. Calculate typing_duration_ms = clamp(len(body) * rand(50,80), 1500, 8000)
  4. Calculate post_send_delay_ms = rand(delay_min, delay_max)
  5. Publish CampaignSendTask to NATS hermes.wa.send.campaign.{tid}
  6. Mark contact as "sent" in campaign_contacts
  ↓
WA consumer receives task:
  1. GetClient(waNumberId) — must be CONNECTED
  2. SendTypingIndicator (composing → wait → paused)
  3. SendMessage via whatsmeow
  4. IncrementSentCount on wa_number
  5. ACK the NATS message
  ↓
Progress events published every 10 sends or 5 seconds
Campaign completes when all contacts processed
Auto-pause on ban threshold breach
```

## Inbox Inbound Flow

```
Contact sends WhatsApp message
  ↓
whatsmeow fires *events.Message
  ↓
WA event handler filters:
  - Skip IsFromMe (history sync, self-messages)
  - Accept chat.Server: DefaultUserServer OR HiddenUserServer (@lid)
  - Skip IsGroup
  - For LID senders: resolve phone via SenderAlt JID
  ↓
Publish WaInboundMessageEvent to NATS hermes.wa.message.inbound.{tid}
  ↓
Inbox consumer receives:
  1. Normalize phone (strip '+' prefix)
  2. Find contact by phone (try with and without '+')
  3. If not found: auto-create contact
  4. Resolve workspace from WA number
  5. Find or create conversation
  6. Reopen if closed
  7. Store message in DB
  8. Update last_message_at
  9. If unassigned: publish notification
  ↓
Gateway WebSocket hub receives NATS event → broadcasts to matching clients
Frontend updates conversation list in real-time
```

## Inbox Reply Flow

```
Agent types message in inbox UI → clicks Send
  ↓
POST /api/v1/conversations/{id}/messages
  ↓
Inbox service:
  1. Create message in DB (status = PENDING)
  2. Resolve contact phone (strip '+' for JID)
  3. Construct recipientJID: phone + "@s.whatsapp.net"
  4. Publish ManualSendTask to NATS hermes.wa.send.manual.{tid}
  5. Return message to frontend
  ↓
WA consumer receives:
  1. GetClient(waNumberId) — NAK if not connected (retry later)
  2. SendMessage via whatsmeow
  3. Update message: status='sent', wa_message_id=<from whatsmeow>
  4. ACK the NATS message
  ↓
whatsmeow delivery receipt:
  → WA event handler publishes WaOutboundStatusEvent
  → Inbox consumer updates message status: sent → delivered → read
```

## whatsmeow Integration

- **Device identity**: MacOS Desktop (`Os="Mac OS"`, `PlatformType=DESKTOP`, `Version=2.2450.6`)
- **QR pairing**: `GetQRChannel(context.Background())` → `Connect()` → consume QR events. **Must use background context** — request context cancellation kills the websocket.
- **Phone pairing**: after first QR event, call `PairPhone(phone, true, PairClientChrome, "Chrome (Linux)")` → returns 8-char code
- **QR image**: raw whatsmeow string converted to 256px PNG via `go-qrcode`, returned as base64
- **Session persistence**: PostgreSQL via `pgx` stdlib driver (blank import required)
- **LID contacts**: WhatsApp migrated contacts to `@lid` server. Accept both `DefaultUserServer` and `HiddenUserServer`.

## WebSocket Hub

- Hub manages clients in `map[*Client]struct{}` with RWMutex
- Max 3 connections per user
- Heartbeat: server ping every 30s, close after 3 missed pongs
- `EventSubscriber` bridges 8 NATS subjects to JSON WebSocket messages
- Scoping: tenant-wide, workspace-scoped, or user-specific broadcasts
- Non-blocking send with drop on full buffer
