import { api, qs } from './client'
import type { Template, ListTemplatesResponse, PageRequest } from './types'

export function createTemplate(params: {
  workspaceId: string; name: string; body: string; mediaUrl?: string; mediaType?: string
}) {
  return api.post<{ template: Template }>('/templates', params)
}

export function getTemplate(id: string) {
  return api.get<{ template: Template }>(`/templates/${id}`)
}

export function listTemplates(params: { workspaceId: string; search?: string } & PageRequest) {
  return api.get<ListTemplatesResponse>(`/templates${qs(params)}`)
}

export function updateTemplate(id: string, params: {
  name?: string; body?: string; mediaUrl?: string; mediaType?: string
}) {
  return api.put<{ template: Template }>(`/templates/${id}`, params)
}

export function deleteTemplate(id: string) {
  return api.del<void>(`/templates/${id}`)
}
