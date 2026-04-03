# WebSocket Contract

The Hermès frontend connects to a single WebSocket endpoint on the gateway for all real-time updates. The gateway subscribes to relevant NATS subjects and fans out events to connected clients based on their tenant/workspace scope.

---

## Connection Lifecycle

### Endpoint

```
wss://{gateway_host}/ws
```

### Authentication

1. Client connects with the JWT access token as a query parameter or `Authorization` header:
   ```
   wss://api.hermes.example.com/ws?token={access_token}
   ```
   or
   ```
   Authorization: Bearer {access_token}
   ```

2. Gateway validates the JWT. If invalid or expired:
   ```json
   { "type": "error", "payload": { "code": "AUTH_FAILED", "message": "Invalid or expired token" } }
   ```
   Connection is closed with WebSocket close code `4001`.

3. On successful auth, gateway sends:
   ```json
   { "type": "connected", "payload": { "user_id": "uuid", "workspace_id": "uuid", "tenant_id": "uuid" } }
   ```

4. Gateway subscribes to NATS subjects scoped to the user's tenant_id and pushes relevant events.

### Heartbeat

| Direction | Interval | Message |
|---|---|---|
| Server → Client | Every 30 seconds | WebSocket ping frame |
| Client → Server | Response to ping | WebSocket pong frame |

If the client does not respond to 3 consecutive pings (90 seconds), the server closes the connection.

The client can also send application-level pings:
```json
{ "type": "ping" }
```

Server responds:
```json
{ "type": "pong", "payload": { "server_time": "2026-04-03T15:30:00Z" } }
```

### Reconnection

The client is responsible for reconnection. Recommended strategy:

1. On disconnect, wait `1s`, then reconnect.
2. On subsequent failures, use exponential backoff: `min(2^attempt * 1000, 30000)` ms.
3. Add jitter: `± random(0, 500)` ms.
4. After 10 consecutive failures, show a "connection lost" banner in the UI.
5. On reconnect, the client should re-fetch current state via REST (conversations list, campaign statuses) since events during disconnect are lost.

### Token Refresh

When the access token is about to expire (< 60 seconds remaining):
1. Client calls `RefreshToken` RPC via REST/gRPC.
2. Client sends a re-auth message over the existing WebSocket:
   ```json
   { "type": "auth", "payload": { "token": "{new_access_token}" } }
   ```
3. Gateway validates and responds:
   ```json
   { "type": "auth_ok" }
   ```

If the token expires before refresh, the WebSocket is closed with code `4001` and the client must reconnect with a new token.

---

## Message Format

All WebSocket messages are JSON with a type discriminator:

```json
{
  "type": "<event_type>",
  "payload": { ... }
}
```

### Envelope Fields

| Field | Type | Description |
|---|---|---|
| `type` | string | Event type identifier (see catalog below). |
| `payload` | object | Event-specific data. |

---

## Event Catalog

### 1. `new_message`

A new inbound message was received from a contact.

**Source NATS subject:** `hermes.wa.message.inbound.{tenant_id}`

**Scope:** Pushed to all users in the same workspace as the receiving WA number.

```json
{
  "type": "new_message",
  "payload": {
    "conversation_id": "uuid",
    "message": {
      "id": "uuid",
      "conversation_id": "uuid",
      "direction": "INBOUND",
      "content_type": "TEXT",
      "body": "Hello, I'm interested in your product",
      "media_url": "",
      "wa_message_id": "3EB0XXXX",
      "status": "DELIVERED",
      "created_at": "2026-04-03T15:30:00Z"
    },
    "contact": {
      "id": "uuid",
      "name": "John Doe",
      "phone": "+6281234567890"
    },
    "wa_number_id": "uuid",
    "wa_number_phone": "+6287654321000",
    "is_new_conversation": false,
    "conversation_status": "ASSIGNED",
    "assigned_to": "uuid"
  }
}
```

**Frontend behavior:**
- If conversation is in the list: move to top, update preview, increment unread counter.
- If conversation is new (`is_new_conversation: true`): add to list.
- If agent is viewing this conversation: append message to chat.
- If conversation is unassigned: highlight in queue.
- Play notification sound (if enabled).

---

### 2. `message_status_updated`

Delivery status of an outbound message changed (sent → delivered → read → failed).

