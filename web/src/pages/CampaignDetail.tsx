import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useParams } from '@tanstack/react-router'
import { useCampaignsStore } from '@/stores/campaigns'
import {
  getCampaign,
  pauseCampaign,
  resumeCampaign,
  cancelCampaign,
  listCampaignContacts,
  listCampaignNumbers,
} from '@/api/campaigns'
import type {
  Campaign,
  CampaignStatus,
  WsCampaignProgressPayload,
  CampaignContactDetail,
  CampaignNumberDetail,
  ContactSendStatus,
} from '@/api/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { Table, TableHeader, TableBody, TableHead, TableRow, TableCell } from '@/components/ui/table'
import { Separator } from '@/components/ui/separator'
import { Pagination } from '@/components/shared/Pagination'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { CAMPAIGN_STATUS, SEND_STATUS, WA_STATUS } from '@/lib/constants'
import { formatNumber, formatPhone } from '@/lib/utils'

function formatEta(seconds: number): string {
  if (seconds <= 0) return '--'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.round(seconds % 60)
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

function StatCard({ label, value, subtext }: { label: string; value: string | number; subtext?: string }) {
  return (
    <Card>
      <CardContent className="p-4">
        <p className="text-sm text-muted-foreground">{label}</p>
        <p className="text-2xl font-bold mt-1">{typeof value === 'number' ? formatNumber(value) : value}</p>
        {subtext && <p className="text-xs text-muted-foreground mt-0.5">{subtext}</p>}
      </CardContent>
    </Card>
  )
}

function CampaignActions({
  campaign,
  onPause,
  onResume,
  onCancel,
  isPending,
}: {
  campaign: Campaign
  onPause: () => void
  onResume: () => void
  onCancel: () => void
  isPending: boolean
}) {
  const status = campaign.status

  return (
    <div className="flex gap-2">
      {status === ('CAMPAIGN_STATUS_RUNNING' satisfies CampaignStatus) && (
        <Button variant="outline" onClick={onPause} disabled={isPending}>
          {isPending ? 'Pausing...' : 'Pause'}
        </Button>
      )}
      {status === ('CAMPAIGN_STATUS_PAUSED' satisfies CampaignStatus) && (
        <Button onClick={onResume} disabled={isPending}>
          {isPending ? 'Resuming...' : 'Resume'}
        </Button>
      )}
      {(status === ('CAMPAIGN_STATUS_RUNNING' satisfies CampaignStatus) ||
        status === ('CAMPAIGN_STATUS_PAUSED' satisfies CampaignStatus)) && (
        <Button variant="destructive" onClick={onCancel} disabled={isPending}>
          {isPending ? 'Cancelling...' : 'Cancel'}
        </Button>
      )}
    </div>
  )
}

function NumberProgressTable({
  campaignId,
  wsProgress,
}: {
  campaignId: string
  wsProgress: WsCampaignProgressPayload | undefined
}) {
  const [page, setPage] = useState(1)

  const { data, isLoading } = useQuery({
    queryKey: ['campaignNumbers', campaignId, page],
    queryFn: () => listCampaignNumbers(campaignId, { page, pageSize: 10 }),
    enabled: !wsProgress?.numberProgress?.length,
  })

  const wsNumbers = wsProgress?.numberProgress

  if (wsNumbers && wsNumbers.length > 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Number Breakdown (Live)</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Phone</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-right">Sent</TableHead>
                <TableHead className="text-right">Failed</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {wsNumbers.map((n) => {
                const statusCfg = WA_STATUS[n.status]
                return (
                  <TableRow key={n.waNumberId}>
                    <TableCell className="font-mono text-sm">{formatPhone(n.phone)}</TableCell>
                    <TableCell>
                      <StatusBadge label={statusCfg.label} variant={statusCfg.variant} dot={statusCfg.dot} />
                    </TableCell>
                    <TableCell className="text-right">{formatNumber(n.sentCount)}</TableCell>
                    <TableCell className="text-right">{formatNumber(n.failedCount)}</TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    )
  }

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Number Breakdown</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground py-4 text-center">Loading...</p>
        </CardContent>
      </Card>
    )
  }

  const numbers = data?.numbers ?? []

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Number Breakdown</CardTitle>
      </CardHeader>
      <CardContent>
        {numbers.length === 0 ? (
          <p className="text-sm text-muted-foreground py-4 text-center">No numbers assigned.</p>
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Phone</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Sent</TableHead>
                  <TableHead className="text-right">Failed</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {numbers.map((n: CampaignNumberDetail) => {
                  const statusCfg = WA_STATUS[n.currentStatus]
                  return (
                    <TableRow key={n.campaignNumber.waNumberId}>
                      <TableCell className="font-mono text-sm">{formatPhone(n.phone)}</TableCell>
                      <TableCell>{n.displayName || '-'}</TableCell>
                      <TableCell>
                        <StatusBadge label={statusCfg.label} variant={statusCfg.variant} dot={statusCfg.dot} />
                      </TableCell>
                      <TableCell className="text-right">{formatNumber(n.campaignNumber.sentCount)}</TableCell>
                      <TableCell className="text-right">{formatNumber(n.campaignNumber.failedCount)}</TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
            {data?.pagination && <Pagination pagination={data.pagination} onPageChange={setPage} />}
          </>
        )}
      </CardContent>
    </Card>
  )
}

function ContactsTable({ campaignId }: { campaignId: string }) {
  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<ContactSendStatus | ''>('')

  const { data, isLoading } = useQuery({
    queryKey: ['campaignContacts', campaignId, page, statusFilter],
    queryFn: () =>
      listCampaignContacts(campaignId, {
        page,
        pageSize: 10,
        status: statusFilter || undefined,
      }),
  })

  const contacts = data?.contacts ?? []

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-base">Campaign Contacts</CardTitle>
          <div className="flex gap-2">
            {(['', 'CONTACT_SEND_STATUS_PENDING', 'CONTACT_SEND_STATUS_SENT', 'CONTACT_SEND_STATUS_DELIVERED', 'CONTACT_SEND_STATUS_FAILED', 'CONTACT_SEND_STATUS_SKIPPED'] as const).map(
              (s) => (
                <Button
                  key={s}
                  variant={statusFilter === s ? 'default' : 'outline'}
                  size="sm"
                  onClick={() => {
                    setStatusFilter(s)
                    setPage(1)
                  }}
                >
                  {s === '' ? 'All' : SEND_STATUS[s].label}
                </Button>
              ),
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <p className="text-sm text-muted-foreground py-4 text-center">Loading contacts...</p>
        ) : contacts.length === 0 ? (
          <p className="text-sm text-muted-foreground py-4 text-center">No contacts found.</p>
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Phone</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Sent At</TableHead>
                  <TableHead>Error</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {contacts.map((detail: CampaignContactDetail) => {
                  const sendCfg = SEND_STATUS[detail.campaignContact.status]
                  return (
                    <TableRow key={detail.campaignContact.contactId}>
                      <TableCell className="font-medium">{detail.contact.name || '-'}</TableCell>
                      <TableCell className="font-mono text-sm">
                        {formatPhone(detail.contact.phone)}
                      </TableCell>
                      <TableCell>
                        <StatusBadge label={sendCfg.label} variant={sendCfg.variant} dot={sendCfg.dot} />
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {detail.campaignContact.sentAt
                          ? new Date(detail.campaignContact.sentAt).toLocaleString()
                          : '-'}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground max-w-[200px] truncate">
                        {detail.campaignContact.error || '-'}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
            {data?.pagination && <Pagination pagination={data.pagination} onPageChange={setPage} />}
          </>
        )}
      </CardContent>
    </Card>
  )
}

export default function CampaignDetail() {
  const { id } = useParams({ strict: false }) as { id: string }
  const queryClient = useQueryClient()

  const wsProgress = useCampaignsStore((s) => s.progress[id])
  const wsStatusChange = useCampaignsStore((s) => s.statusChanges[id])

  const { data, isLoading, error } = useQuery({
    queryKey: ['campaign', id],
    queryFn: () => getCampaign(id),
    refetchInterval: 15000,
  })

  const pauseMutation = useMutation({
    mutationFn: () => pauseCampaign(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['campaign', id] }),
  })

  const resumeMutation = useMutation({
    mutationFn: () => resumeCampaign(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['campaign', id] }),
  })

  const cancelMutation = useMutation({
    mutationFn: () => cancelCampaign(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['campaign', id] }),
  })

  const actionPending = pauseMutation.isPending || resumeMutation.isPending || cancelMutation.isPending

  if (isLoading) {
    return (
      <div className="container max-w-5xl py-8">
        <p className="text-center text-muted-foreground py-16">Loading campaign...</p>
      </div>
    )
  }

  if (error || !data) {
    return (
      <div className="container max-w-5xl py-8">
        <Card>
          <CardContent className="p-8 text-center">
            <p className="text-destructive">
              {error instanceof Error ? error.message : 'Failed to load campaign'}
            </p>
          </CardContent>
        </Card>
      </div>
    )
  }

  const { campaign, template } = data
  const effectiveStatus = wsStatusChange?.status ?? campaign.status
  const statusCfg = CAMPAIGN_STATUS[effectiveStatus]

  const sent = wsProgress?.sentCount ?? campaign.sentCount
  const failed = wsProgress?.failedCount ?? campaign.failedCount
  const replied = wsProgress?.repliedCount ?? campaign.repliedCount
  const banned = wsProgress?.bannedCount ?? campaign.bannedCount
  const delivered = wsProgress?.deliveredCount ?? 0
  const total = wsProgress?.totalContacts ?? campaign.totalContacts
  const progressPercent = wsProgress?.progressPercent ?? (total > 0 ? Math.round((sent / total) * 100) : 0)

  const isPausedByBan =
    effectiveStatus === 'CAMPAIGN_STATUS_PAUSED' && wsStatusChange?.reason === 'ban_threshold'

  return (
    <div className="container max-w-5xl py-8 space-y-6">
      {/* Header */}
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-3xl font-bold tracking-tight">{campaign.name}</h1>
            <StatusBadge
              label={statusCfg.label}
              variant={statusCfg.variant}
              dot={statusCfg.dot}
              pulse={effectiveStatus === 'CAMPAIGN_STATUS_RUNNING'}
            />
          </div>
          <p className="text-muted-foreground mt-1">
            Template: {template?.name ?? 'Unknown'} &middot; Created{' '}
            {new Date(campaign.createdAt).toLocaleDateString()}
          </p>
        </div>
        <CampaignActions
          campaign={{ ...campaign, status: effectiveStatus }}
          onPause={() => pauseMutation.mutate()}
          onResume={() => resumeMutation.mutate()}
          onCancel={() => cancelMutation.mutate()}
          isPending={actionPending}
        />
      </div>

      {/* Auto-pause banner */}
      {isPausedByBan && (
        <div className="rounded-md border border-destructive/50 bg-destructive/10 p-4">
          <p className="font-semibold text-destructive">Campaign auto-paused: ban threshold reached</p>
          <p className="text-sm text-destructive/80 mt-1">
            The number of banned numbers has reached the configured threshold ({campaign.banPauseThreshold}).
            Review affected numbers before resuming.
          </p>
        </div>
      )}

      {/* Progress */}
      <Card>
        <CardContent className="p-6">
          <div className="flex items-center justify-between mb-2">
            <span className="text-sm font-medium">
              Progress: {formatNumber(sent)} / {formatNumber(total)} ({progressPercent}%)
            </span>
            <div className="flex gap-4 text-sm text-muted-foreground">
              {wsProgress && (
                <>
                  <span>{wsProgress.sendRatePerMin.toFixed(1)} msg/min</span>
                  <span>ETA: {formatEta(wsProgress.etaSeconds)}</span>
                </>
              )}
            </div>
          </div>
          <Progress value={progressPercent} />
        </CardContent>
      </Card>

      {/* Stats grid */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
        <StatCard label="Sent" value={sent} />
        <StatCard label="Delivered" value={delivered} />
        <StatCard label="Failed" value={failed} />
        <StatCard label="Replied" value={replied} />
        <StatCard
          label="Banned"
          value={banned}
          subtext={banned > 0 ? `Threshold: ${campaign.banPauseThreshold}` : undefined}
        />
      </div>

      <Separator />

      {/* Number breakdown */}
      <NumberProgressTable campaignId={id} wsProgress={wsProgress} />

      {/* Contacts table */}
      <ContactsTable campaignId={id} />
    </div>
  )
}
