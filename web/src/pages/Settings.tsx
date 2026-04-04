import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Plus, Pencil, Trash2, Loader2, Zap, Settings2, Bell, Webhook, Volume2,
} from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import {
  listCannedResponses, createCannedResponse,
  updateCannedResponse, deleteCannedResponse,
} from '@/api/inbox'
import {
  listNotificationConfigs, configureNotification,
  testNotification, deleteNotificationConfig,
} from '@/api/notifications'
import type {
  CannedResponse, NotificationType, WebhookType,
} from '@/api/types'
import { NotificationType as NotifType, WebhookType as WHType } from '@/api/types'
import { truncate } from '@/lib/utils'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  Select, SelectTrigger, SelectValue, SelectContent, SelectItem,
} from '@/components/ui/select'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogFooter, DialogDescription,
} from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'

// ── Notification type display config ───────────────────────

const NOTIF_TYPE_CONFIG: Record<NotificationType, { label: string; icon: typeof Bell }> = {
  NOTIFICATION_TYPE_UNSPECIFIED: { label: 'Unknown', icon: Bell },
  NOTIFICATION_TYPE_BROWSER_PUSH: { label: 'Browser Push', icon: Bell },
  NOTIFICATION_TYPE_SOUND: { label: 'Sound', icon: Volume2 },
  NOTIFICATION_TYPE_WEBHOOK: { label: 'Webhook', icon: Webhook },
}

const WEBHOOK_TYPE_LABELS: Record<WebhookType, string> = {
  WEBHOOK_TYPE_UNSPECIFIED: 'Unknown',
  WEBHOOK_TYPE_TELEGRAM: 'Telegram',
  WEBHOOK_TYPE_DISCORD: 'Discord',
  WEBHOOK_TYPE_CUSTOM: 'Custom',
}

// ── Canned Response Dialog ─────────────────────────────────

interface CannedDialogProps {
  open: boolean
  onClose: () => void
  onSubmit: (shortcut: string, body: string) => void
  loading: boolean
  initial?: { shortcut: string; body: string }
  title: string
}

