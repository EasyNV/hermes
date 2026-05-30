// ══════════════════════════════════════════════════════════════
// Enums — const objects provide both runtime values and types
// ══════════════════════════════════════════════════════════════

export const Role = { UNSPECIFIED: 'ROLE_UNSPECIFIED', SUPERADMIN: 'ROLE_SUPERADMIN', TENANT_ADMIN: 'ROLE_TENANT_ADMIN', WORKSPACE_ADMIN: 'ROLE_WORKSPACE_ADMIN', CS_AGENT: 'ROLE_CS_AGENT' } as const
export type Role = (typeof Role)[keyof typeof Role]

export const WaNumberStatus = { UNSPECIFIED: 'WA_NUMBER_STATUS_UNSPECIFIED', ACTIVE: 'WA_NUMBER_STATUS_ACTIVE', BANNED: 'WA_NUMBER_STATUS_BANNED', DISCONNECTED: 'WA_NUMBER_STATUS_DISCONNECTED', COOLDOWN: 'WA_NUMBER_STATUS_COOLDOWN' } as const
export type WaNumberStatus = (typeof WaNumberStatus)[keyof typeof WaNumberStatus]

export const ProxyType = { UNSPECIFIED: 'PROXY_TYPE_UNSPECIFIED', SOCKS5: 'PROXY_TYPE_SOCKS5', HTTP: 'PROXY_TYPE_HTTP' } as const
export type ProxyType = (typeof ProxyType)[keyof typeof ProxyType]

export const ProxyStatus = { UNSPECIFIED: 'PROXY_STATUS_UNSPECIFIED', ACTIVE: 'PROXY_STATUS_ACTIVE', DEAD: 'PROXY_STATUS_DEAD', FLAGGED: 'PROXY_STATUS_FLAGGED' } as const
export type ProxyStatus = (typeof ProxyStatus)[keyof typeof ProxyStatus]

export const CampaignStatus = { UNSPECIFIED: 'CAMPAIGN_STATUS_UNSPECIFIED', DRAFT: 'CAMPAIGN_STATUS_DRAFT', SCHEDULED: 'CAMPAIGN_STATUS_SCHEDULED', RUNNING: 'CAMPAIGN_STATUS_RUNNING', PAUSED: 'CAMPAIGN_STATUS_PAUSED', COMPLETED: 'CAMPAIGN_STATUS_COMPLETED', CANCELLED: 'CAMPAIGN_STATUS_CANCELLED' } as const
export type CampaignStatus = (typeof CampaignStatus)[keyof typeof CampaignStatus]

export const ContactSendStatus = { UNSPECIFIED: 'CONTACT_SEND_STATUS_UNSPECIFIED', PENDING: 'CONTACT_SEND_STATUS_PENDING', SENT: 'CONTACT_SEND_STATUS_SENT', DELIVERED: 'CONTACT_SEND_STATUS_DELIVERED', FAILED: 'CONTACT_SEND_STATUS_FAILED', SKIPPED: 'CONTACT_SEND_STATUS_SKIPPED' } as const
export type ContactSendStatus = (typeof ContactSendStatus)[keyof typeof ContactSendStatus]

export const RotationStrategy = { UNSPECIFIED: 'ROTATION_STRATEGY_UNSPECIFIED', ROUND_ROBIN: 'ROTATION_STRATEGY_ROUND_ROBIN', LEAST_USED: 'ROTATION_STRATEGY_LEAST_USED' } as const
export type RotationStrategy = (typeof RotationStrategy)[keyof typeof RotationStrategy]

export const ConversationStatus = { UNSPECIFIED: 'CONVERSATION_STATUS_UNSPECIFIED', UNASSIGNED: 'CONVERSATION_STATUS_UNASSIGNED', ASSIGNED: 'CONVERSATION_STATUS_ASSIGNED', CLOSED: 'CONVERSATION_STATUS_CLOSED' } as const
export type ConversationStatus = (typeof ConversationStatus)[keyof typeof ConversationStatus]

