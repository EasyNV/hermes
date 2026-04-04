import { api } from './client'
import type { LoginResponse, RefreshTokenResponse, GetMeResponse } from './types'

export function login(email: string, password: string) {
  return api.post<LoginResponse>('/auth/login', { email, password })
}

export function refreshToken(token: string) {
  return api.post<RefreshTokenResponse>('/auth/refresh', { refreshToken: token })
}

export function logout() {
  return api.post<void>('/auth/logout')
}

export function getMe() {
  return api.get<GetMeResponse>('/auth/me')
}
