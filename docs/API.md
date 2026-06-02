# Hermes API Reference

Hermes exposes protobuf/gRPC services, a REST JSON adapter for the frontend, and WebSocket endpoints for live events and MBS bridge login.

Current gateway surfaces:

- Gateway gRPC: `:8080`
- Gateway REST/WS: `:8081`
- MBS gRPC: `:8082`

Authentication uses bearer JWTs:

```http
Authorization: Bearer <jwt>
```

Do not put real JWTs, passwords, cookies, access tokens, TOTP secrets, or connection strings in docs or examples. Use `[REDACTED]`.

## Protobuf services

Definitions live in `proto/hermes/v1/`.

### `HermesGateway` — 75 RPCs

Gateway/core API:

- Auth: `Login`, `RefreshToken`, `Logout`, `GetMe`
- Tenants: `CreateTenant`, `GetTenant`, `ListTenants`, `UpdateTenant`
- Workspaces: `CreateWorkspace`, `GetWorkspace`, `ListWorkspaces`, `UpdateWorkspace`, `DeleteWorkspace`
- Users: `CreateUser`, `GetUser`, `ListUsers`, `UpdateUser`, `DeleteUser`
- WA numbers: `RegisterWaNumber`, `GetQRCode`, `ListWaNumbers`, `GetWaNumber`, `UpdateWaNumber`, `DisconnectWaNumber`, `ReconnectWaNumber`, `DeleteWaNumber`
- Proxies: `AddProxies`, `ListProxies`, `UpdateProxy`, `DeleteProxy`, `AssignProxy`, `GetProxyHealth`, `GetBestProxy`
- Contacts: `CreateContact`, `ImportContacts`, `ListContacts`, `GetContact`, `UpdateContact`, `DeleteContact`
- Templates/campaigns: `CreateTemplate`, `GetTemplate`, `ListTemplates`, `UpdateTemplate`, `DeleteTemplate`, `CreateCampaign`, `GetCampaign`, `ListCampaigns`, `StartCampaign`, `PauseCampaign`, `ResumeCampaign`, `CancelCampaign`, `UpdateCampaignNumbers`, `UpdateCampaignContacts`, `ListCampaignContacts`, `ListCampaignNumbers`
- Inbox: `ListConversations`, `GetConversation`, `ClaimConversation`, `TransferConversation`, `CloseConversation`, `ListMessages`, `SendMessage`, `SearchMessages`, `SendTypingIndicator`
- Reporting/admin: `GetContactCampaignHistory`, `GetAgentPerformance`, `GetDashboardStats`
- Canned responses: `CreateCannedResponse`, `ListCannedResponses`, `UpdateCannedResponse`, `DeleteCannedResponse`
- Notifications: `ConfigureNotification`, `ListNotificationConfigs`, `TestNotification`, `DeleteNotificationConfig`

### `HermesMbs` — 9 RPCs

MBS API:

- `BridgeLogin`
- `ListSessions`
- `GetSessionStatus`
- `ListSessionAssets`
- `BurnSession`
- `RemoveSession`
- `ResolvePhone`
- `SendMessage`
- `Listen`

### Other backend service APIs

- `HermesCampaign` — 17 RPCs.
- `HermesInbox` — 14 RPCs.
- `HermesContacts` — 11 RPCs.
- `HermesProxy` — 11 RPCs.
- `HermesWa` — 8 RPCs.
- `HermesNotify` — 6 RPCs.

## REST adapter

The REST adapter is mounted under `/api/v1/` on the gateway HTTP server. It currently registers 89 routes total including the MBS bridge-login WebSocket.

### Auth

Unauthenticated:

- `POST /api/v1/auth/login`
- `POST /api/v1/auth/refresh`

Authenticated:

- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/me`

Example login request shape:

```json
{
  "email": "operator@example.com",
  "password": "[REDACTED]"
}
```

Example login response shape:

```json
{
  "accessToken": "[REDACTED]",
  "refreshToken": "[REDACTED]",
  "user": {
    "id": "...",
    "tenantId": "...",
    "email": "operator@example.com",
    "role": "tenant_admin"
  }
}
```

### Dashboard

- `GET /api/v1/dashboard/stats`

### Tenants

- `POST /api/v1/tenants`
- `GET /api/v1/tenants`
- `GET /api/v1/tenants/{id}`
- `PUT /api/v1/tenants/{id}`

### Workspaces

- `POST /api/v1/workspaces`
- `GET /api/v1/workspaces`
- `GET /api/v1/workspaces/{id}`
- `PUT /api/v1/workspaces/{id}`
- `DELETE /api/v1/workspaces/{id}`

### Users

- `POST /api/v1/users`
- `GET /api/v1/users`
- `GET /api/v1/users/{id}`
- `PUT /api/v1/users/{id}`
- `DELETE /api/v1/users/{id}`

### WA numbers

- `POST /api/v1/wa-numbers`
- `GET /api/v1/wa-numbers`
- `GET /api/v1/wa-numbers/{id}`
- `GET /api/v1/wa-numbers/{id}/qr-code`
- `POST /api/v1/wa-numbers/{id}/pair-phone`
- `PUT /api/v1/wa-numbers/{id}`
- `POST /api/v1/wa-numbers/{id}/disconnect`
- `POST /api/v1/wa-numbers/{id}/reconnect`
- `DELETE /api/v1/wa-numbers/{id}`

### Proxies

- `POST /api/v1/proxies`
- `GET /api/v1/proxies`
- `GET /api/v1/proxies/best`
- `GET /api/v1/proxies/{id}/health`
- `PUT /api/v1/proxies/{id}`
- `DELETE /api/v1/proxies/{id}`
- `POST /api/v1/proxies/assign`

### Contacts

- `POST /api/v1/contacts`
- `POST /api/v1/contacts/import`
- `GET /api/v1/contacts`
- `GET /api/v1/contacts/{id}`
- `PUT /api/v1/contacts/{id}`
- `DELETE /api/v1/contacts/{id}`
- `GET /api/v1/contacts/{id}/campaigns`

### Templates

- `POST /api/v1/templates`
- `GET /api/v1/templates`
- `GET /api/v1/templates/{id}`
- `PUT /api/v1/templates/{id}`
- `DELETE /api/v1/templates/{id}`

### Campaigns

- `POST /api/v1/campaigns`
- `GET /api/v1/campaigns`
- `GET /api/v1/campaigns/{id}`
- `POST /api/v1/campaigns/{id}/start`
- `POST /api/v1/campaigns/{id}/pause`
- `POST /api/v1/campaigns/{id}/resume`
- `POST /api/v1/campaigns/{id}/cancel`
- `PUT /api/v1/campaigns/{id}/numbers`
- `PUT /api/v1/campaigns/{id}/contacts`
- `GET /api/v1/campaigns/{id}/contacts`
- `GET /api/v1/campaigns/{id}/numbers`

Campaigns can dispatch through WA or MBS, depending on campaign configuration and selected sender/session type. MBS campaign sends are queued on `hermes.mbs.send.campaign.<tenant_id>`.

### Conversations / inbox

- `GET /api/v1/conversations`
- `GET /api/v1/conversations/{id}`
- `POST /api/v1/conversations/{id}/claim`
- `POST /api/v1/conversations/{id}/transfer`
- `POST /api/v1/conversations/{id}/close`
- `GET /api/v1/conversations/{id}/messages`
- `POST /api/v1/conversations/{id}/messages`
- `POST /api/v1/conversations/{id}/typing`
- `DELETE /api/v1/conversations/clear`
- `POST /api/v1/messages/search`

### Agent performance

- `GET /api/v1/agent-performance`

### Canned responses

- `POST /api/v1/canned-responses`
- `GET /api/v1/canned-responses`
- `PUT /api/v1/canned-responses/{id}`
- `DELETE /api/v1/canned-responses/{id}`

### MBS sessions

- `GET /api/v1/mbs-sessions`
- `GET /api/v1/mbs-sessions/{uid}`
- `GET /api/v1/mbs-sessions/{uid}/assets`
- `POST /api/v1/mbs-sessions/{uid}/burn`
- `DELETE /api/v1/mbs-sessions/{uid}`
- `POST /api/v1/mbs-sessions/{uid}/resolve-phone`
- `POST /api/v1/mbs-sessions/{uid}/messages`

`GET /api/v1/mbs-sessions` supports tenant-scoped listing. Gateway derives/forces tenant context from the authenticated user.

`POST /api/v1/mbs-sessions/{uid}/resolve-phone` accepts a phone number and optional `pageIdOverride`; it returns thread/page/WEC resolution data.

`POST /api/v1/mbs-sessions/{uid}/messages` accepts either a `threadId` or phone recipient, text, optional dedupe ID, and optional `pageIdOverride`.

### Allowlist

- `GET /api/v1/allowlist`
- `POST /api/v1/allowlist`
- `DELETE /api/v1/allowlist`
- `DELETE /api/v1/allowlist/clear`

### Notifications

- `POST /api/v1/notifications`
- `GET /api/v1/notifications`
- `POST /api/v1/notifications/{id}/test`
- `DELETE /api/v1/notifications/{id}`

## WebSocket endpoints

### General event WebSocket

- `/ws`

The frontend uses this endpoint for gateway fan-out of service events.

### MBS bridge login WebSocket

- `/ws/mbs/bridge-login?token=<jwt>`

This endpoint tunnels `HermesMbs.BridgeLogin` over WebSocket JSON frames.

Browser → gateway frames:

```json
{"type":"start","payload":{"email":"operator@example.com","password":"[REDACTED]","totpSecret":"[REDACTED_OPTIONAL]"}}
```

```json
{"type":"input","payload":{"fieldId":"totp_code","value":"[REDACTED]"}}
```

```json
{"type":"cancel"}
```

Gateway → browser frames:

```json
{"type":"bridge_login_progress","payload":{"stage":"BRIDGE_STAGE_PREFLIGHT","detail":"..."}}
```

```json
{"type":"bridge_login_prompt","payload":{"stepId":"two_step_verification","instructions":"...","fields":[{"id":"totp_code","name":"Code","type":"code"}]}}
```

```json
{"type":"bridge_login_success","payload":{"uid":"...","displayName":"...","pageCount":1,"assets":[...]}}
```

```json
{"type":"bridge_login_failure","payload":{"code":"BRIDGE_ERR_CHECKPOINT","message":"...","retryable":false}}
```

```json
{"type":"error","payload":{"code":"BAD_FRAME","message":"..."}}
```

Security notes:

- Gateway validates the JWT before accepting/bridging the flow.
- Gateway overwrites tenant from JWT claims; browser-supplied tenant is ignored.
- Frame size is capped.
- The bridge flow has a bounded timeout.
- Token-in-query posture is currently shared with the existing WebSocket pattern; production logs and reverse proxies must scrub query strings.

## Roles

Current role names used by the gateway/auth model:

- `superadmin`
- `tenant_admin`
- `workspace_admin`
- `cs_agent`

RBAC is enforced on gRPC through middleware. REST endpoints authenticate through JWT middleware; REST-to-gRPC RBAC parity should be verified and enforced as part of hardening.

## Pagination

List endpoints use protobuf `PageRequest` / `PageResponse` semantics. REST responses use the protojson camelCase shape.

Typical request fields:

```json
{
  "page": 1,
  "pageSize": 50
}
```

Typical response field:

```json
{
  "page": {
    "page": 1,
    "pageSize": 50,
    "total": 123
  }
}
```

## Error shape

REST handlers map gRPC status codes to HTTP status codes and return JSON error payloads. Treat response details as implementation-specific; client code should key on HTTP status and stable error codes where provided.

Do not expose raw internal errors containing secrets. Audit any `err.Error()` propagation before returning it to a browser or external caller.
