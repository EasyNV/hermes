import { useEffect } from 'react'
import { useAuthStore } from '@/stores/auth'

/** Initializes auth on first mount and exposes auth state. */
export function useAuth() {
  const store = useAuthStore()

  useEffect(() => {
    if (!store.isAuthenticated && store.isLoading) {
      store.initialize()
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  return store
}
