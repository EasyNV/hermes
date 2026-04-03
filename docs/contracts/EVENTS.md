# NATS JetStream Event Contracts

All async inter-service communication in Hermès uses NATS JetStream. This document defines every event subject, its payload, publisher, consumers, and stream configuration.

Event payloads are defined as protobuf messages in `proto/events.proto`. Every event includes an `EventMeta` envelope with `event_id` (used as `Nats-Msg-Id` for dedup), `tenant_id`, `timestamp`, `trace_id`, and `source`.

---

## Stream Configuration

### Stream: `HERMES_WA`

Messages from WhatsApp sessions (inbound messages, outbound status, bans, connections).

| Setting | Value |
|---|---|
| Subjects | `hermes.wa.>` |
| Storage | File |
| Retention | Limits (max age + max bytes) |
| Max Age | 7 days |
| Max Bytes | 10 GB |
| Replicas | 3 (production), 1 (dev) |
| Discard Policy | Old |
| Dedup Window | 5 minutes |
| Max Msg Size | 1 MB |

### Stream: `HERMES_CAMPAIGN`

Campaign lifecycle and send task events.

| Setting | Value |
|---|---|
| Subjects | `hermes.campaign.>`, `hermes.wa.send.campaign.>` |
| Storage | File |
| Retention | WorkQueue (for send tasks), Limits (for status/progress) |
| Max Age | 30 days |
| Max Bytes | 50 GB |
| Replicas | 3 (production), 1 (dev) |
| Discard Policy | Old |
| Dedup Window | 5 minutes |
| Max Msg Size | 256 KB |

### Stream: `HERMES_INBOX`

Manual send tasks from inbox agents.

| Setting | Value |
|---|---|
| Subjects | `hermes.wa.send.manual.>` |
| Storage | File |
| Retention | WorkQueue |
| Max Age | 24 hours |
| Max Bytes | 1 GB |
| Replicas | 3 (production), 1 (dev) |
| Discard Policy | Old |
| Dedup Window | 5 minutes |
| Max Msg Size | 1 MB |

### Stream: `HERMES_CONTACTS`

Contact import completion events.

| Setting | Value |
|---|---|
| Subjects | `hermes.contacts.>` |
| Storage | File |
| Retention | Limits |
| Max Age | 24 hours |
| Max Bytes | 100 MB |
| Replicas | 3 (production), 1 (dev) |
| Discard Policy | Old |
| Dedup Window | 2 minutes |
| Max Msg Size | 64 KB |

### Stream: `HERMES_NOTIFY`

Notification dispatch requests.

| Setting | Value |
|---|---|
| Subjects | `hermes.notify.>` |
| Storage | File |
| Retention | WorkQueue |
| Max Age | 1 hour |
| Max Bytes | 500 MB |
| Replicas | 3 (production), 1 (dev) |
| Discard Policy | Old |
| Dedup Window | 2 minutes |
| Max Msg Size | 64 KB |

---

## Event Catalog

### 1. `hermes.wa.message.inbound.{tenant_id}`

Incoming WhatsApp message from a contact.

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.message.inbound.{tenant_id}` |
| **Payload** | `WaInboundMessageEvent` (proto/events.proto) |
| **Publisher** | `hermes-wa` |
| **Consumers** | `hermes-inbox`, `hermes-campaign`, `hermes-notify` |
| **Ordering** | Per wa_number_id (recommended consumer filter) |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-inbox | `inbox-inbound-{tenant_id}` | `hermes.wa.message.inbound.{tenant_id}` | Explicit | 5 | 30s |
| hermes-campaign | `campaign-inbound-{tenant_id}` | `hermes.wa.message.inbound.{tenant_id}` | Explicit | 3 | 30s |
| hermes-notify | `notify-inbound-{tenant_id}` | `hermes.wa.message.inbound.{tenant_id}` | Explicit | 3 | 10s |

**Processing semantics:**
- **hermes-inbox:** Creates or updates a conversation. If conversation doesn't exist, creates one (status = UNASSIGNED). Stores the message in the `messages` table. Publishes `notify.dispatch` if conversation is unassigned.
- **hermes-campaign:** Checks if `sender_jid` matches any contact in an active campaign. If so, increments `replied_count` on the campaign.
- **hermes-notify:** Triggers notification dispatch if the conversation is unassigned (no agent handling it).

---

### 2. `hermes.wa.message.outbound.{tenant_id}`

Delivery status update for an outbound message (sent/delivered/read/failed).

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.message.outbound.{tenant_id}` |
| **Payload** | `WaOutboundStatusEvent` (proto/events.proto) |
| **Publisher** | `hermes-wa` |
| **Consumers** | `hermes-inbox`, `hermes-campaign` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-inbox | `inbox-outbound-{tenant_id}` | `hermes.wa.message.outbound.{tenant_id}` | Explicit | 5 | 30s |
| hermes-campaign | `campaign-outbound-{tenant_id}` | `hermes.wa.message.outbound.{tenant_id}` | Explicit | 3 | 30s |

