# Hermès REST API Reference

76 endpoints on port **8081** under `/api/v1/`. All responses are JSON. Auth via `Authorization: Bearer <jwt>` header.

---

## Auth (4 endpoints)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/auth/login` | No | Login with email + password |
| POST | `/api/v1/auth/refresh` | No | Refresh JWT tokens |
| POST | `/api/v1/auth/logout` | Yes | Invalidate refresh tokens |
| GET | `/api/v1/auth/me` | Yes | Get current user profile |

### Login
```bash
curl -X POST http://localhost:8081/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@hermes.local","password":"admin123"}'
# → {"accessToken":"eyJ...","refreshToken":"uuid","expiresIn":900,"user":{...}}
```

## Dashboard (1 endpoint)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/dashboard/stats?tenantId=&workspaceId=` | Yes | Aggregate stats |

## Tenants (4 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/tenants` | Yes | superadmin | Create tenant |
| GET | `/api/v1/tenants` | Yes | superadmin, tenant_admin | List tenants |
| GET | `/api/v1/tenants/{id}` | Yes | superadmin, tenant_admin | Get tenant |
| PUT | `/api/v1/tenants/{id}` | Yes | superadmin, tenant_admin | Update tenant |

## Workspaces (5 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/workspaces` | Yes | superadmin, tenant_admin | Create workspace |
| GET | `/api/v1/workspaces?tenantId=` | Yes | all | List workspaces |
| GET | `/api/v1/workspaces/{id}` | Yes | all | Get workspace |
| PUT | `/api/v1/workspaces/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Update |
| DELETE | `/api/v1/workspaces/{id}` | Yes | superadmin, tenant_admin | Delete |

## Users (5 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/users` | Yes | superadmin, tenant_admin, workspace_admin | Create user |
| GET | `/api/v1/users?workspaceId=` | Yes | superadmin, tenant_admin, workspace_admin | List users |
| GET | `/api/v1/users/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Get user |
| PUT | `/api/v1/users/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Update user |
| DELETE | `/api/v1/users/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Delete user |

## WhatsApp Numbers (9 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/wa-numbers` | Yes | superadmin, tenant_admin | Register number (triggers QR) |
| GET | `/api/v1/wa-numbers?tenantId=&workspaceId=&status=` | Yes | all | List numbers |
| GET | `/api/v1/wa-numbers/{id}` | Yes | all | Get number details |
| GET | `/api/v1/wa-numbers/{id}/qr-code` | Yes | superadmin, tenant_admin | Get QR code (PNG base64) |
| PUT | `/api/v1/wa-numbers/{id}` | Yes | superadmin, tenant_admin | Update name/proxy/workspaces |
| POST | `/api/v1/wa-numbers/{id}/disconnect` | Yes | superadmin, tenant_admin | Disconnect session |
| POST | `/api/v1/wa-numbers/{id}/reconnect` | Yes | superadmin, tenant_admin | Reconnect session |
| DELETE | `/api/v1/wa-numbers/{id}` | Yes | superadmin, tenant_admin | Delete number |
| POST | `/api/v1/wa-numbers/{id}/pair-phone` | Yes | superadmin, tenant_admin | Phone number pairing |

### Register Number
```bash
curl -X POST http://localhost:8081/api/v1/wa-numbers \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"phone":"628123456789","displayName":"Sales Bot","workspaceIds":["ws-uuid"]}'
# → {"waNumber":{...},"qrCode":"iVBORw0K..."}  # base64 PNG
```

### Phone Pairing (alternative to QR)
```bash
curl -X POST http://localhost:8081/api/v1/wa-numbers/{id}/pair-phone \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"phoneNumber":"628123456789"}'
# → {"pairingCode":"ABCD-EFGH"}
# User enters code: WhatsApp → Linked Devices → Link with phone number
```

## Proxies (7 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/proxies` | Yes | superadmin, tenant_admin | Bulk add proxies |
| GET | `/api/v1/proxies?tenantId=&status=` | Yes | superadmin, tenant_admin | List proxies |
| GET | `/api/v1/proxies/best?tenantId=` | Yes | superadmin, tenant_admin | Get best proxy |
| GET | `/api/v1/proxies/{id}/health` | Yes | superadmin, tenant_admin | Health check |
| PUT | `/api/v1/proxies/{id}` | Yes | superadmin, tenant_admin | Update proxy |
| DELETE | `/api/v1/proxies/{id}` | Yes | superadmin, tenant_admin | Delete proxy |
| POST | `/api/v1/proxies/assign` | Yes | superadmin, tenant_admin | Assign proxy to number |

## Contacts (7 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/contacts` | Yes | superadmin, tenant_admin, workspace_admin | Create contact |
| POST | `/api/v1/contacts/import` | Yes | superadmin, tenant_admin, workspace_admin | CSV import |
| GET | `/api/v1/contacts?tenantId=&search=&tags=` | Yes | all | List contacts |
| GET | `/api/v1/contacts/{id}` | Yes | all | Get contact |
| PUT | `/api/v1/contacts/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Update |
| DELETE | `/api/v1/contacts/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Delete |
| GET | `/api/v1/contacts/{id}/campaigns` | Yes | all | Campaign history |

## Templates (5 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/templates` | Yes | superadmin, tenant_admin, workspace_admin | Create template |
| GET | `/api/v1/templates?workspaceId=&search=` | Yes | all | List templates |
| GET | `/api/v1/templates/{id}` | Yes | all | Get template |
| PUT | `/api/v1/templates/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Update |
| DELETE | `/api/v1/templates/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Delete |

