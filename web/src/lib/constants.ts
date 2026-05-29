import type { WaNumberStatus, ProxyStatus, CampaignStatus, ConversationStatus, ContactSendStatus, MessageStatus, Role, MbsSessionState } from '@/api/types'

export type StatusVariant = 'success' | 'warning' | 'destructive' | 'secondary' | 'default' | 'info'

interface StatusConfig {
  label: string
  variant: StatusVariant
  dot: string
}

export const WA_STATUS: Record<WaNumberStatus, StatusConfig> = {
  WA_NUMBER_STATUS_UNSPECIFIED: { label: 'Unknown', variant: 'secondary', dot: 'bg-gray-400' },
  WA_NUMBER_STATUS_ACTIVE: { label: 'Active', variant: 'success', dot: 'bg-green-500' },
  WA_NUMBER_STATUS_BANNED: { label: 'Banned', variant: 'destructive', dot: 'bg-red-500' },
  WA_NUMBER_STATUS_DISCONNECTED: { label: 'Disconnected', variant: 'secondary', dot: 'bg-gray-400' },
  WA_NUMBER_STATUS_COOLDOWN: { label: 'Cooldown', variant: 'warning', dot: 'bg-yellow-500' },
}

export const PROXY_STATUS: Record<ProxyStatus, StatusConfig> = {
  PROXY_STATUS_UNSPECIFIED: { label: 'Unknown', variant: 'secondary', dot: 'bg-gray-400' },
  PROXY_STATUS_ACTIVE: { label: 'Active', variant: 'success', dot: 'bg-green-500' },
  PROXY_STATUS_DEAD: { label: 'Dead', variant: 'destructive', dot: 'bg-red-500' },
  PROXY_STATUS_FLAGGED: { label: 'Flagged', variant: 'warning', dot: 'bg-yellow-500' },
}

export const CAMPAIGN_STATUS: Record<CampaignStatus, StatusConfig> = {
  CAMPAIGN_STATUS_UNSPECIFIED: { label: 'Unknown', variant: 'secondary', dot: 'bg-gray-400' },
  CAMPAIGN_STATUS_DRAFT: { label: 'Draft', variant: 'secondary', dot: 'bg-gray-400' },
  CAMPAIGN_STATUS_SCHEDULED: { label: 'Scheduled', variant: 'info', dot: 'bg-blue-500' },
  CAMPAIGN_STATUS_RUNNING: { label: 'Running', variant: 'success', dot: 'bg-green-500' },
  CAMPAIGN_STATUS_PAUSED: { label: 'Paused', variant: 'warning', dot: 'bg-yellow-500' },
  CAMPAIGN_STATUS_COMPLETED: { label: 'Completed', variant: 'info', dot: 'bg-blue-500' },
  CAMPAIGN_STATUS_CANCELLED: { label: 'Cancelled', variant: 'destructive', dot: 'bg-red-500' },
}

export const CONVERSATION_STATUS: Record<ConversationStatus, StatusConfig> = {
  CONVERSATION_STATUS_UNSPECIFIED: { label: 'Unknown', variant: 'secondary', dot: 'bg-gray-400' },
  CONVERSATION_STATUS_UNASSIGNED: { label: 'Unassigned', variant: 'warning', dot: 'bg-yellow-500' },
  CONVERSATION_STATUS_ASSIGNED: { label: 'Assigned', variant: 'success', dot: 'bg-green-500' },
  CONVERSATION_STATUS_CLOSED: { label: 'Closed', variant: 'secondary', dot: 'bg-gray-400' },
}

export const SEND_STATUS: Record<ContactSendStatus, StatusConfig> = {
  CONTACT_SEND_STATUS_UNSPECIFIED: { label: 'Unknown', variant: 'secondary', dot: 'bg-gray-400' },
  CONTACT_SEND_STATUS_PENDING: { label: 'Pending', variant: 'secondary', dot: 'bg-gray-400' },
  CONTACT_SEND_STATUS_SENT: { label: 'Sent', variant: 'info', dot: 'bg-blue-500' },
  CONTACT_SEND_STATUS_DELIVERED: { label: 'Delivered', variant: 'success', dot: 'bg-green-500' },
  CONTACT_SEND_STATUS_FAILED: { label: 'Failed', variant: 'destructive', dot: 'bg-red-500' },
  CONTACT_SEND_STATUS_SKIPPED: { label: 'Skipped', variant: 'secondary', dot: 'bg-gray-400' },
}

export const MSG_STATUS: Record<MessageStatus, { label: string; ticks: string }> = {
  MESSAGE_STATUS_UNSPECIFIED: { label: 'Unknown', ticks: '' },
  MESSAGE_STATUS_PENDING: { label: 'Sending', ticks: '🕐' },
  MESSAGE_STATUS_SENT: { label: 'Sent', ticks: '✓' },
  MESSAGE_STATUS_DELIVERED: { label: 'Delivered', ticks: '✓✓' },
  MESSAGE_STATUS_READ: { label: 'Read', ticks: '✓✓' },
  MESSAGE_STATUS_FAILED: { label: 'Failed', ticks: '✗' },
}

export const ROLE_LABELS: Record<Role, string> = {
  ROLE_UNSPECIFIED: 'Unknown',
  ROLE_SUPERADMIN: 'Super Admin',
  ROLE_TENANT_ADMIN: 'Tenant Admin',
  ROLE_WORKSPACE_ADMIN: 'Workspace Admin',
  ROLE_CS_AGENT: 'CS Agent',
}

export function isAdmin(role: Role): boolean {
  return role === 'ROLE_SUPERADMIN' || role === 'ROLE_TENANT_ADMIN' || role === 'ROLE_WORKSPACE_ADMIN'
}

export function isSuperAdmin(role: Role): boolean {
  return role === 'ROLE_SUPERADMIN'
}

export const MBS_STATUS: Record<MbsSessionState, StatusConfig> = {
  MBS_SESSION_STATE_UNSPECIFIED:  { label: 'Unknown',      variant: 'secondary',   dot: 'bg-gray-400' },
  MBS_SESSION_STATE_WARMING:      { label: 'Warming',      variant: 'info',        dot: 'bg-blue-500' },
  MBS_SESSION_STATE_ACTIVE:       { label: 'Active',       variant: 'success',     dot: 'bg-green-500' },
  MBS_SESSION_STATE_RECONNECTING: { label: 'Reconnecting', variant: 'warning',     dot: 'bg-yellow-500' },
  MBS_SESSION_STATE_REFRESHING:   { label: 'Refreshing',   variant: 'info',        dot: 'bg-blue-500' },
  MBS_SESSION_STATE_SUSPENDED:    { label: 'Suspended',    variant: 'warning',     dot: 'bg-yellow-500' },
  MBS_SESSION_STATE_BURNED:       { label: 'Burned',       variant: 'destructive', dot: 'bg-red-500' },
}
