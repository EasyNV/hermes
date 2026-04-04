import { useEffect } from 'react'
import { useWebSocketStore } from '@/stores/websocket'
import { useAuthStore } from '@/stores/auth'

/** Manages WebSocket lifecycle: connects when authenticated, disconnects on unmount. */
export function useWebSocket() {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const { status, connect, disconnect } = useWebSocketStore()

  useEffect(() => {
    if (isAuthenticated) {
      connect()
    } else {
      disconnect()
    }
    return () => disconnect()
  }, [isAuthenticated]) // eslint-disable-line react-hooks/exhaustive-deps

  return { status }
}
