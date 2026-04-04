# Hermès API Reference

Complete gRPC API reference for the `HermesGateway` service — the only service the frontend communicates with. 75 RPCs across 11 domains.

**Transport:** gRPC (port 8080) + WebSocket (port 8081)
**Auth:** JWT Bearer token via `Authorization` metadata (gRPC) or `?token=` query param (WebSocket)

---

## Auth (4 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `Login` | any | Authenticate with email + password. Returns JWT access + refresh tokens. |
| `RefreshToken` | any (valid refresh) | Exchange refresh token for new access + refresh pair. |
| `Logout` | any authenticated | Invalidate current session's refresh token. |
| `GetMe` | any authenticated | Return current user's profile and permissions. |

## Tenant Management (4 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `CreateTenant` | superadmin | Create a new tenant. |
| `GetTenant` | superadmin, tenant_admin | Get tenant by ID. |
| `ListTenants` | superadmin (all), tenant_admin (own) | List tenants. |
| `UpdateTenant` | superadmin, tenant_admin (own) | Update tenant settings. |

## Workspace Management (5 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `CreateWorkspace` | superadmin, tenant_admin | Create workspace under a tenant. |
| `GetWorkspace` | all roles (own workspace) | Get workspace by ID. |
| `ListWorkspaces` | all roles (scoped) | List workspaces within a tenant. |
| `UpdateWorkspace` | superadmin, tenant_admin, workspace_admin | Update workspace settings. |
| `DeleteWorkspace` | superadmin, tenant_admin | Delete a workspace. |

## User Management (5 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `CreateUser` | superadmin, tenant_admin, workspace_admin | Create user (cannot create higher role). |
| `GetUser` | superadmin, tenant_admin, workspace_admin, self | Get user by ID. |
| `ListUsers` | superadmin, tenant_admin, workspace_admin | List users in workspace. |
| `UpdateUser` | superadmin, tenant_admin, workspace_admin | Update user (cannot promote above own role). |
| `DeleteUser` | superadmin, tenant_admin, workspace_admin | Delete user (cannot delete higher role). |

## WhatsApp Numbers (8 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `RegisterWaNumber` | superadmin, tenant_admin | Register a new WA number for QR linking. |
| `GetQRCode` | superadmin, tenant_admin | Get QR code for an unlinked number. |
| `ListWaNumbers` | all roles (own workspace) | List WA numbers in workspace. |
| `GetWaNumber` | all roles (own workspace) | Get WA number details + connection state. |
| `UpdateWaNumber` | superadmin, tenant_admin | Update number settings. |
| `DisconnectWaNumber` | superadmin, tenant_admin | Disconnect a WA session. |
| `ReconnectWaNumber` | superadmin, tenant_admin | Reconnect a previously linked number. |
| `DeleteWaNumber` | superadmin, tenant_admin | Delete a WA number (disconnects first). |

## Proxy Management (7 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `AddProxies` | superadmin, tenant_admin | Bulk-add proxies (protocol://host:port:user:pass). |
| `ListProxies` | superadmin, tenant_admin | List proxies with health stats. |
| `UpdateProxy` | superadmin, tenant_admin | Update proxy settings. |
| `DeleteProxy` | superadmin, tenant_admin | Delete a proxy (unassigns first). |
| `AssignProxy` | superadmin, tenant_admin | Assign proxy to a WA number. |
| `GetProxyHealth` | superadmin, tenant_admin | Get health check results for a proxy. |
| `GetBestProxy` | superadmin, tenant_admin | Get the best available proxy (lowest latency, least assigned). |

## Contacts (6 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `CreateContact` | superadmin, tenant_admin, workspace_admin | Create a single contact. |
| `ImportContacts` | superadmin, tenant_admin, workspace_admin | CSV bulk import (async, result via WS `import_complete`). |
| `ListContacts` | all roles | List contacts with search + tag filtering. |
| `GetContact` | all roles | Get contact by ID. |
| `UpdateContact` | superadmin, tenant_admin, workspace_admin | Update contact details/tags. |
| `DeleteContact` | superadmin, tenant_admin, workspace_admin | Delete a contact. |

## Templates (5 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `CreateTemplate` | superadmin, tenant_admin, workspace_admin | Create message template (supports spintax). |
| `GetTemplate` | all roles | Get template by ID. |
| `ListTemplates` | all roles | List templates in workspace. |
| `UpdateTemplate` | superadmin, tenant_admin, workspace_admin | Update template body/variables. |
| `DeleteTemplate` | superadmin, tenant_admin, workspace_admin | Delete a template. |

## Campaigns (11 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `CreateCampaign` | superadmin, tenant_admin, workspace_admin | Create campaign (draft state). |
| `GetCampaign` | all roles | Get campaign details + stats. |
| `ListCampaigns` | all roles | List campaigns with status filter. |
| `StartCampaign` | superadmin, tenant_admin, workspace_admin | Start a draft/paused campaign. |
| `PauseCampaign` | superadmin, tenant_admin, workspace_admin | Pause a running campaign. |
| `ResumeCampaign` | superadmin, tenant_admin, workspace_admin | Resume a paused campaign. |
| `CancelCampaign` | superadmin, tenant_admin, workspace_admin | Cancel a campaign (permanent). |
| `UpdateCampaignNumbers` | superadmin, tenant_admin, workspace_admin | Set WA numbers for a campaign. |
| `UpdateCampaignContacts` | superadmin, tenant_admin, workspace_admin | Set contact list for a campaign. |
| `ListCampaignContacts` | all roles | List contacts assigned to a campaign. |
| `ListCampaignNumbers` | all roles | List numbers assigned to a campaign. |

