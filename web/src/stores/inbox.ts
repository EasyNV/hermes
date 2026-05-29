import { create } from 'zustand'
import type {
  Conversation, Message, MessageStatus,
  WsNewMessagePayload, WsConversationUpdatedPayload, ConversationStatus,
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
}))
