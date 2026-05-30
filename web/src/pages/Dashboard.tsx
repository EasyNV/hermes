import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth'
import { getDashboardStats } from '@/api/dashboard'
import { listMbsSessions } from '@/api/mbs'
import { useMbsStore, selectSessions, sortSessionsByLastSeen } from '@/stores/mbs'
import { MbsSessionState, Role } from '@/api/types'
import { formatNumber } from '@/lib/utils'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import {
  Smartphone,
  MessageCircle,
  Megaphone,
  Inbox,
  Globe,
  ShieldAlert,
  Users,
  ArrowRight,
  Building2,
} from 'lucide-react'
import type { ReactNode } from 'react'

interface StatCardProps {
  title: string
  value: string
  subtitle?: string
  icon: ReactNode
  loading: boolean
}

function StatCard({ title, value, subtitle, icon, loading }: StatCardProps) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
        <div className="text-muted-foreground">{icon}</div>
      </CardHeader>
      <CardContent>
        {loading ? (
          <div className="space-y-2">
            <div className="h-7 w-24 animate-pulse rounded bg-muted" />
            {subtitle !== undefined && (
              <div className="h-4 w-32 animate-pulse rounded bg-muted" />
            )}
          </div>
        ) : (
          <>
            <div className="text-2xl font-bold">{value}</div>
            {subtitle && (
              <p className="text-xs text-muted-foreground">{subtitle}</p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}

export default function Dashboard() {
  const user = useAuthStore((s) => s.user)
  const tenant = useAuthStore((s) => s.tenant)
  const workspace = useAuthStore((s) => s.workspace)

  const { data, isLoading } = useQuery({
    queryKey: ['dashboard-stats', workspace?.id, tenant?.id],
    queryFn: () =>
      getDashboardStats({
        workspaceId: workspace?.id,
        tenantId: tenant?.id,
      }),
    enabled: !!workspace?.id || !!tenant?.id,
    refetchInterval: 30_000,
  })

  // ── MBS dashboard hydrate (Stage E2 chunk 6) ──────────────────────
  // Fetch up to 100 sessions to compute counts. For E2 tenants this is
  // sufficient; Stage F switches to a server-side aggregate.
  const tenantId = tenant?.id ?? ''
  const mbsListQuery = useQuery({
    queryKey: ['mbs', 'sessions', 'dashboard', tenantId],
    queryFn: () => listMbsSessions({ tenantId, page: 1, pageSize: 100 }),
    enabled: !!tenantId,
    refetchInterval: 30_000,
  })

  useEffect(() => {
    if (mbsListQuery.data?.sessions) {
      useMbsStore.getState().upsertList(mbsListQuery.data.sessions)
    }
  }, [mbsListQuery.data])

  const mbsSessionsDict = useMbsStore(selectSessions)
  const mbsSessions = useMemo(
    () => sortSessionsByLastSeen(mbsSessionsDict),
    [mbsSessionsDict],
  )
  const mbsCounts = useMemo(() => {
    let active = 0
    let warming = 0
    let burned = 0
    for (const s of mbsSessions) {
      if (s.state === MbsSessionState.ACTIVE) active++
      else if (s.state === MbsSessionState.WARMING) warming++
      else if (s.state === MbsSessionState.BURNED) burned++
    }
    return { active, warming, burned, total: mbsSessions.length }
  }, [mbsSessions])

  const lastInbound = useMbsStore((s) => s.lastInboundByThread)
  const recentInbound = useMemo(
    () =>
      Object.values(lastInbound)
        .sort((a, b) => (b.receivedAt || '').localeCompare(a.receivedAt || ''))
        .slice(0, 5),
    [lastInbound],
  )

  const isAgent = user?.role === Role.CS_AGENT

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">Dashboard</h1>
        <p className="text-muted-foreground">
          Overview of your WhatsApp automation platform
        </p>
      </div>

      {isAgent && (
        <Card className="border-primary/20 bg-primary/5">
          <CardContent className="flex items-center justify-between p-4">
            <div className="flex items-center gap-3">
              <Inbox className="h-5 w-5 text-primary" />
              <div>
                <p className="font-medium">Agent Inbox</p>
                <p className="text-sm text-muted-foreground">
                  {isLoading
                    ? 'Loading...'
                    : `${formatNumber(data?.unassignedConversations ?? 0)} unassigned conversations waiting`}
                </p>
              </div>
            </div>
            <Button asChild size="sm">
              <Link to="/inbox">
                Open Inbox
                <ArrowRight className="ml-2 h-4 w-4" />
              </Link>
            </Button>
          </CardContent>
        </Card>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <StatCard
          title="Active Numbers"
          value={
            data
              ? `${formatNumber(data.activeNumbers)} / ${formatNumber(data.totalNumbers)}`
              : '-'
          }
          subtitle={
            data
              ? `${data.totalNumbers > 0 ? Math.round((data.activeNumbers / data.totalNumbers) * 100) : 0}% online`
              : undefined
          }
          icon={<Smartphone className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="Messages Today"
          value={
            data
              ? formatNumber(data.messagesSentToday + data.messagesReceivedToday)
              : '-'
          }
          subtitle={
            data
              ? `${formatNumber(data.messagesSentToday)} sent, ${formatNumber(data.messagesReceivedToday)} received`
              : undefined
          }
          icon={<MessageCircle className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="Active Campaigns"
          value={data ? formatNumber(data.activeCampaigns) : '-'}
          subtitle="Currently running"
          icon={<Megaphone className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="Unassigned Conversations"
          value={data ? formatNumber(data.unassignedConversations) : '-'}
          subtitle="Awaiting agent response"
          icon={<Inbox className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="Active Proxies"
          value={
            data
              ? `${formatNumber(data.activeProxies)} / ${formatNumber(data.totalProxies)}`
              : '-'
          }
          subtitle={
            data
              ? `${data.totalProxies > 0 ? Math.round((data.activeProxies / data.totalProxies) * 100) : 0}% healthy`
              : undefined
          }
          icon={<Globe className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="Bans Today"
          value={data ? formatNumber(data.bansToday) : '-'}
          subtitle="Numbers flagged or banned"
          icon={<ShieldAlert className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="Total Contacts"
          value={data ? formatNumber(data.totalContacts) : '-'}
          subtitle="Across all campaigns"
          icon={<Users className="h-4 w-4" />}
          loading={isLoading}
        />
        <StatCard
          title="MBS Pages"
          value={
            mbsListQuery.isLoading && mbsSessions.length === 0
              ? '-'
              : `${formatNumber(mbsCounts.active)} / ${formatNumber(mbsCounts.total)}`
          }
          subtitle={
            mbsListQuery.isLoading && mbsSessions.length === 0
              ? undefined
              : `${formatNumber(mbsCounts.warming)} warming, ${formatNumber(mbsCounts.burned)} burned`
          }
          icon={<Building2 className="h-4 w-4" />}
          loading={mbsListQuery.isLoading && mbsSessions.length === 0}
        />
      </div>

      {/* Recent MBS inbound — only show if we have any */}
      {recentInbound.length > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center justify-between text-sm font-medium">
              <span>Recent MBS messages</span>
              <Button asChild variant="ghost" size="sm" className="h-7 text-xs">
                <Link to="/mbs-sessions">
                  View sessions
                  <ArrowRight className="ml-1 h-3 w-3" />
                </Link>
              </Button>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-2">
              {recentInbound.map((m) => (
                <li key={m.mid} className="rounded border p-2 text-xs">
                  <div className="flex items-center justify-between">
                    <span className="font-mono">{m.senderPhone || '—'}</span>
                    <span className="text-muted-foreground">
                      {m.receivedAt
                        ? new Date(m.receivedAt).toLocaleString(undefined, {
                            month: 'short',
                            day: 'numeric',
                            hour: '2-digit',
                            minute: '2-digit',
                          })
                        : '—'}
                    </span>
                  </div>
                  <p className="mt-1 truncate text-muted-foreground">{m.text}</p>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
