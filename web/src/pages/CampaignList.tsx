import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Plus, MoreHorizontal, Play, Pause, RotateCcw, XCircle } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { listCampaigns, startCampaign, pauseCampaign, resumeCampaign, cancelCampaign } from '@/api/campaigns'
import { CampaignStatus } from '@/api/types'
import type { Campaign, CampaignStatus as CampaignStatusType } from '@/api/types'
import { formatNumber } from '@/lib/utils'
import { CAMPAIGN_STATUS } from '@/lib/constants'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { Pagination } from '@/components/shared/Pagination'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import {
  Table, TableHeader, TableBody, TableHead, TableRow, TableCell,
} from '@/components/ui/table'
import {
  Select, SelectTrigger, SelectValue, SelectContent, SelectItem,
} from '@/components/ui/select'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'

// ── Action Helpers ──────────────────────────────────────────
type CampaignAction = 'start' | 'pause' | 'resume' | 'cancel'

interface ActionConfig {
  label: string
  icon: React.ReactNode
  variant: 'default' | 'destructive'
}

const ACTION_CONFIG: Record<CampaignAction, ActionConfig> = {
  start: { label: 'Start', icon: <Play className="mr-2 h-4 w-4" />, variant: 'default' },
  pause: { label: 'Pause', icon: <Pause className="mr-2 h-4 w-4" />, variant: 'default' },
  resume: { label: 'Resume', icon: <RotateCcw className="mr-2 h-4 w-4" />, variant: 'default' },
  cancel: { label: 'Cancel', icon: <XCircle className="mr-2 h-4 w-4" />, variant: 'destructive' },
}

function getAvailableActions(status: CampaignStatusType): CampaignAction[] {
  switch (status) {
    case CampaignStatus.DRAFT:
    case CampaignStatus.SCHEDULED:
      return ['start', 'cancel']
    case CampaignStatus.RUNNING:
      return ['pause', 'cancel']
    case CampaignStatus.PAUSED:
      return ['resume', 'cancel']
    default:
      return []
  }
}

function getProgressPercent(campaign: Campaign): number {
  if (campaign.totalContacts === 0) return 0
  return Math.round(((campaign.sentCount + campaign.failedCount) / campaign.totalContacts) * 100)
}

// ── Status Filter Options ───────────────────────────────────
const STATUS_FILTER_OPTIONS: { value: string; label: string }[] = [
  { value: 'all', label: 'All Statuses' },
  { value: CampaignStatus.DRAFT, label: 'Draft' },
  { value: CampaignStatus.SCHEDULED, label: 'Scheduled' },
  { value: CampaignStatus.RUNNING, label: 'Running' },
  { value: CampaignStatus.PAUSED, label: 'Paused' },
  { value: CampaignStatus.COMPLETED, label: 'Completed' },
  { value: CampaignStatus.CANCELLED, label: 'Cancelled' },
]