export const MessageDirection = { UNSPECIFIED: 'MESSAGE_DIRECTION_UNSPECIFIED', INBOUND: 'MESSAGE_DIRECTION_INBOUND', OUTBOUND: 'MESSAGE_DIRECTION_OUTBOUND' } as const
export type MessageDirection = (typeof MessageDirection)[keyof typeof MessageDirection]

export const MessageStatus = { UNSPECIFIED: 'MESSAGE_STATUS_UNSPECIFIED', PENDING: 'MESSAGE_STATUS_PENDING', SENT: 'MESSAGE_STATUS_SENT', DELIVERED: 'MESSAGE_STATUS_DELIVERED', READ: 'MESSAGE_STATUS_READ', FAILED: 'MESSAGE_STATUS_FAILED' } as const
export type MessageStatus = (typeof MessageStatus)[keyof typeof MessageStatus]

export const ContentType = { UNSPECIFIED: 'CONTENT_TYPE_UNSPECIFIED', TEXT: 'CONTENT_TYPE_TEXT', IMAGE: 'CONTENT_TYPE_IMAGE', DOCUMENT: 'CONTENT_TYPE_DOCUMENT', AUDIO: 'CONTENT_TYPE_AUDIO', VIDEO: 'CONTENT_TYPE_VIDEO' } as const
export type ContentType = (typeof ContentType)[keyof typeof ContentType]

export const NotificationType = { UNSPECIFIED: 'NOTIFICATION_TYPE_UNSPECIFIED', BROWSER_PUSH: 'NOTIFICATION_TYPE_BROWSER_PUSH', SOUND: 'NOTIFICATION_TYPE_SOUND', WEBHOOK: 'NOTIFICATION_TYPE_WEBHOOK' } as const
export type NotificationType = (typeof NotificationType)[keyof typeof NotificationType]

export const WebhookType = { UNSPECIFIED: 'WEBHOOK_TYPE_UNSPECIFIED', TELEGRAM: 'WEBHOOK_TYPE_TELEGRAM', DISCORD: 'WEBHOOK_TYPE_DISCORD', CUSTOM: 'WEBHOOK_TYPE_CUSTOM' } as const
export type WebhookType = (typeof WebhookType)[keyof typeof WebhookType]

// InboxChannel discriminates which messaging channel a Conversation /
// Message belongs to. Added in Stage E3 (chunk 1).
//   INBOX_CHANNEL_WA  → waNumberId is set, mbs* are empty.
//   INBOX_CHANNEL_MBS → mbsSessionUid + mbsThreadId are set,
//                       waNumberId is empty.
export const InboxChannel = {
  UNSPECIFIED: 'INBOX_CHANNEL_UNSPECIFIED',
  WA:          'INBOX_CHANNEL_WA',
  MBS:         'INBOX_CHANNEL_MBS',
} as const
export type InboxChannel = (typeof InboxChannel)[keyof typeof InboxChannel]

// Source of truth: proto/hermes/v1/mbs.proto::MbsSessionState. Values must
// stay in lockstep with internal/gateway/rest/handlers_mbs.go::parseStateFilter
// (E2 chunk 2) and the lifecycle frame emitted by internal/gateway/websocket/
// events_mbs.go (E2 chunk 3).
export const MbsSessionState = {
  UNSPECIFIED: 'MBS_SESSION_STATE_UNSPECIFIED',
  WARMING: 'MBS_SESSION_STATE_WARMING',
  ACTIVE: 'MBS_SESSION_STATE_ACTIVE',
  RECONNECTING: 'MBS_SESSION_STATE_RECONNECTING',
  REFRESHING: 'MBS_SESSION_STATE_REFRESHING',
  SUSPENDED: 'MBS_SESSION_STATE_SUSPENDED',
  BURNED: 'MBS_SESSION_STATE_BURNED',
} as const
export type MbsSessionState = (typeof MbsSessionState)[keyof typeof MbsSessionState]

