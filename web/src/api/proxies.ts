import { api, qs } from './client'
import type {
  Proxy, ProxyInput, ProxyStatus, ProxyType, AddProxiesResponse,
  ListProxiesResponse, GetProxyHealthResponse, GetBestProxyResponse, PageRequest, WaNumber,
} from './types'

export function addProxies(tenantId: string, proxies: ProxyInput[]) {
  return api.post<AddProxiesResponse>('/proxies', { tenantId, proxies })
}

export function listProxies(params: { tenantId: string; status?: ProxyStatus } & PageRequest) {
  return api.get<ListProxiesResponse>(`/proxies${qs(params)}`)
}

export function updateProxy(id: string, params: {
  host?: string; port?: number; username?: string; password?: string; type?: ProxyType; status?: ProxyStatus
}) {
  return api.put<{ proxy: Proxy }>(`/proxies/${id}`, params)
}

export function deleteProxy(id: string) {
  return api.del<void>(`/proxies/${id}`)
}

export function assignProxy(waNumberId: string, proxyId: string) {
  return api.post<{ waNumber: WaNumber }>('/proxies/assign', { waNumberId, proxyId })
}

export function getProxyHealth(id: string) {
  return api.get<GetProxyHealthResponse>(`/proxies/${id}/health`)
}

export function getBestProxy(tenantId: string, type?: ProxyType) {
  return api.get<GetBestProxyResponse>(`/proxies/best${qs({ tenantId, type })}`)
}
