import { create } from 'zustand'
import type { WsEvent } from '@/api/types'
import { useInboxStore } from './inbox'
import { useCampaignsStore } from './campaigns'
import { useMbsStore } from './mbs'

type WsStatus = 'disconnected' | 'connecting' | 'connected' | 'error'

interface WebSocketState {
  status: WsStatus
  reconnectAttempts: number
  connect: () => void
  disconnect: () => void
  send: (msg: unknown) => void
  subscribeConversation: (id: string) => void
  unsubscribeConversation: (id: string) => void
}

let ws: WebSocket | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | null = null

function handleEvent(event: WsEvent) {
  const inbox = useInboxStore.getState()
  const campaigns = useCampaignsStore.getState()
  const mbs = useMbsStore.getState()

  switch (event.type) {
    case 'new_message':
      inbox.handleNewMessage(event.payload)
      break
    case 'message_status_updated':
      inbox.updateMessageStatus(event.payload.messageId, event.payload.status)
      break
    case 'conversation_updated':
      inbox.handleConversationUpdated(event.payload)
      break
    case 'campaign_progress':
      campaigns.updateProgress(event.payload)
      break
    case 'campaign_status_changed':
      campaigns.updateStatus(event.payload)
      break
    case 'typing_indicator':
      inbox.setTyping(event.payload.conversationId, event.payload.isComposing)
      break
    case 'mbs_new_message':
      // MBS inbound feeds the Pages preview tile and the cold-compose
      // composer's "last contact" indicator. Chunk 6 will additionally
      // forward into useInboxStore via the mbs:<uid>:<thread>
      // conversation prefix path. Keeping channels distinct here in
      // chunk 4 prevents accidental cross-routing while the inbox
      // integration is still in design.
      mbs.handleInbound(event.payload)
      break
    case 'mbs_outbound_status':
      mbs.handleOutbound(event.payload)
      break
    case 'mbs_session_lifecycle':
      mbs.handleSessionLifecycle(event.payload)
      break
    // connected, pong, auth_ok, error — handled implicitly or ignored
    default:
      break
  }
}

export const useWebSocketStore = create<WebSocketState>((set, get) => ({
  status: 'disconnected',
  reconnectAttempts: 0,

  connect: () => {
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return

    const token = localStorage.getItem('access_token')
    if (!token) return

    set({ status: 'connecting' })
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    ws = new WebSocket(`${proto}//${location.host}/ws?token=${token}`)

    ws.onopen = () => {
      set({ status: 'connected', reconnectAttempts: 0 })
    }

    ws.onmessage = (ev) => {
      try {
        const data = JSON.parse(ev.data) as WsEvent
        handleEvent(data)
      } catch { /* malformed message */ }
    }

    ws.onclose = () => {
      ws = null
      set({ status: 'disconnected' })
      // Exponential backoff reconnect
      const attempts = get().reconnectAttempts
      if (attempts >= 10) {
        set({ status: 'error' })
        return
      }
      const delay = Math.min(Math.pow(2, attempts) * 1000, 30000)
      const jitter = Math.random() * 1000 - 500
      reconnectTimer = setTimeout(() => {
        set({ reconnectAttempts: attempts + 1 })
        get().connect()
      }, Math.max(delay + jitter, 500))
    }

    ws.onerror = () => {
      // onclose will fire after onerror
    }
  },

  disconnect: () => {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    if (ws) {
      ws.close()
      ws = null
    }
    set({ status: 'disconnected', reconnectAttempts: 0 })
  },

  send: (msg) => {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg))
    }
  },

  subscribeConversation: (id) => {
    get().send({ type: 'subscribe_conversation', payload: { conversationId: id } })
  },

  unsubscribeConversation: (id) => {
    get().send({ type: 'unsubscribe_conversation', payload: { conversationId: id } })
  },
}))
