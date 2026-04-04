import { api, qs } from './client'
import type {
  NotificationConfig, NotificationType, WebhookType,
  ListNotificationConfigsResponse, TestNotificationResponse,
} from './types'

export function configureNotification(params: {
  workspaceId: string; type: NotificationType; webhookUrl?: string
  webhookType?: WebhookType; enabled: boolean
}) {
  return api.post<{ config: NotificationConfig }>('/notifications', params)
}

export function listNotificationConfigs(workspaceId: string) {
  return api.get<ListNotificationConfigsResponse>(`/notifications${qs({ workspaceId })}`)
}

export function testNotification(configId: string) {
  return api.post<TestNotificationResponse>(`/notifications/${configId}/test`)
}

export function deleteNotificationConfig(id: string) {
  return api.del<void>(`/notifications/${id}`)
}