**Processing semantics:**
- **hermes-inbox:** Updates `messages.status` to the new status. Publishes WebSocket event for real-time UI update.
- **hermes-campaign:** Updates `campaign_contacts.status` based on delivery status. Increments campaign-level counters (`sent_count`, `delivered_count`, `failed_count`).

---

### 3. `hermes.wa.send.campaign.{tenant_id}`

Campaign send task — one message to one contact.

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.send.campaign.{tenant_id}` |
| **Payload** | `CampaignSendTask` (proto/events.proto) |
| **Publisher** | `hermes-campaign` |
| **Consumer** | `hermes-wa` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-wa | `wa-campaign-send-{tenant_id}` | `hermes.wa.send.campaign.{tenant_id}` | Explicit | 3 | 120s |

**Processing semantics:**
- WA pod filters by assigned `wa_number_id` (only processes sends for numbers it owns).
- Sends typing indicator for `typing_duration_ms`.
- Sends the message.
- Waits `post_send_delay_ms`.
- Publishes `wa.message.outbound` with delivery status.
- On failure: NAKs for retry (up to 3). On permanent failure: ACKs and publishes outbound status with FAILED.

**Ordering:** The campaign service publishes tasks in the order determined by the rotation strategy. The WA consumer processes them in order (per wa_number_id) to maintain correct timing between sends from the same number.

---

### 4. `hermes.wa.send.manual.{tenant_id}`

Manual agent reply from inbox.

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.send.manual.{tenant_id}` |
| **Payload** | `ManualSendTask` (proto/events.proto) |
| **Publisher** | `hermes-inbox` |
| **Consumer** | `hermes-wa` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-wa | `wa-manual-send-{tenant_id}` | `hermes.wa.send.manual.{tenant_id}` | Explicit | 5 | 60s |

**Processing semantics:**
- WA pod sends the message immediately (no typing delay for manual sends — the agent already typed).
- Publishes `wa.message.outbound` with delivery status.
- On success: ACK.
- On failure: NAK for retry. After max retries: ACK and publish outbound FAILED status.

---

### 5. `hermes.wa.ban.{tenant_id}`

WA number ban detection.

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.ban.{tenant_id}` |
| **Payload** | `WaBanEvent` (proto/events.proto) |
| **Publisher** | `hermes-wa` |
| **Consumers** | `hermes-campaign`, `hermes-proxy` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-campaign | `campaign-ban-{tenant_id}` | `hermes.wa.ban.{tenant_id}` | Explicit | 3 | 30s |
| hermes-proxy | `proxy-ban-{tenant_id}` | `hermes.wa.ban.{tenant_id}` | Explicit | 3 | 30s |

**Processing semantics:**
- **hermes-campaign:** Removes the banned number from all active campaigns. Checks if the ban count for any campaign has hit `ban_pause_threshold` → auto-pauses the campaign. Redistributes remaining contacts to surviving numbers.
- **hermes-proxy:** Increments `proxies.ban_count` for the associated proxy. If ban count exceeds threshold → flags the proxy (status = FLAGGED).

---

### 6. `hermes.wa.connection.{tenant_id}`

WA session connection state change.

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.connection.{tenant_id}` |
| **Payload** | `WaConnectionEvent` (proto/events.proto) |
| **Publisher** | `hermes-wa` |
| **Consumer** | `hermes-gateway` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-gateway | `gateway-connection-{tenant_id}` | `hermes.wa.connection.{tenant_id}` | Explicit | 3 | 10s |

**Processing semantics:**
- Gateway pushes the event to all connected WebSocket clients subscribed to this tenant/workspace.
- Frontend updates the number status indicator in real-time.

---

### 7. `hermes.wa.presence.{tenant_id}` (Phase 3)

Contact typing indicator detection.

| Field | Value |
|---|---|
| **Subject** | `hermes.wa.presence.{tenant_id}` |
| **Payload** | `WaPresenceEvent` (proto/events.proto) |
| **Publisher** | `hermes-wa` |
| **Consumer** | `hermes-gateway` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-gateway | `gateway-presence-{tenant_id}` | `hermes.wa.presence.{tenant_id}` | Explicit | 1 | 5s |

**Processing semantics:**
- Gateway pushes typing indicator to the agent viewing the conversation.
- Short TTL — if not delivered quickly, discard (typing indicators are ephemeral).

---

### 8. `hermes.campaign.status.{tenant_id}`

Campaign state transition.

| Field | Value |
|---|---|
| **Subject** | `hermes.campaign.status.{tenant_id}` |
| **Payload** | `CampaignStatusEvent` (proto/events.proto) |
| **Publisher** | `hermes-campaign` |
| **Consumer** | `hermes-gateway` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-gateway | `gateway-campaign-status-{tenant_id}` | `hermes.campaign.status.{tenant_id}` | Explicit | 3 | 10s |

**Processing semantics:**
- Gateway pushes the status change to WebSocket clients.
- Frontend updates campaign status badge and triggers toast notification.

---

### 9. `hermes.campaign.progress.{tenant_id}`

Real-time campaign progress update.