## Inbox (10 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `ListConversations` | all roles (cs_agent: assigned only) | List conversations with status/assignee filter. |
| `GetConversation` | all roles | Get conversation details. |
| `ClaimConversation` | workspace_admin, cs_agent | Claim unassigned conversation. |
| `TransferConversation` | workspace_admin, cs_agent (assigned) | Transfer to another agent. |
| `CloseConversation` | workspace_admin, cs_agent (assigned) | Close a conversation. |
| `ListMessages` | all roles | List messages in a conversation. |
| `SendMessage` | workspace_admin, cs_agent (assigned) | Send a reply message. |
| `SearchMessages` | all roles | Full-text search across messages. |
| `SendTypingIndicator` | all roles | Send typing indicator to contact. |
| `GetContactCampaignHistory` | all roles | Get a contact's campaign interaction history. |

## Dashboard & Canned Responses (6 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `GetDashboardStats` | all roles | Get workspace stats (numbers, messages, campaigns, bans). |
| `GetAgentPerformance` | superadmin, tenant_admin, workspace_admin | Get agent performance metrics. |
| `CreateCannedResponse` | superadmin, tenant_admin, workspace_admin | Create a quick-reply template. |
| `ListCannedResponses` | all roles | List canned responses in workspace. |
| `UpdateCannedResponse` | superadmin, tenant_admin, workspace_admin | Update a canned response. |
| `DeleteCannedResponse` | superadmin, tenant_admin, workspace_admin | Delete a canned response. |

## Notifications (4 RPCs)

| RPC | Roles | Description |
|-----|-------|-------------|
| `ConfigureNotification` | superadmin, tenant_admin, workspace_admin | Set up webhook/push notification channel. |
| `ListNotificationConfigs` | superadmin, tenant_admin, workspace_admin | List notification configs. |
| `TestNotification` | superadmin, tenant_admin, workspace_admin | Send a test notification. |
| `DeleteNotificationConfig` | superadmin, tenant_admin, workspace_admin | Delete notification config. |

---

## WebSocket Events

Connect to `ws://gateway:8081/ws?token=<JWT>` for real-time events.

### Client → Server Messages

| Type | Payload | Description |
|------|---------|-------------|
| `ping` | — | Heartbeat ping (server replies `pong`). |
| `auth` | `{ token }` | Re-authenticate with new JWT. |
| `subscribe_conversation` | `{ conversation_id }` | Subscribe to per-conversation events. |
| `unsubscribe_conversation` | `{ conversation_id }` | Unsubscribe from conversation events. |

### Server → Client Events

| Type | Source | Scope | Description |
|------|--------|-------|-------------|
| `connected` | hub | user | Connection established, includes user/tenant/workspace IDs. |
| `auth_ok` | hub | user | Re-authentication successful. |
| `new_message` | `hermes.wa.message.inbound.*` | tenant | Inbound WhatsApp message received. |
| `message_status_updated` | `hermes.wa.message.outbound.*` | tenant | Outbound message delivery status change. |
| `number_status_changed` | `hermes.wa.connection.*` | tenant | WA number connection state change. |
| `ban_detected` | `hermes.wa.ban.*` | tenant | WA number ban detected. |
| `campaign_status_changed` | `hermes.campaign.status.*` | workspace | Campaign state transition. |
| `campaign_progress` | `hermes.campaign.progress.*` | workspace | Campaign delivery progress update. |
| `import_complete` | `hermes.contacts.import.done.*` | user | CSV import finished. |
| `typing_indicator` | `hermes.wa.presence.*` | tenant | Contact typing indicator. |

### WebSocket Lifecycle

1. Connect with JWT token (query param or header)
2. Receive `connected` event with session info
3. Subscribe to conversations as needed
4. Receive events scoped by tenant/workspace
5. Heartbeat: server pings every 30s, closes after 3 missed pongs (90s)
6. Max 3 connections per user

---

## RBAC Matrix

4 roles: `superadmin` > `tenant_admin` > `workspace_admin` > `cs_agent`

| Domain | superadmin | tenant_admin | workspace_admin | cs_agent |
|--------|:---:|:---:|:---:|:---:|
| Auth (login/logout/me) | ✅ | ✅ | ✅ | ✅ |
| Tenants (CRUD) | ✅ | ✅ (own) | ❌ | ❌ |
| Workspaces (read) | ✅ | ✅ | ✅ | ✅ |
| Workspaces (write) | ✅ | ✅ | ✅ (own) | ❌ |
| Users (CRUD) | ✅ | ✅ | ✅ (own WS) | ❌ |
| WA Numbers | ✅ | ✅ | ❌ | ❌ |
| Proxies | ✅ | ✅ | ❌ | ❌ |
| Contacts (read) | ✅ | ✅ | ✅ | ✅ |
| Contacts (write) | ✅ | ✅ | ✅ | ❌ |
| Templates (read) | ✅ | ✅ | ✅ | ✅ |
| Templates (write) | ✅ | ✅ | ✅ | ❌ |
| Campaigns (read) | ✅ | ✅ | ✅ | ✅ |
| Campaigns (write/control) | ✅ | ✅ | ✅ | ❌ |
| Conversations (read) | ✅ | ✅ | ✅ | ✅ (own) |
| Conversations (claim/close/send) | ❌ | ❌ | ✅ | ✅ |
| Canned Responses (read) | ✅ | ✅ | ✅ | ✅ |
| Canned Responses (write) | ✅ | ✅ | ✅ | ❌ |
| Notifications | ✅ | ✅ | ✅ | ❌ |
| Dashboard stats | ✅ | ✅ | ✅ | ✅ |

## Pagination

All list endpoints accept `PageRequest { page, page_size }` and return `PageResponse { page, page_size, total_items, total_pages }`.

Default page size: 20. Max page size: 100.