## Campaigns (11 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/campaigns` | Yes | superadmin, tenant_admin, workspace_admin | Create campaign |
| GET | `/api/v1/campaigns?workspaceId=&status=` | Yes | all | List campaigns |
| GET | `/api/v1/campaigns/{id}` | Yes | all | Get campaign + numbers + template |
| POST | `/api/v1/campaigns/{id}/start` | Yes | superadmin, tenant_admin, workspace_admin | Start campaign |
| POST | `/api/v1/campaigns/{id}/pause` | Yes | superadmin, tenant_admin, workspace_admin | Pause |
| POST | `/api/v1/campaigns/{id}/resume` | Yes | superadmin, tenant_admin, workspace_admin | Resume |
| POST | `/api/v1/campaigns/{id}/cancel` | Yes | superadmin, tenant_admin, workspace_admin | Cancel |
| PUT | `/api/v1/campaigns/{id}/numbers` | Yes | superadmin, tenant_admin, workspace_admin | Assign numbers |
| PUT | `/api/v1/campaigns/{id}/contacts` | Yes | superadmin, tenant_admin, workspace_admin | Assign contacts |
| GET | `/api/v1/campaigns/{id}/contacts?status=` | Yes | all | List campaign contacts |
| GET | `/api/v1/campaigns/{id}/numbers` | Yes | all | List campaign numbers |

### Create + Start Campaign
```bash
# Create
curl -X POST http://localhost:8081/api/v1/campaigns \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"workspaceId":"ws-uuid","templateId":"tpl-uuid","name":"Promo Q2",
       "waNumberIds":["num-uuid"],"contactIds":["c-uuid1","c-uuid2"],
       "delayMinMs":3000,"delayMaxMs":15000}'

# Start
curl -X POST http://localhost:8081/api/v1/campaigns/{id}/start \
  -H "Authorization: Bearer $TOKEN"
```

## Inbox — Conversations (9 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| GET | `/api/v1/conversations?workspaceId=&status=&assignedTo=&search=` | Yes | all | List conversations |
| GET | `/api/v1/conversations/{id}` | Yes | all | Get conversation + contact + WA number |
| POST | `/api/v1/conversations/{id}/claim` | Yes | workspace_admin, cs_agent | Claim conversation |
| POST | `/api/v1/conversations/{id}/transfer` | Yes | workspace_admin, cs_agent | Transfer to agent |
| POST | `/api/v1/conversations/{id}/close` | Yes | workspace_admin, cs_agent | Close conversation |
| GET | `/api/v1/conversations/{id}/messages?page=&pageSize=` | Yes | all | List messages |
| POST | `/api/v1/conversations/{id}/messages` | Yes | workspace_admin, cs_agent | Send reply |
| POST | `/api/v1/conversations/{id}/typing` | Yes | all | Send typing indicator |
| POST | `/api/v1/messages/search` | Yes | all | Full-text message search |

### Send Reply
```bash
curl -X POST http://localhost:8081/api/v1/conversations/{id}/messages \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"contentType":"CONTENT_TYPE_TEXT","body":"Hello from Hermes!"}'
# → {"message":{"id":"...","status":"MESSAGE_STATUS_PENDING",...}}
# Status updates: pending → sent → delivered (async via whatsmeow)
```

## Agent Performance (1 endpoint)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/agent-performance?workspaceId=&userId=` | Yes | Agent metrics |

## Canned Responses (4 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/canned-responses` | Yes | superadmin, tenant_admin, workspace_admin | Create |
| GET | `/api/v1/canned-responses?workspaceId=&search=` | Yes | all | List |
| PUT | `/api/v1/canned-responses/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Update |
| DELETE | `/api/v1/canned-responses/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Delete |

## Notifications (4 endpoints)

| Method | Path | Auth | Roles | Description |
|--------|------|------|-------|-------------|
| POST | `/api/v1/notifications` | Yes | superadmin, tenant_admin, workspace_admin | Configure |
| GET | `/api/v1/notifications?workspaceId=` | Yes | superadmin, tenant_admin, workspace_admin | List |
| POST | `/api/v1/notifications/{id}/test` | Yes | superadmin, tenant_admin, workspace_admin | Test |
| DELETE | `/api/v1/notifications/{id}` | Yes | superadmin, tenant_admin, workspace_admin | Delete |

---

## WebSocket Events

Connect: `ws://localhost:8081/ws?token=<jwt>`

### Client → Server
| Type | Payload | Description |
|------|---------|-------------|
| `ping` | — | Heartbeat |
| `auth` | `{token}` | Re-authenticate |
| `subscribe_conversation` | `{conversation_id}` | Subscribe to conversation events |
| `unsubscribe_conversation` | `{conversation_id}` | Unsubscribe |

### Server → Client
| Type | Scope | Description |
|------|-------|-------------|
| `new_message` | tenant | Inbound WhatsApp message |
| `message_status_updated` | tenant | Delivery status change |
| `number_status_changed` | tenant | WA number connection change |
| `ban_detected` | tenant | Number banned |
| `campaign_status_changed` | workspace | Campaign state transition |
| `campaign_progress` | workspace | Campaign delivery progress |
| `import_complete` | user | CSV import finished |
| `typing_indicator` | tenant | Contact typing |

---

## RBAC Roles

`superadmin` > `tenant_admin` > `workspace_admin` > `cs_agent`

- **superadmin**: Full platform access
- **tenant_admin**: Manages own tenant's workspaces, numbers, proxies
- **workspace_admin**: Manages campaigns, templates, contacts, inbox
- **cs_agent**: Inbox only (unassigned + own conversations), can send replies

## Pagination

All list endpoints accept `?page=1&pageSize=50`. Response includes:
```json
{"pagination":{"total":100,"page":1,"pageSize":50,"totalPages":2}}
```
