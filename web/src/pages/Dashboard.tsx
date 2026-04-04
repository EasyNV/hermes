import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth'
import { getDashboardStats } from '@/api/dashboard'
import { Role } from '@/api/types'
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
      </div>
    </div>
  )
}
