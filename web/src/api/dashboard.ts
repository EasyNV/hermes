import { api, qs } from './client'
import type { GetDashboardStatsResponse } from './types'

export function getDashboardStats(params: { workspaceId?: string; tenantId?: string }) {
  return api.get<GetDashboardStatsResponse>(`/dashboard/stats${qs(params)}`)
}