// ══════════════════════════════════════════════════════════════
// Pagination
// ══════════════════════════════════════════════════════════════

export interface PageRequest {
  page?: number
  pageSize?: number
}

export interface PageResponse {
  total: number
  page: number
  pageSize: number
  totalPages: number
}

// ══════════════════════════════════════════════════════════════
// Resource Types (mirrors proto resource messages)
// ══════════════════════════════════════════════════════════════

export interface Tenant {
  id: string
  name: string
  settingsJson: string
  maxNumbersPerProxy: number
  createdAt: string
}

export interface Workspace {
  id: string
  tenantId: string
  name: string
  settingsJson: string
  dailyCap: number
  createdAt: string
}

export interface User {
  id: string
  workspaceId: string
  email: string
  role: Role
  createdAt: string
}

export interface WaNumber {
  id: string
  tenantId: string
  jid: string
  phone: string
  displayName: string
  status: WaNumberStatus
  proxyId: string
  healthScore: number
  dailySentCount: number
  totalSent: number
  banCount: number
  lastBanAt?: string
  connectedAt?: string
  podId: string
  createdAt: string
  workspaceIds: string[]
}

export interface Proxy {
  id: string
  tenantId: string
  host: string
  port: number
  username: string
  password: string
  type: ProxyType
  status: ProxyStatus
  banCount: number
  assignedCount: number
  lastHealthCheck?: string
  createdAt: string
}

export interface Contact {
  id: string
  tenantId: string
  phone: string
  name: string
  tags: string[]
  customFields: Record<string, string>
  isBanned: boolean
  createdAt: string
  updatedAt?: string
}

export interface Template {
  id: string
  workspaceId: string
  name: string
  body: string
  mediaUrl: string
  mediaType: string
  variables: string[]
  createdBy: string
  createdAt: string
}

export interface Campaign {
  id: string
  workspaceId: string
  templateId: string
  name: string
  status: CampaignStatus
  scheduleAt?: string
  dailyCapPerNum: number
  banPauseThreshold: number
  rotationStrategy: RotationStrategy
  delayMinMs: number
  delayMaxMs: number
  totalContacts: number
  sentCount: number
  failedCount: number
  repliedCount: number
  bannedCount: number
  createdBy: string
  createdAt: string
  startedAt?: string
  completedAt?: string
  // Stage F follow-up chunk 8 (2026-05-30) — dispatch channel.
  // 'wa' (default) | 'mbs'. Empty in wire payloads is treated as 'wa'
  // by the server for backward compat with old clients; new code should
  // always populate this.
  channel: 'wa' | 'mbs'
}

export interface CampaignNumber {
  campaignId: string
  waNumberId: string
  status: WaNumberStatus
  sentCount: number
  failedCount: number
}

export interface CampaignContact {
  campaignId: string
  contactId: string
  waNumberId: string
  status: ContactSendStatus
  sentAt?: string
  deliveredAt?: string
  failedAt?: string
  error: string
}

export interface Conversation {
  id: string
  workspaceId: string
  contactId: string
  waNumberId: string
  assignedTo: string
  status: ConversationStatus
  lastMessageAt?: string
  campaignId: string
  createdAt: string
  contactName: string
  contactPhone: string
  lastMessagePreview: string
  unreadCount: number
  firstResponseTimeSecs: number
  // E3 chunk 1: channel discriminator + MBS-specific keys
  channel: InboxChannel
  mbsSessionUid: string
  mbsThreadId: string
  mbsPageId: string
}

export interface Message {
  id: string
  conversationId: string
  direction: MessageDirection
  contentType: ContentType
  body: string
  mediaUrl: string
  templateId: string
  resolvedVarsJson: string
  waMessageId: string
  status: MessageStatus
  createdAt: string
  // E3 chunk 1: Meta MID for MBS messages (empty for WA)
  mbsMid: string
}

export interface CannedResponse {
  id: string
  workspaceId: string
  shortcut: string
  body: string
  createdBy: string
  createdAt: string
}

