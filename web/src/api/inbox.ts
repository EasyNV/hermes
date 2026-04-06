import { api, qs } from './client'
import type {
  Conversation, ConversationStatus, Message, ContentType,
  ListConversationsResponse, GetConversationResponse, ListMessagesResponse,
  SearchMessagesResponse, GetContactCampaignHistoryResponse, GetAgentPerformanceResponse,
  ListCannedResponsesResponse, CannedResponse, PageRequest,
} from './types'

// ── Conversations ──

export function listConversations(params: {
  workspaceId: string; status?: ConversationStatus; assignedTo?: string
  waNumberId?: string; search?: string
} & PageRequest) {
  return api.get<ListConversationsResponse>(`/conversations${qs(params)}`)
}

export function getConversation(id: string) {
  return api.get<GetConversationResponse>(`/conversations/${id}`)
}

export function claimConversation(id: string) {
  return api.post<{ conversation: Conversation }>(`/conversations/${id}/claim`)
}

export function transferConversation(id: string, targetUserId: string) {
  return api.post<{ conversation: Conversation }>(`/conversations/${id}/transfer`, { targetUserId })
}

export function closeConversation(id: string) {
  return api.post<{ conversation: Conversation }>(`/conversations/${id}/close`)
}

// ── Messages ──

export function listMessages(conversationId: string, params?: PageRequest) {
  return api.get<ListMessagesResponse>(`/conversations/${conversationId}/messages${qs(params ?? {})}`)
}

export function sendMessage(conversationId: string, params: {
  contentType: ContentType; body?: string; mediaUrl?: string
}) {
  return api.post<{ message: Message }>(`/conversations/${conversationId}/messages`, params)
}

export function searchMessages(params: {
  workspaceId: string; query: string; conversationId?: string
} & PageRequest) {
  return api.post<SearchMessagesResponse>('/messages/search', params)
}

export function sendTypingIndicator(conversationId: string) {
  return api.post<void>(`/conversations/${conversationId}/typing`)
}

// ── Contact History ──

export function getContactCampaignHistory(contactId: string, params?: PageRequest) {
  return api.get<GetContactCampaignHistoryResponse>(`/contacts/${contactId}/campaigns${qs(params ?? {})}`)
}

// ── Agent Performance ──

export function getAgentPerformance(params: {
  workspaceId: string; userId?: string; fromDate?: string; toDate?: string
}) {
  return api.get<GetAgentPerformanceResponse>(`/agent-performance${qs(params)}`)
}

// ── Canned Responses ──

export function createCannedResponse(params: { workspaceId: string; shortcut: string; body: string }) {
  return api.post<{ cannedResponse: CannedResponse }>('/canned-responses', params)
}

export function listCannedResponses(params: { workspaceId: string; search?: string } & PageRequest) {
  return api.get<ListCannedResponsesResponse>(`/canned-responses${qs(params)}`)
}

export function updateCannedResponse(id: string, params: { shortcut?: string; body?: string }) {
  return api.put<{ cannedResponse: CannedResponse }>(`/canned-responses/${id}`, params)
}

export function deleteCannedResponse(id: string) {
  return api.del<void>(`/canned-responses/${id}`)
}

export function clearAllConversations() {
  return api.del<{ deleted: number }>('/conversations/clear')
}