| Field | Value |
|---|---|
| **Subject** | `hermes.campaign.progress.{tenant_id}` |
| **Payload** | `CampaignProgressEvent` (proto/events.proto) |
| **Publisher** | `hermes-campaign` |
| **Consumer** | `hermes-gateway` |

**Publish frequency:** Every 10 sends or every 5 seconds (whichever comes first) while campaign is RUNNING.

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-gateway | `gateway-campaign-progress-{tenant_id}` | `hermes.campaign.progress.{tenant_id}` | Explicit | 1 | 5s |

**Processing semantics:**
- Gateway pushes progress to WebSocket clients.
- Frontend updates progress bar, send rate, ETA on campaign detail page and dashboard.

---

### 10. `hermes.contacts.import.done.{tenant_id}`

Contact CSV import completed.

| Field | Value |
|---|---|
| **Subject** | `hermes.contacts.import.done.{tenant_id}` |
| **Payload** | `ContactsImportDoneEvent` (proto/events.proto) |
| **Publisher** | `hermes-contacts` |
| **Consumer** | `hermes-gateway` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-gateway | `gateway-import-done-{tenant_id}` | `hermes.contacts.import.done.{tenant_id}` | Explicit | 3 | 10s |

**Processing semantics:**
- Gateway pushes import result to the WebSocket client that initiated the import.
- Frontend shows a toast with import stats (imported, skipped, failed).

---

### 11. `hermes.notify.dispatch.{tenant_id}`

Notification dispatch request.

| Field | Value |
|---|---|
| **Subject** | `hermes.notify.dispatch.{tenant_id}` |
| **Payload** | `NotifyDispatchEvent` (proto/events.proto) |
| **Publishers** | `hermes-inbox`, `hermes-campaign` |
| **Consumer** | `hermes-notify` |

**Consumer Configuration:**

| Consumer | Durable Name | Filter Subject | Ack Policy | Max Deliver | Ack Wait |
|---|---|---|---|---|---|
| hermes-notify | `notify-dispatch-{tenant_id}` | `hermes.notify.dispatch.{tenant_id}` | Explicit | 5 | 30s |

**Processing semantics:**
- Notify service looks up all enabled `notification_configs` for the `workspace_id`.
- For each config, formats and delivers the notification:
  - **browser_push:** Pushes via Web Push API.
  - **sound:** Publishes a WebSocket event (gateway handles the sound trigger).
  - **webhook (telegram):** Sends formatted message to Telegram bot API.
  - **webhook (discord):** Sends embed to Discord webhook URL.
  - **webhook (custom):** POSTs JSON body to the configured URL.
- On webhook failure (4xx/5xx): retries with backoff (NATS redeliver).
- On permanent failure (e.g. invalid URL, 404): ACKs to prevent infinite retry, logs error.

---

## Subject Hierarchy (Complete)

```
hermes.
├── wa.
│   ├── message.
│   │   ├── inbound.{tenant_id}      # WaInboundMessageEvent
│   │   └── outbound.{tenant_id}     # WaOutboundStatusEvent
│   ├── send.
│   │   ├── campaign.{tenant_id}     # CampaignSendTask
│   │   └── manual.{tenant_id}       # ManualSendTask
│   ├── ban.{tenant_id}              # WaBanEvent
│   ├── connection.{tenant_id}       # WaConnectionEvent
│   └── presence.{tenant_id}         # WaPresenceEvent (Phase 3)
├── campaign.
│   ├── status.{tenant_id}           # CampaignStatusEvent
│   └── progress.{tenant_id}         # CampaignProgressEvent
├── contacts.
│   └── import.done.{tenant_id}      # ContactsImportDoneEvent
└── notify.
    └── dispatch.{tenant_id}         # NotifyDispatchEvent
```

---

## Error Handling & Retry Policy

| Scenario | Behavior |
|---|---|
| Consumer NAK | Message redelivered after ack_wait. Up to max_deliver retries. |
| Consumer timeout (no ACK/NAK) | Same as NAK — redelivered after ack_wait. |
| Max retries exhausted | Message moved to advisory subject `$JS.EVENT.ADVISORY.MAX_DELIVERIES.*`. Log alert. |
| Duplicate message (same Nats-Msg-Id) | Silently deduplicated by JetStream within dedup window. |
| Consumer crash | Pending messages redeliver to other consumers in the group (if horizontally scaled). |
| Stream full (max_bytes) | Oldest messages discarded (Discard Policy: Old). |

## Idempotency

All consumers MUST be idempotent. Even with NATS deduplication, messages can be redelivered (consumer restart within ack_wait window). Design:

- **Campaign send tasks:** Use `idempotency_key` (campaign_id + contact_id). WA service checks Redis/DB before sending.
- **Manual send tasks:** Use `message_id` as idempotency key. WA service checks if already sent.
- **Status updates:** Apply only if the new status is a valid forward transition (pending → sent → delivered → read). Ignore stale/duplicate status events.
- **Ban events:** Flag operation is idempotent (setting a boolean to true is safe to repeat).