export interface NotificationConfig {
  id: string
  workspaceId: string
  type: NotificationType
  webhookUrl: string
  webhookType: WebhookType
  enabled: boolean
  createdAt: string
}

export interface ErrorDetail {
  code: string
  message: string
  field: string
}

// ══════════════════════════════════════════════════════════════
// Response Types (composite or list responses)
// ══════════════════════════════════════════════════════════════

// Auth
export interface LoginResponse {
  accessToken: string
  refreshToken: string
  expiresIn: number
  user: User
}

export interface RefreshTokenResponse {
  accessToken: string
  refreshToken: string
  expiresIn: number
}

export interface GetMeResponse {
  user: User
  workspace: Workspace
  tenant: Tenant
}

// Tenants
export interface ListTenantsResponse { tenants: Tenant[]; pagination: PageResponse }
// Workspaces
export interface ListWorkspacesResponse { workspaces: Workspace[]; pagination: PageResponse }
// Users
export interface ListUsersResponse { users: User[]; pagination: PageResponse }

// WA Numbers
export interface RegisterWaNumberResponse { waNumber: WaNumber; qrCode: string }
export interface GetQRCodeResponse { qrCode: string; isLinked: boolean }
export interface ListWaNumbersResponse { waNumbers: WaNumber[]; pagination: PageResponse }

// ══════════════════════════════════════════════════════════════
// MBS — Meta Business Suite resources + REST shapes
// (Stage E2 chunk 4. REST routes live under /api/v1/mbs-sessions.)
// ══════════════════════════════════════════════════════════════

export interface MbsSession {
  uid: string
  fbid: string
  state: MbsSessionState
  podId: string
  lastConnackRc: number
  createdAt: string
  lastSeenAt: string
  cookieExpiresAt: string
  burnedAt: string         // empty unless state === BURNED
  burnedReason: string
  // Primary asset embedded inline by listMbsSessions / getMbsSessionStatus.
  // Added Stage F follow-up chunk 8 (2026-05-30) — campaign picker reads
  // wecPhoneNumber + wecAccountRegistered to decide pickability.
  // Optional: a session in 'bridging' state may not yet have a primary
  // asset (Stage B.2 sets is_primary on a row in mbs_session_assets only
  // after the WEC verification roundtrip).
  primaryAsset?: MbsAsset
}

// MbsAsset — wire shape from proto MbsAsset (proto/hermes/v1/mbs.proto).
//
// Source-of-truth field list and numbers live in the .proto. This is
// hand-rolled because the frontend doesn't pull from gen/ts (the
// build only generates Go).
//
// Stage F follow-up chunk 4 (2026-05-30): replaced the speculative
// {uid, kind, externalId, displayName, metadata} type that never
// matched the actual API response. The old shape would have required
// a server change none of us shipped — the assets endpoint has
// always returned the proto field set below.
export interface MbsAsset {
  pageId: string
  pageName: string
  wabaId: string
  wecMailboxId: string
  wecPhoneNumber: string
  businessPresenceNodeId: string
  igAccountId: string
  hasWaba: boolean
  // Added wire-side in chunk 4 — fields existed in mbs_session_assets
  // but were dropped by proto_conv until 2026-05-30.
  businessId: string
  businessName: string
  isPrimary: boolean
  wecAccountRegistered: boolean
}

export interface MbsListSessionsResponse { sessions: MbsSession[]; pagination: PageResponse }
export interface MbsGetSessionStatusResponse { session: MbsSession }
export interface MbsListSessionAssetsResponse { assets: MbsAsset[] }
export interface MbsBurnSessionResponse { session: MbsSession }
export interface MbsResolvePhoneResponse {
  threadId: string         // FBID-keyed thread id; empty if exists=false
  pageId: string
  exists: boolean
}
export interface MbsSendMessageResponse {
  mid: string              // wire-confirmed message id
  otid: string             // client-side outbound transaction id (echo)
  threadId: string
  sentAt: string
}

