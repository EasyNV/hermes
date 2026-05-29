import { create } from 'zustand'
import type {
  Conversation, Message, MessageStatus,
  WsNewMessagePayload, WsConversationUpdatedPayload, ConversationStatus,
  WsMbsNewMessagePayload, WsMbsOutboundStatusPayload,
} from '@/api/types'

interface InboxState {
  conversations: Conversation[]
  activeConversationId: string | null
  messages: Message[]
  typingMap: Record<string, boolean>

  setConversations: (convs: Conversation[]) => void
  setActiveConversation: (id: string | null) => void
  setMessages: (msgs: Message[]) => void
  appendMessage: (msg: Message) => void
  updateMessageStatus: (messageId: string, status: MessageStatus) => void
  handleNewMessage: (payload: WsNewMessagePayload) => void
  handleConversationUpdated: (payload: WsConversationUpdatedPayload) => void
  setTyping: (conversationId: string, isTyping: boolean) => void
  // E3 chunk 5: MBS frontend bridge. Both are best-effort — if the local
  // conversation hasn't been loaded yet, these no-op and the next list
  // refetch reconciles. handleMbsOutbound is used for outbound MBS sends
  // where the local row may not yet have mbs_mid set (correlation
  // populated lazily by the inbox-service outbound consumer).
  handleMbsNewMessage: (payload: WsMbsNewMessagePayload) => void
  handleMbsOutboundStatus: (payload: WsMbsOutboundStatusPayload) => void
}

export const useInboxStore = create<InboxState>((set, get) => ({
  conversations: [],
  activeConversationId: null,
  messages: [],
  typingMap: {},

  setConversations: (convs) => set({ conversations: convs }),

  setActiveConversation: (id) => set({ activeConversationId: id, messages: [], typingMap: {} }),

  setMessages: (msgs) => set({ messages: msgs }),

  appendMessage: (msg) => set((s) => ({ messages: [...s.messages, msg] })),

  updateMessageStatus: (messageId, status) =>
    set((s) => ({
      messages: s.messages.map((m) => (m.id === messageId ? { ...m, status } : m)),
    })),

  handleNewMessage: (payload) => {
    const state = get()
    // Update conversations list: move to top, update preview
    const existing = state.conversations.find((c) => c.id === payload.conversationId)
    let updatedConvs: Conversation[]

    if (existing) {
      updatedConvs = [
        {
          ...existing,
          lastMessagePreview: payload.message.body,
          lastMessageAt: payload.message.createdAt,
          unreadCount: existing.unreadCount + 1,
          status: payload.conversationStatus,
          assignedTo: payload.assignedTo,
        },
        ...state.conversations.filter((c) => c.id !== payload.conversationId),
      ]
    } else if (payload.isNewConversation) {
      const newConv: Conversation = {
        id: payload.conversationId,
        workspaceId: '',
        contactId: payload.contact.id,
        waNumberId: payload.waNumberId,
        assignedTo: payload.assignedTo,
        status: payload.conversationStatus,
        lastMessageAt: payload.message.createdAt,
        campaignId: '',
        createdAt: payload.message.createdAt,
        contactName: payload.contact.name,
        contactPhone: payload.contact.phone,
        lastMessagePreview: payload.message.body,
        unreadCount: 1,
        firstResponseTimeSecs: 0,
        // E3 chunk 1: WS new_message currently only fires for WA;
        // MBS conversations enter via list refetch (E3.5).
        channel: 'INBOX_CHANNEL_WA',
        mbsSessionUid: '',
        mbsThreadId: '',
        mbsPageId: '',
      }
      updatedConvs = [newConv, ...state.conversations]
    } else {
      updatedConvs = state.conversations
    }

    set({ conversations: updatedConvs })

    // Append message if viewing this conversation
    if (state.activeConversationId === payload.conversationId) {
      set((s) => ({ messages: [...s.messages, payload.message] }))
    }
  },

  handleConversationUpdated: (payload) =>
    set((s) => ({
      conversations: s.conversations.map((c) =>
        c.id === payload.conversationId
          ? { ...c, status: payload.status as ConversationStatus, assignedTo: payload.assignedTo }
          : c,
      ),
    })),

  setTyping: (conversationId, isTyping) =>
    set((s) => ({ typingMap: { ...s.typingMap, [conversationId]: isTyping } })),

  // E3 chunk 5: MBS bridge. mbs_new_message fires from the gateway's
  // direct subscription to hermes.mbs.message.inbound.*. inbox-service
  // ALSO writes a Conversation + Message row via its consumer (chunk 3).
  // We bridge frontend-side: find the local conversation row by the
  // (uid, threadId) tuple and append the message; if not yet loaded,
  // skip (next refetch picks it up).
  handleMbsNewMessage: (payload) => {
    const state = get()
    const conv = state.conversations.find(
      (c) =>
        c.channel === 'INBOX_CHANNEL_MBS' &&
        c.mbsSessionUid === payload.uid &&
        c.mbsThreadId === payload.threadId,
    )
    if (!conv) {
      // No local row yet. The list query will reconcile on next refetch.
      return
    }
    // Update conversation list ordering + preview.
    const updated: Conversation = {
      ...conv,
      lastMessagePreview: payload.text,
      lastMessageAt: payload.receivedAt,
      unreadCount: conv.unreadCount + (state.activeConversationId === conv.id ? 0 : 1),
    }
    set({
      conversations: [updated, ...state.conversations.filter((c) => c.id !== conv.id)],
    })
    // Append message into the active drawer if open.
    if (state.activeConversationId === conv.id) {
      const synth: Message = {
        id: payload.mid, // mbs_mid is unique enough for client-side dedupe
        conversationId: conv.id,
        direction: 'MESSAGE_DIRECTION_INBOUND',
        contentType: 'CONTENT_TYPE_TEXT',
        body: payload.text,
        mediaUrl: '',
        templateId: '',
        resolvedVarsJson: '',
        waMessageId: '',
        status: 'MESSAGE_STATUS_DELIVERED',
        createdAt: payload.receivedAt,
        mbsMid: payload.mid,
      }
      set((s) => ({ messages: [...s.messages, synth] }))
    }
  },

  // E3 chunk 5: mbs_outbound_status reconciliation. The inbox-service
  // outbound consumer (chunk 4) writes the canonical UPDATE; this WS
  // bridge is a UX speed-up so the row flips to sent/failed without
  // waiting for a list refetch. Best-effort: match by mbsMid.
  handleMbsOutboundStatus: (payload) => {
    const state = get()
    // Find the message in the currently-loaded drawer.
    if (state.messages.length === 0) return
    const newStatus: MessageStatus = payload.ok ? 'MESSAGE_STATUS_SENT' : 'MESSAGE_STATUS_FAILED'
    set({
      messages: state.messages.map((m) =>
        m.mbsMid === payload.mid && m.mbsMid !== '' ? { ...m, status: newStatus } : m,
      ),
    })
  },
}))
