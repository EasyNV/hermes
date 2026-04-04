import { api, qs } from './client'
import type {
  Contact, CreateContactResponse, ImportContactsResponse,
  ListContactsResponse, PageRequest,
} from './types'

export function createContact(params: {
  tenantId: string; phone: string; name?: string; tags?: string[]; customFields?: Record<string, string>
}) {
  return api.post<CreateContactResponse>('/contacts', params)
}

export function importContacts(params: {
  tenantId: string; csvData: string; filename: string
  columnMapping: Record<string, string>; defaultTags?: string[]
}) {
  return api.post<ImportContactsResponse>('/contacts/import', params)
}

export function listContacts(params: {
  tenantId: string; search?: string; tags?: string[]
  isBanned?: boolean; filterBanned?: boolean
} & PageRequest) {
  const { tags, ...rest } = params
  const q = qs(rest)
  const tagParams = tags?.length ? '&' + tags.map(t => `tags=${encodeURIComponent(t)}`).join('&') : ''
  return api.get<ListContactsResponse>(`/contacts${q}${tagParams}`)
}

export function getContact(id: string) {
  return api.get<{ contact: Contact }>(`/contacts/${id}`)
}

export function updateContact(id: string, params: {
  name?: string; phone?: string; tags?: string[]; customFields?: Record<string, string>; isBanned?: boolean
}) {
  return api.put<{ contact: Contact }>(`/contacts/${id}`, params)
}

export function deleteContact(id: string) {
  return api.del<void>(`/contacts/${id}`)
}