// Proxies
export interface ProxyInput { host: string; port: number; username: string; password: string; type: ProxyType }
export interface AddProxiesResponse { proxies: Proxy[]; skippedCount: number }
export interface ListProxiesResponse { proxies: Proxy[]; pagination: PageResponse }
export interface GetProxyHealthResponse { proxy: Proxy; latencyMs: number; reachable: boolean; checkedAt: string }
export interface GetBestProxyResponse { proxy: Proxy; poolExhausted: boolean }

// Contacts
export interface CreateContactResponse { contact: Contact; alreadyExisted: boolean }
export interface ImportError { row: number; phone: string; error: string }
export interface ImportContactsResponse { importedCount: number; skippedCount: number; failedCount: number; errors: ImportError[]; bannedCount: number }
export interface ListContactsResponse { contacts: Contact[]; pagination: PageResponse }

// Templates
export interface ListTemplatesResponse { templates: Template[]; pagination: PageResponse }

// Campaigns
export interface GetCampaignResponse { campaign: Campaign; numbers: CampaignNumber[]; template: Template }
export interface ListCampaignsResponse { campaigns: Campaign[]; pagination: PageResponse }
export interface CampaignContactDetail { campaignContact: CampaignContact; contact: Contact }
export interface ListCampaignContactsResponse { contacts: CampaignContactDetail[]; pagination: PageResponse }
export interface CampaignNumberDetail { campaignNumber: CampaignNumber; phone: string; displayName: string; currentStatus: WaNumberStatus }
export interface ListCampaignNumbersResponse { numbers: CampaignNumberDetail[]; pagination: PageResponse }

// Inbox
export interface ListConversationsResponse { conversations: Conversation[]; pagination: PageResponse }
export interface GetConversationResponse { conversation: Conversation; contact: Contact; waNumber: WaNumber }
export interface ListMessagesResponse { messages: Message[]; pagination: PageResponse }
export interface SearchMessageHit { message: Message; conversationId: string; contactName: string; highlight: string }
export interface SearchMessagesResponse { hits: SearchMessageHit[]; pagination: PageResponse }
export interface ContactCampaignSummary { campaignId: string; campaignName: string; templateId: string; templateName: string; resolvedBody: string; status: ContactSendStatus; sentAt?: string; deliveredAt?: string }
export interface GetContactCampaignHistoryResponse { campaigns: ContactCampaignSummary[]; pagination: PageResponse }
export interface AgentPerformanceMetrics { userId: string; email: string; avgResponseTimeSecs: number; medianResponseTimeSecs: number; totalConversations: number; activeConversations: number; messagesSent: number }
export interface GetAgentPerformanceResponse { agents: AgentPerformanceMetrics[] }

// Canned Responses
export interface ListCannedResponsesResponse { cannedResponses: CannedResponse[]; pagination: PageResponse }

// Notifications
export interface ListNotificationConfigsResponse { configs: NotificationConfig[] }
export interface TestNotificationResponse { success: boolean; error: string }

// Dashboard
export interface GetDashboardStatsResponse {
  activeNumbers: number
  totalNumbers: number
  messagesSentToday: number
  messagesReceivedToday: number
  activeCampaigns: number
  unassignedConversations: number
  activeProxies: number
  totalProxies: number
  bansToday: number
  totalContacts: number
}

// ══════════════════════════════════════════════════════════════
// WebSocket Event Types
// ══════════════════════════════════════════════════════════════

export interface WsNewMessagePayload {
  conversationId: string
  message: Message
  contact: { id: string; name: string; phone: string }
  waNumberId: string
  waNumberPhone: string
  isNewConversation: boolean
  conversationStatus: ConversationStatus
  assignedTo: string
}

export interface WsMessageStatusPayload {
  conversationId: string
  messageId: string
  waMessageId: string
  status: MessageStatus
  updatedAt: string
}

