import { api, qs } from './client'
import type {
  Campaign, CampaignStatus, RotationStrategy, GetCampaignResponse,
  ListCampaignsResponse, ListCampaignContactsResponse, ListCampaignNumbersResponse,
  ContactSendStatus, CampaignNumber, PageRequest,
} from './types'

export function createCampaign(params: {
  workspaceId: string; templateId: string; name: string; scheduleAt?: string
  dailyCapPerNum?: number; banPauseThreshold?: number; rotationStrategy?: RotationStrategy
  delayMinMs?: number; delayMaxMs?: number; contactIds: string[]
  // Stage F follow-up chunk 8 — channel-first. 'wa' (default) or 'mbs'.
  // The two ID lists are mutually exclusive at the API boundary; the
  // server rejects mixed combinations with InvalidArgument.
  channel?: 'wa' | 'mbs'
  waNumberIds?: string[]
  mbsSessionUids?: string[]
}) {
  return api.post<{ campaign: Campaign }>('/campaigns', params)
}

export function getCampaign(id: string) {
  return api.get<GetCampaignResponse>(`/campaigns/${id}`)
}

export function listCampaigns(params: { workspaceId: string; status?: CampaignStatus } & PageRequest) {
  return api.get<ListCampaignsResponse>(`/campaigns${qs(params)}`)
}

export function startCampaign(id: string) {
  return api.post<{ campaign: Campaign }>(`/campaigns/${id}/start`)
}

export function pauseCampaign(id: string) {
  return api.post<{ campaign: Campaign }>(`/campaigns/${id}/pause`)
}

export function resumeCampaign(id: string) {
  return api.post<{ campaign: Campaign }>(`/campaigns/${id}/resume`)
}

export function cancelCampaign(id: string) {
  return api.post<{ campaign: Campaign }>(`/campaigns/${id}/cancel`)
}

export function updateCampaignNumbers(campaignId: string, params: {
  addWaNumberIds?: string[]; removeWaNumberIds?: string[]
}) {
  return api.put<{ campaign: Campaign; numbers: CampaignNumber[] }>(`/campaigns/${campaignId}/numbers`, params)
}

export function updateCampaignContacts(campaignId: string, params: {
  addContactIds?: string[]; removeContactIds?: string[]
}) {
  return api.put<{ campaign: Campaign }>(`/campaigns/${campaignId}/contacts`, params)
}

export function listCampaignContacts(campaignId: string, params?: { status?: ContactSendStatus } & PageRequest) {
  return api.get<ListCampaignContactsResponse>(`/campaigns/${campaignId}/contacts${qs(params ?? {})}`)
}

export function listCampaignNumbers(campaignId: string, params?: PageRequest) {
  return api.get<ListCampaignNumbersResponse>(`/campaigns/${campaignId}/numbers${qs(params ?? {})}`)
}
