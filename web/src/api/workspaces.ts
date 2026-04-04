import { api, qs } from './client'
import type { ListWorkspacesResponse, Workspace, PageRequest } from './types'

export function listWorkspaces(params: { tenantId: string } & PageRequest) {
  return api.get<ListWorkspacesResponse>(`/workspaces${qs(params)}`)
}

export function getWorkspace(id: string) {
  return api.get<{ workspace: Workspace }>(`/workspaces/${id}`)
}

export function createWorkspace(params: { tenantId: string; name: string; settingsJson?: string; dailyCap?: number }) {
  return api.post<{ workspace: Workspace }>('/workspaces', params)
}