// ── Main Page ───────────────────────────────────────────────
export default function CampaignList() {
  const workspace = useAuthStore((s) => s.workspace)
  const workspaceId = workspace?.id ?? ''
  const queryClient = useQueryClient()

  const [statusFilter, setStatusFilter] = useState<string>('all')
  const [page, setPage] = useState(1)
  const [confirmAction, setConfirmAction] = useState<{
    campaign: Campaign
    action: CampaignAction
  } | null>(null)

  const resolvedStatus = statusFilter === 'all' ? undefined : (statusFilter as CampaignStatusType)

  const { data, isLoading } = useQuery({
    queryKey: ['campaigns', workspaceId, resolvedStatus, page],
    queryFn: () =>
      listCampaigns({
        workspaceId,
        status: resolvedStatus,
        page,
        pageSize: 20,
      }),
    enabled: !!workspaceId,
  })

  const actionMutations = {
    start: useMutation({
      mutationFn: (id: string) => startCampaign(id),
      onSuccess: () => {
        queryClient.invalidateQueries({ queryKey: ['campaigns'] })
        setConfirmAction(null)
      },
    }),
    pause: useMutation({
      mutationFn: (id: string) => pauseCampaign(id),
      onSuccess: () => {
        queryClient.invalidateQueries({ queryKey: ['campaigns'] })
        setConfirmAction(null)
      },
    }),
    resume: useMutation({
      mutationFn: (id: string) => resumeCampaign(id),
      onSuccess: () => {
        queryClient.invalidateQueries({ queryKey: ['campaigns'] })
        setConfirmAction(null)
      },
    }),
    cancel: useMutation({
      mutationFn: (id: string) => cancelCampaign(id),
      onSuccess: () => {
        queryClient.invalidateQueries({ queryKey: ['campaigns'] })
        setConfirmAction(null)
      },
    }),
  }

  function handleActionClick(campaign: Campaign, action: CampaignAction) {
    setConfirmAction({ campaign, action })
  }

  function executeAction() {
    if (!confirmAction) return
    const { campaign, action } = confirmAction
    actionMutations[action].mutate(campaign.id)
  }

  const isActionPending =
    actionMutations.start.isPending ||
    actionMutations.pause.isPending ||
    actionMutations.resume.isPending ||
    actionMutations.cancel.isPending

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold tracking-tight">Campaigns</h1>
        <Button asChild>
          <Link to="/campaigns/new">
            <Plus className="mr-2 h-4 w-4" />
            New Campaign
          </Link>
        </Button>
      </div>

      {/* Status Filter */}
      <div className="max-w-[200px]">
        <Select
          value={statusFilter}
          onValueChange={(v) => { setStatusFilter(v); setPage(1) }}
        >
          <SelectTrigger>
            <SelectValue placeholder="Filter by status" />
          </SelectTrigger>
          <SelectContent>
            {STATUS_FILTER_OPTIONS.map((opt) => (
              <SelectItem key={opt.value} value={opt.value}>
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* Table */}
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Progress</TableHead>
              <TableHead className="text-right">Sent</TableHead>
              <TableHead className="text-right">Failed</TableHead>
              <TableHead className="text-right">Replied</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="w-[50px]" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {isLoading && (
              <TableRow>
                <TableCell colSpan={8} className="text-center py-8 text-muted-foreground">
                  Loading campaigns...
                </TableCell>
              </TableRow>
            )}
            {!isLoading && data?.campaigns.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} className="text-center py-8 text-muted-foreground">
                  No campaigns found.
                </TableCell>
              </TableRow>
            )}
            {data?.campaigns.map((campaign) => {
              const statusConfig = CAMPAIGN_STATUS[campaign.status]
              const progressPercent = getProgressPercent(campaign)
              const processed = campaign.sentCount + campaign.failedCount
              const actions = getAvailableActions(campaign.status)

              return (
                <TableRow key={campaign.id}>
                  <TableCell>
                    <Link
                      to="/campaigns/$id"
                      params={{ id: campaign.id }}
                      className="font-medium text-primary hover:underline"
                    >
                      {campaign.name}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <StatusBadge
                      label={statusConfig.label}
                      variant={statusConfig.variant}
                      dot={statusConfig.dot}
                      pulse={campaign.status === CampaignStatus.RUNNING}
                    />
                  </TableCell>
                  <TableCell className="min-w-[160px]">
                    <div className="flex items-center gap-2">
                      <Progress value={progressPercent} className="h-2 flex-1" />
                      <span className="text-xs text-muted-foreground whitespace-nowrap">
                        {formatNumber(processed)}/{formatNumber(campaign.totalContacts)} ({progressPercent}%)
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatNumber(campaign.sentCount)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {campaign.failedCount > 0 ? (
                      <span className="text-destructive">{formatNumber(campaign.failedCount)}</span>
                    ) : (
                      '0'
                    )}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatNumber(campaign.repliedCount)}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {new Date(campaign.createdAt).toLocaleDateString()}
                  </TableCell>
                  <TableCell>
                    {actions.length > 0 && (
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
                            <MoreHorizontal className="h-4 w-4" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          {actions.map((action, idx) => {
                            const config = ACTION_CONFIG[action]
                            const isDestructive = config.variant === 'destructive'
                            return (
                              <span key={action}>
                                {isDestructive && idx > 0 && <DropdownMenuSeparator />}
                                <DropdownMenuItem
                                  className={isDestructive ? 'text-destructive focus:text-destructive' : ''}
                                  onClick={() => handleActionClick(campaign, action)}
                                >
                                  {config.icon}
                                  {config.label}
                                </DropdownMenuItem>
                              </span>
                            )
                          })}
                        </DropdownMenuContent>
                      </DropdownMenu>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>

      {data?.pagination && (
        <Pagination pagination={data.pagination} onPageChange={setPage} />
      )}

      {/* Action Confirmation Dialog */}
      <ConfirmDialog
        open={!!confirmAction}
        onClose={() => setConfirmAction(null)}
        onConfirm={executeAction}
        title={
          confirmAction
            ? `${ACTION_CONFIG[confirmAction.action].label} Campaign`
            : ''
        }
        description={
          confirmAction
            ? `Are you sure you want to ${confirmAction.action} the campaign "${confirmAction.campaign.name}"?`
            : ''
        }
        confirmLabel={confirmAction ? ACTION_CONFIG[confirmAction.action].label : 'Confirm'}
        destructive={confirmAction?.action === 'cancel'}
        loading={isActionPending}
      />
    </div>
  )
}