**Source NATS subject:** `hermes.wa.message.outbound.{tenant_id}`

**Scope:** Pushed to the user viewing the conversation (or all workspace users).

```json
{
  "type": "message_status_updated",
  "payload": {
    "conversation_id": "uuid",
    "message_id": "uuid",
    "wa_message_id": "3EB0XXXX",
    "status": "DELIVERED",
    "updated_at": "2026-04-03T15:30:05Z"
  }
}
```

**Frontend behavior:**
- Update tick indicators on the message bubble (single tick → double tick → blue tick).
- On FAILED: show error indicator on the message.

---

### 3. `conversation_updated`

A conversation's metadata changed (claimed, transferred, closed, reopened).

**Source:** Gateway internal (after processing ClaimConversation, TransferConversation, CloseConversation RPCs, or inbox service creating a new conversation).

**Scope:** All users in the workspace.

```json
{
  "type": "conversation_updated",
  "payload": {
    "conversation_id": "uuid",
    "status": "ASSIGNED",
    "assigned_to": "uuid",
    "assigned_to_name": "Agent Smith",
    "updated_by": "uuid",
    "action": "claimed"
  }
}
```

`action` values: `"claimed"`, `"transferred"`, `"closed"`, `"reopened"`, `"created"`.

**Frontend behavior:**
- Update conversation status badge in the list.
- If claimed by another agent: remove from the viewer's unassigned queue.
- If transferred to the viewer: show toast "Conversation transferred to you."
- If closed: move to closed tab or remove from active list.

---

### 4. `campaign_progress`

Real-time campaign send progress update.

**Source NATS subject:** `hermes.campaign.progress.{tenant_id}`

**Scope:** Users in the workspace that owns the campaign.

```json
{
  "type": "campaign_progress",
  "payload": {
    "campaign_id": "uuid",
    "total_contacts": 5000,
    "sent_count": 1250,
    "delivered_count": 1100,
    "failed_count": 15,
    "replied_count": 42,
    "banned_count": 0,
    "progress_percent": 25.0,
    "send_rate_per_min": 12.5,
    "eta_seconds": 1800,
    "number_progress": [
      {
        "wa_number_id": "uuid",
        "phone": "+6287654321000",
        "status": "ACTIVE",
        "sent_count": 420,
        "failed_count": 3
      },
      {
        "wa_number_id": "uuid",
        "phone": "+6287654321001",
        "status": "ACTIVE",
        "sent_count": 415,
        "failed_count": 5
      },
      {
        "wa_number_id": "uuid",
        "phone": "+6287654321002",
        "status": "ACTIVE",
        "sent_count": 415,
        "failed_count": 7
      }
    ]
  }
}
```

**Frontend behavior:**
- Update progress bar on campaign detail page.
- Update counters (sent, delivered, failed, replied).
- Update send rate and ETA.

---

### 5. `campaign_status_changed`

Campaign transitioned to a new state.

**Source NATS subject:** `hermes.campaign.status.{tenant_id}`

**Scope:** Users in the workspace that owns the campaign.

```json
{
  "type": "campaign_status_changed",
  "payload": {
    "campaign_id": "uuid",
    "previous_status": "RUNNING",
    "new_status": "PAUSED",
    "reason": "ban_threshold",
    "timestamp": "2026-04-03T15:30:00Z"
  }
}
```

**Frontend behavior:**
- Update campaign status badge.
- Show toast notification (especially for auto-pause and completion).
- If paused by ban threshold: show warning banner with ban details.

---

### 6. `number_status_changed`

A WA number's connection status changed (connected, disconnected, banned).

**Source NATS subject:** `hermes.wa.connection.{tenant_id}`

**Scope:** All users in the tenant (number management is tenant-scoped).

```json
{
  "type": "number_status_changed",
  "payload": {
    "wa_number_id": "uuid",
    "phone": "+6287654321000",
    "display_name": "Sales 01",
    "status": "ACTIVE",
    "previous_status": "DISCONNECTED",
    "pod_id": "hermes-wa-3",
    "timestamp": "2026-04-03T15:30:00Z"
  }
}
```

**Frontend behavior:**
- Update status indicator (green/red/yellow dot) next to the number.
- On BANNED: show alert toast, highlight in number management table.

---

### 7. `ban_detected`