export interface WsConversationUpdatedPayload {
  conversationId: string
  status: ConversationStatus
  assignedTo: string
  assignedToName: string
  updatedBy: string
  action: 'claimed' | 'transferred' | 'closed' | 'reopened' | 'created'
}

export interface WsCampaignProgressPayload {
  campaignId: string
  totalContacts: number
  sentCount: number
  deliveredCount: number
  failedCount: number
  repliedCount: number
  bannedCount: number
  progressPercent: number
  sendRatePerMin: number
  etaSeconds: number
  numberProgress: Array<{
    waNumberId: string
    phone: string
    status: WaNumberStatus
    sentCount: number
    failedCount: number
  }>
}

export interface WsCampaignStatusPayload {
  campaignId: string
  previousStatus: CampaignStatus
  newStatus: CampaignStatus
  reason: string
  timestamp: string
}

export interface WsNumberStatusPayload {
  waNumberId: string
  phone: string
  displayName: string
  status: WaNumberStatus
  previousStatus: WaNumberStatus
  podId: string
  timestamp: string
}

export interface WsBanDetectedPayload {
  waNumberId: string
  phone: string
  displayName: string
  proxyId: string
  proxyHost: string
  detectedAt: string
  activeCampaignsAffected: string[]
  waReason: string
}

export interface WsTypingIndicatorPayload {
  conversationId: string
  contactJid: string
  isComposing: boolean
}

export interface WsImportCompletePayload {
  filename: string
  importedCount: number
  skippedCount: number
  updatedCount: number
  failedCount: number
}

export interface WsNotificationAlertPayload {
  category: string
  title: string
  body: string
  workspaceId: string
}

// ── MBS WS payloads ─────────────────────────────────────────────
// Wire shape locked by internal/gateway/websocket/events_mbs.go
// (Stage E2 chunk 3). Field names must match the gateway handler
// VERBATIM — drift breaks runtime.

export interface WsMbsNewMessagePayload {
  uid: string                  // int64 → decimal string for JS safety
  pageId: string
  wecMailboxId: string
  threadId: string
  mid: string
  senderPhone: string
  text: string
  receivedAt: string           // ISO 8601 UTC
}

export interface WsMbsOutboundStatusPayload {
  uid: string
  threadId: string
  mid: string
  otid: string                 // client-side outbound transaction id
  latencyMs: number
  ok: boolean
  error: string                // empty on success
  sentAt: string
}

export interface WsMbsSessionLifecyclePayload {
  uid: string
  previousState: MbsSessionState
  newState: MbsSessionState
  reason: string
  lastConnackRc: number
  podId: string
  timestamp: string
}

export type WsEvent =
  | { type: 'new_message'; payload: WsNewMessagePayload }
  | { type: 'message_status_updated'; payload: WsMessageStatusPayload }
  | { type: 'conversation_updated'; payload: WsConversationUpdatedPayload }
  | { type: 'campaign_progress'; payload: WsCampaignProgressPayload }
  | { type: 'campaign_status_changed'; payload: WsCampaignStatusPayload }
  | { type: 'number_status_changed'; payload: WsNumberStatusPayload }
  | { type: 'ban_detected'; payload: WsBanDetectedPayload }
  | { type: 'typing_indicator'; payload: WsTypingIndicatorPayload }
  | { type: 'import_complete'; payload: WsImportCompletePayload }
  | { type: 'notification_alert'; payload: WsNotificationAlertPayload }
  | { type: 'mbs_new_message'; payload: WsMbsNewMessagePayload }
  | { type: 'mbs_outbound_status'; payload: WsMbsOutboundStatusPayload }
  | { type: 'mbs_session_lifecycle'; payload: WsMbsSessionLifecyclePayload }
  | { type: 'connected'; payload: { userId: string; workspaceId: string; tenantId: string } }
  | { type: 'pong'; payload: { serverTime: string } }
  | { type: 'auth_ok' }
  | { type: 'subscribed'; payload: { conversationId: string } }
  | { type: 'error'; payload: { code: string; message: string } }
