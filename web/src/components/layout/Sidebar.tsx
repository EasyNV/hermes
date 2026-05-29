import { Link } from '@tanstack/react-router'
import {
  LayoutDashboard, Smartphone, Building2, Shield, Users, FileText,
  Send, MessageSquare, Settings, Zap,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth'
import { Role } from '@/api/types'

const navItems = [
  { to: '/' as const, label: 'Dashboard', icon: LayoutDashboard },
  { to: '/numbers' as const, label: 'Numbers', icon: Smartphone },
  { to: '/mbs-sessions' as const, label: 'MBS Pages', icon: Building2 },
  { to: '/proxies' as const, label: 'Proxies', icon: Shield },
  { to: '/contacts' as const, label: 'Contacts', icon: Users },
  { to: '/templates' as const, label: 'Templates', icon: FileText },
  { to: '/campaigns' as const, label: 'Campaigns', icon: Send },
  { to: '/inbox' as const, label: 'Inbox', icon: MessageSquare },
  { to: '/settings' as const, label: 'Settings', icon: Settings },
]

export function Sidebar() {
  const user = useAuthStore((s) => s.user)

  const visible = user?.role === Role.CS_AGENT
    ? navItems.filter((i) => i.to === '/' || i.to === '/inbox')
    : navItems

  return (
    <aside className="hidden w-[var(--sidebar-width)] shrink-0 border-r bg-card md:block">
      <div className="flex h-14 items-center border-b px-4">
        <Zap className="mr-2 h-5 w-5 text-primary" />
        <span className="text-lg font-bold">Hermes</span>
      </div>
      <nav className="flex flex-col gap-1 p-3">
        {visible.map((item) => (
          <Link
            key={item.to}
            to={item.to}
            activeOptions={{ exact: item.to === '/' }}
            className={cn(
              'flex items-center gap-3 rounded-lg px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground',
            )}
            activeProps={{
              className:
                'flex items-center gap-3 rounded-lg px-3 py-2 text-sm bg-accent text-accent-foreground font-medium transition-colors',
            }}
          >
            <item.icon className="h-4 w-4" />
            {item.label}
          </Link>
        ))}
      </nav>
    </aside>
  )
}
