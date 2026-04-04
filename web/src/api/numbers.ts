import { api, qs } from './client'
import type {
  WaNumber, WaNumberStatus, RegisterWaNumberResponse, GetQRCodeResponse,
  ListWaNumbersResponse, PageRequest,
} from './types'

export function registerWaNumber(params: {
  tenantId: string; phone: string; displayName: string; proxyId?: string; workspaceIds: string[]
}) {
  return api.post<RegisterWaNumberResponse>('/wa-numbers', params)
}

export function getQRCode(waNumberId: string) {
  return api.get<GetQRCodeResponse>(`/wa-numbers/${waNumberId}/qr-code`)
}

export function listWaNumbers(params: {
  tenantId: string; workspaceId?: string; status?: WaNumberStatus
} & PageRequest) {
  return api.get<ListWaNumbersResponse>(`/wa-numbers${qs(params)}`)
}

export function getWaNumber(id: string) {
  return api.get<{ waNumber: WaNumber }>(`/wa-numbers/${id}`)
}

export function updateWaNumber(id: string, params: {
  displayName?: string; proxyId?: string; workspaceIds?: string[]
}) {
  return api.put<{ waNumber: WaNumber }>(`/wa-numbers/${id}`, params)
}

export function disconnectWaNumber(id: string) {
  return api.post<{ waNumber: WaNumber }>(`/wa-numbers/${id}/disconnect`)
}

export function reconnectWaNumber(id: string) {
  return api.post<{ waNumber: WaNumber; qrCode: string }>(`/wa-numbers/${id}/reconnect`)
}

export function deleteWaNumber(id: string) {
  return api.del<void>(`/wa-numbers/${id}`)
}

export function pairPhone(waNumberId: string, phoneNumber: string) {
  return api.post<{ pairingCode: string }>(`/wa-numbers/${waNumberId}/pair-phone`, { phoneNumber })
}