A WA number was banned. Higher priority notification than `number_status_changed`.

**Source NATS subject:** `hermes.wa.ban.{tenant_id}`

**Scope:** Tenant admins and workspace admins.

```json
{
  "type": "ban_detected",
  "payload": {
    "wa_number_id": "uuid",
    "phone": "+6287654321000",
    "display_name": "Sales 01",
    "proxy_id": "uuid",
    "proxy_host": "proxy1.example.com",
    "detected_at": "2026-04-03T15:30:00Z",
    "active_campaigns_affected": ["uuid1", "uuid2"],
    "wa_reason": "spam_detected"
  }
}
```

**Frontend behavior:**
- Show prominent alert/modal.
- List affected campaigns.
- Link to proxy details for investigation.

---

### 8. `typing_indicator`

A contact is typing in a conversation (Phase 3).

**Source NATS subject:** `hermes.wa.presence.{tenant_id}`

**Scope:** The agent viewing the specific conversation.

```json
{
  "type": "typing_indicator",
  "payload": {
    "conversation_id": "uuid",
    "contact_jid": "6281234567890@s.whatsapp.net",
    "is_composing": true
  }
}
```

**Frontend behavior:**
- Show "typing..." indicator in the chat footer.
- Auto-clear after 10 seconds if no `is_composing: false` received (safety timeout).

---

### 9. `import_complete`

Contact CSV import finished processing.

**Source NATS subject:** `hermes.contacts.import.done.{tenant_id}`

**Scope:** The user who initiated the import.

```json
{
  "type": "import_complete",
  "payload": {
    "filename": "contacts-april.csv",
    "imported_count": 1500,
    "skipped_count": 23,
    "updated_count": 0,
    "failed_count": 7
  }
}
```

**Frontend behavior:**
- Show toast notification with import summary.
- Refresh contacts list if on the contacts page.

---

### 10. `notification_alert`

A notification that should trigger browser-level alerts (from notify service).

**Source NATS subject:** `hermes.notify.dispatch.{tenant_id}` (sound type)

**Scope:** All users in the target workspace.

```json
{
  "type": "notification_alert",
  "payload": {
    "category": "NEW_MESSAGE",
    "title": "New message from +62812...",
    "body": "Hello, I'm interested in your product",
    "workspace_id": "uuid"
  }
}
```

**Frontend behavior:**
- Trigger browser notification (if permitted).
- Play notification sound (if sound notifications enabled for this workspace).

---

## Client-to-Server Messages

The WebSocket is primarily server-push, but clients can send these messages:

### `ping`
Heartbeat check (see above).

### `auth`
Re-authenticate with a new token (see Token Refresh above).

### `subscribe_conversation`
Subscribe to events for a specific conversation (typing indicators, message status).

```json
{
  "type": "subscribe_conversation",
  "payload": {
    "conversation_id": "uuid"
  }
}
```

Server responds:
```json
{
  "type": "subscribed",
  "payload": {
    "conversation_id": "uuid"
  }
}
```

### `unsubscribe_conversation`
Stop receiving conversation-specific events.

```json
{
  "type": "unsubscribe_conversation",
  "payload": {
    "conversation_id": "uuid"
  }
}
```

---

## Error Events

If the server encounters an error processing a client message:

```json
{
  "type": "error",
  "payload": {
    "code": "INVALID_MESSAGE",
    "message": "Unknown message type: foobar"
  }
}
```

Error codes:
| Code | Description |
|---|---|
| `AUTH_FAILED` | Token invalid or expired. Connection will close. |
| `AUTH_EXPIRED` | Token expired during session. Send `auth` message to re-authenticate. |
| `INVALID_MESSAGE` | Malformed or unknown message type. |
| `NOT_FOUND` | Referenced resource (e.g. conversation_id) not found. |
| `FORBIDDEN` | User doesn't have permission for the requested operation. |
| `INTERNAL` | Server-side error. Client should retry. |

---

## Connection Limits

| Parameter | Value |
|---|---|
| Max connections per user | 3 (multiple browser tabs) |
| Max message size (client → server) | 4 KB |
| Max message size (server → client) | 64 KB |
| Idle timeout (no client activity) | 5 minutes (pong responses count as activity) |
| Rate limit (client messages) | 10 messages/second per connection |