function CannedDialog({ open, onClose, onSubmit, loading, initial, title }: CannedDialogProps) {
  const [shortcut, setShortcut] = useState(initial?.shortcut ?? '/')
  const [body, setBody] = useState(initial?.body ?? '')

  const isValid = shortcut.startsWith('/') && shortcut.length > 1 && body.trim().length > 0

  function handleSubmit() {
    if (isValid) onSubmit(shortcut, body.trim())
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            Shortcuts must start with /. Agents can type the shortcut in chat to quickly insert the response.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="shortcut">Shortcut</Label>
            <Input
              id="shortcut"
              placeholder="/greeting"
              value={shortcut}
              onChange={(e) => {
                const val = e.target.value
                setShortcut(val.startsWith('/') ? val : `/${val}`)
              }}
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="body">Response Body</Label>
            <Textarea
              id="body"
              placeholder="Hello! How can I help you today?"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              rows={4}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={loading}>Cancel</Button>
          <Button onClick={handleSubmit} disabled={!isValid || loading}>
            {loading ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Notification Config Dialog ─────────────────────────────

interface NotifDialogProps {
  open: boolean
  onClose: () => void
  onSubmit: (params: {
    type: NotificationType
    webhookUrl: string
    webhookType: WebhookType
    enabled: boolean
  }) => void
  loading: boolean
}

function NotifDialog({ open, onClose, onSubmit, loading }: NotifDialogProps) {
  const [type, setType] = useState<NotificationType>(NotifType.BROWSER_PUSH)
  const [webhookUrl, setWebhookUrl] = useState('')
  const [webhookType, setWebhookType] = useState<WebhookType>(WHType.CUSTOM)
  const [enabled, setEnabled] = useState(true)

  const isWebhook = type === NotifType.WEBHOOK
  const isValid = isWebhook ? webhookUrl.trim().length > 0 : true

  function handleSubmit() {
    if (!isValid) return
    onSubmit({
      type,
      webhookUrl: isWebhook ? webhookUrl.trim() : '',
      webhookType: isWebhook ? webhookType : WHType.UNSPECIFIED,
      enabled,
    })
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Configure Notification</DialogTitle>
          <DialogDescription>
            Set up how you want to receive notifications for this workspace.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label>Notification Type</Label>
            <Select value={type} onValueChange={(v) => setType(v as NotificationType)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NotifType.BROWSER_PUSH}>Browser Push</SelectItem>
                <SelectItem value={NotifType.SOUND}>Sound</SelectItem>
                <SelectItem value={NotifType.WEBHOOK}>Webhook</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {isWebhook && (
            <>
              <div className="flex flex-col gap-2">
                <Label>Webhook URL</Label>
                <Input
                  placeholder="https://api.telegram.org/bot.../sendMessage"
                  value={webhookUrl}
                  onChange={(e) => setWebhookUrl(e.target.value)}
                />
              </div>
              <div className="flex flex-col gap-2">
                <Label>Webhook Type</Label>
                <Select value={webhookType} onValueChange={(v) => setWebhookType(v as WebhookType)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={WHType.TELEGRAM}>Telegram</SelectItem>
                    <SelectItem value={WHType.DISCORD}>Discord</SelectItem>
                    <SelectItem value={WHType.CUSTOM}>Custom</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </>
          )}

          <div className="flex items-center justify-between">
            <Label>Enabled</Label>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={loading}>Cancel</Button>
          <Button onClick={handleSubmit} disabled={!isValid || loading}>
            {loading ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ── Main Settings Page ─────────────────────────────────────

export default function Settings() {
  const workspace = useAuthStore((s) => s.workspace)
  const workspaceId = workspace?.id ?? ''
  const queryClient = useQueryClient()

  // ── Canned Responses State ──

  const [showCannedDialog, setShowCannedDialog] = useState(false)
  const [editingCanned, setEditingCanned] = useState<CannedResponse | null>(null)
  const [deletingCannedId, setDeletingCannedId] = useState<string | null>(null)

  // ── Notification State ──

  const [showNotifDialog, setShowNotifDialog] = useState(false)
  const [deletingNotifId, setDeletingNotifId] = useState<string | null>(null)
  const [testingConfigId, setTestingConfigId] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<{ configId: string; success: boolean; error: string } | null>(null)

  // ── Queries ──

  const cannedQuery = useQuery({
    queryKey: ['canned-responses', workspaceId],
    queryFn: () => listCannedResponses({ workspaceId, pageSize: 100 }),
    enabled: !!workspaceId,
  })

  const notifQuery = useQuery({
    queryKey: ['notification-configs', workspaceId],
    queryFn: () => listNotificationConfigs(workspaceId),
    enabled: !!workspaceId,
  })

  // ── Canned Response Mutations ──

  const createCannedMutation = useMutation({
    mutationFn: (params: { shortcut: string; body: string }) =>
      createCannedResponse({ workspaceId, ...params }),
    onSuccess: () => {
      setShowCannedDialog(false)
      queryClient.invalidateQueries({ queryKey: ['canned-responses', workspaceId] })
    },
  })

  const updateCannedMutation = useMutation({
    mutationFn: (params: { id: string; shortcut: string; body: string }) =>
      updateCannedResponse(params.id, { shortcut: params.shortcut, body: params.body }),
    onSuccess: () => {
      setEditingCanned(null)
      queryClient.invalidateQueries({ queryKey: ['canned-responses', workspaceId] })
    },
  })

  const deleteCannedMutation = useMutation({
    mutationFn: (id: string) => deleteCannedResponse(id),
    onSuccess: () => {
      setDeletingCannedId(null)
      queryClient.invalidateQueries({ queryKey: ['canned-responses', workspaceId] })
    },
  })

  // ── Notification Mutations ──

  const configureNotifMutation = useMutation({
    mutationFn: (params: {
      type: NotificationType; webhookUrl: string; webhookType: WebhookType; enabled: boolean
    }) => configureNotification({ workspaceId, ...params }),
    onSuccess: () => {
      setShowNotifDialog(false)
      queryClient.invalidateQueries({ queryKey: ['notification-configs', workspaceId] })
    },
  })

  const testNotifMutation = useMutation({
    mutationFn: (configId: string) => testNotification(configId),
    onSuccess: (data, configId) => {
      setTestingConfigId(null)
      setTestResult({ configId, success: data.success, error: data.error })
      setTimeout(() => setTestResult(null), 5000)
    },
    onError: (_err, configId) => {
      setTestingConfigId(null)
      setTestResult({ configId, success: false, error: 'Request failed' })
      setTimeout(() => setTestResult(null), 5000)
    },
  })

  const deleteNotifMutation = useMutation({
    mutationFn: (id: string) => deleteNotificationConfig(id),
    onSuccess: () => {
      setDeletingNotifId(null)
      queryClient.invalidateQueries({ queryKey: ['notification-configs', workspaceId] })
    },
  })

  // ── Render ───────────────────────────────────────────────

  const cannedResponses = cannedQuery.data?.cannedResponses ?? []
  const notificationConfigs = notifQuery.data?.configs ?? []

  return (
    <div className="mx-auto max-w-4xl p-6">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground">
          Manage canned responses and notification preferences for {workspace?.name ?? 'your workspace'}.
        </p>
      </div>

      <Tabs defaultValue="canned">
        <TabsList>
          <TabsTrigger value="canned">Canned Responses</TabsTrigger>
          <TabsTrigger value="notifications">Notifications</TabsTrigger>
        </TabsList>

        {/* ── Canned Responses Tab ── */}
        <TabsContent value="canned">
          <Card>
            <CardHeader className="flex-row items-center justify-between space-y-0">
              <CardTitle className="text-base">Canned Responses</CardTitle>
              <Button size="sm" onClick={() => setShowCannedDialog(true)}>
                <Plus className="mr-1 h-4 w-4" /> Add Response
              </Button>
            </CardHeader>
            <CardContent>
              {cannedQuery.isLoading && (
                <div className="flex items-center justify-center py-8">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              )}
              {cannedResponses.length === 0 && !cannedQuery.isLoading && (
                <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
                  <Settings2 className="h-8 w-8 mb-2" />
                  <p className="text-sm">No canned responses yet</p>
                  <p className="text-xs">Create shortcuts for frequently used replies.</p>
                </div>
              )}
              <div className="flex flex-col">
                {cannedResponses.map((cr, idx) => (
                  <div key={cr.id}>
                    {idx > 0 && <Separator />}
                    <div className="flex items-start justify-between py-3">
                      <div className="flex-1 overflow-hidden">
                        <p className="text-sm font-semibold">{cr.shortcut}</p>
                        <p className="mt-0.5 text-sm text-muted-foreground">
                          {truncate(cr.body, 120)}
                        </p>
                      </div>
                      <div className="ml-4 flex shrink-0 gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          onClick={() => setEditingCanned(cr)}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          onClick={() => setDeletingCannedId(cr.id)}
                        >
                          <Trash2 className="h-4 w-4 text-destructive" />
                        </Button>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        {/* ── Notifications Tab ── */}
        <TabsContent value="notifications">
          <Card>
            <CardHeader className="flex-row items-center justify-between space-y-0">
              <CardTitle className="text-base">Notification Configs</CardTitle>
              <Button size="sm" onClick={() => setShowNotifDialog(true)}>
                <Plus className="mr-1 h-4 w-4" /> Add Notification
              </Button>
            </CardHeader>
            <CardContent>
              {notifQuery.isLoading && (
                <div className="flex items-center justify-center py-8">
                  <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
                </div>
              )}
              {notificationConfigs.length === 0 && !notifQuery.isLoading && (
                <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
                  <Bell className="h-8 w-8 mb-2" />
                  <p className="text-sm">No notification configs</p>
                  <p className="text-xs">Set up how you receive alerts.</p>
                </div>
              )}
              <div className="flex flex-col">
                {notificationConfigs.map((cfg, idx) => {
                  const typeConfig = NOTIF_TYPE_CONFIG[cfg.type] ?? NOTIF_TYPE_CONFIG.NOTIFICATION_TYPE_UNSPECIFIED
                  const IconComp = typeConfig.icon
                  const testRes = testResult?.configId === cfg.id ? testResult : null

                  return (
                    <div key={cfg.id}>
                      {idx > 0 && <Separator />}
                      <div className="flex items-center justify-between py-3">
                        <div className="flex items-center gap-3 flex-1 overflow-hidden">
                          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted">
                            <IconComp className="h-4 w-4" />
                          </div>
                          <div className="flex-1 overflow-hidden">
                            <div className="flex items-center gap-2">
                              <Badge variant={cfg.enabled ? 'success' : 'secondary'}>
                                {typeConfig.label}
                              </Badge>
                              {cfg.type === NotifType.WEBHOOK && cfg.webhookType !== WHType.UNSPECIFIED && (
                                <Badge variant="outline">
                                  {WEBHOOK_TYPE_LABELS[cfg.webhookType]}
                                </Badge>
                              )}
                              {!cfg.enabled && (
                                <span className="text-xs text-muted-foreground">Disabled</span>
                              )}
                            </div>
                            {cfg.webhookUrl && (
                              <p className="mt-0.5 truncate text-xs text-muted-foreground">
                                {cfg.webhookUrl}
                              </p>
                            )}
                            {testRes && (
                              <p className={`mt-1 text-xs ${testRes.success ? 'text-green-600' : 'text-destructive'}`}>
                                {testRes.success ? 'Test successful!' : `Test failed: ${testRes.error}`}
                              </p>
                            )}
                          </div>
                        </div>
                        <div className="ml-4 flex shrink-0 items-center gap-1">
                          <Switch
                            checked={cfg.enabled}
                            onCheckedChange={(checked) =>
                              configureNotifMutation.mutate({
                                type: cfg.type,
                                webhookUrl: cfg.webhookUrl,
                                webhookType: cfg.webhookType,
                                enabled: checked,
                              })
                            }
                          />
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => {
                              setTestingConfigId(cfg.id)
                              testNotifMutation.mutate(cfg.id)
                            }}
                            disabled={testingConfigId === cfg.id}
                          >
                            {testingConfigId === cfg.id ? (
                              <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                            ) : (
                              <Zap className="mr-1 h-3 w-3" />
                            )}
                            Test
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            onClick={() => setDeletingNotifId(cfg.id)}
                          >
                            <Trash2 className="h-4 w-4 text-destructive" />
                          </Button>
                        </div>
                      </div>
                    </div>
                  )
                })}
              </div>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* ── Canned Response Create Dialog ── */}
      <CannedDialog
        open={showCannedDialog}
        onClose={() => setShowCannedDialog(false)}
        onSubmit={(shortcut, body) => createCannedMutation.mutate({ shortcut, body })}
        loading={createCannedMutation.isPending}
        title="Create Canned Response"
      />

      {/* ── Canned Response Edit Dialog ── */}
      {editingCanned && (
        <CannedDialog
          open={!!editingCanned}
          onClose={() => setEditingCanned(null)}
          onSubmit={(shortcut, body) =>
            updateCannedMutation.mutate({ id: editingCanned.id, shortcut, body })
          }
          loading={updateCannedMutation.isPending}
          initial={{ shortcut: editingCanned.shortcut, body: editingCanned.body }}
          title="Edit Canned Response"
        />
      )}

      {/* ── Canned Response Delete Confirm ── */}
      <ConfirmDialog
        open={!!deletingCannedId}
        onClose={() => setDeletingCannedId(null)}
        onConfirm={() => deletingCannedId && deleteCannedMutation.mutate(deletingCannedId)}
        title="Delete Canned Response"
        description="This action cannot be undone. The shortcut will no longer be available for agents."
        confirmLabel="Delete"
        destructive
        loading={deleteCannedMutation.isPending}
      />

      {/* ── Notification Config Dialog ── */}
      <NotifDialog
        open={showNotifDialog}
        onClose={() => setShowNotifDialog(false)}
        onSubmit={(params) => configureNotifMutation.mutate(params)}
        loading={configureNotifMutation.isPending}
      />

      {/* ── Notification Delete Confirm ── */}
      <ConfirmDialog
        open={!!deletingNotifId}
        onClose={() => setDeletingNotifId(null)}
        onConfirm={() => deletingNotifId && deleteNotifMutation.mutate(deletingNotifId)}
        title="Delete Notification Config"
        description="This notification configuration will be permanently removed."
        confirmLabel="Delete"
        destructive
        loading={deleteNotifMutation.isPending}
      />
    </div>
  )
}
