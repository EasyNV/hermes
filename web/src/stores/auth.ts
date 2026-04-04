import { create } from 'zustand'
import type { User, Tenant, Workspace } from '@/api/types'
import * as authApi from '@/api/auth'

interface AuthState {
  user: User | null
  tenant: Tenant | null
  workspace: Workspace | null
  isAuthenticated: boolean
  isLoading: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  initialize: () => Promise<void>
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  tenant: null,
  workspace: null,
  isAuthenticated: false,
  isLoading: true,

  login: async (email, password) => {
    const res = await authApi.login(email, password)
    localStorage.setItem('access_token', res.accessToken)
    localStorage.setItem('refresh_token', res.refreshToken)
    // Fetch full profile
    const me = await authApi.getMe()
    set({
      user: me.user,
      tenant: me.tenant,
      workspace: me.workspace,
      isAuthenticated: true,
      isLoading: false,
    })
  },

  logout: async () => {
    try { await authApi.logout() } catch { /* ignore */ }
    localStorage.removeItem('access_token')
    localStorage.removeItem('refresh_token')
    set({ user: null, tenant: null, workspace: null, isAuthenticated: false, isLoading: false })
  },

  initialize: async () => {
    const token = localStorage.getItem('access_token')
    if (!token) {
      set({ isLoading: false })
      return
    }
    try {
      const me = await authApi.getMe()
      set({
        user: me.user,
        tenant: me.tenant,
        workspace: me.workspace,
        isAuthenticated: true,
        isLoading: false,
      })
    } catch {
      localStorage.removeItem('access_token')
      localStorage.removeItem('refresh_token')
      set({ isLoading: false })
    }
  },
}))
