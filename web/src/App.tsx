import { createRootRoute, createRoute, createRouter, RouterProvider, Outlet, redirect, useNavigate } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { useAuthStore } from '@/stores/auth'
import { useWebSocket } from '@/hooks/useWebSocket'
import { Sidebar } from '@/components/layout/Sidebar'
import { TopBar } from '@/components/layout/TopBar'

// ── Page imports ──
import LoginPage from '@/pages/Login'
import DashboardPage from '@/pages/Dashboard'
import NumbersPage from '@/pages/Numbers'
import MbsSessionsPage from '@/pages/MbsSessions'
import ProxiesPage from '@/pages/Proxies'
import ContactsPage from '@/pages/Contacts'
import TemplatesPage from '@/pages/Templates'
import CampaignListPage from '@/pages/CampaignList'
import CampaignCreatePage from '@/pages/CampaignCreate'
import CampaignDetailPage from '@/pages/CampaignDetail'
import InboxPage from '@/pages/Inbox'
import SettingsPage from '@/pages/Settings'

// ── Query Client ──
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, retry: 1, refetchOnWindowFocus: false },
  },
})

// ── Auth Layout (wraps all protected routes) ──
function AuthLayout() {
  const isLoading = useAuthStore((s) => s.isLoading)
  useWebSocket()

  if (isLoading) {
    return (
      <div className="flex h-screen items-center justify-center">
        <p className="text-muted-foreground">Loading...</p>
      </div>
    )
  }

  return (
    <div className="flex h-screen">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <TopBar />
        <main className="flex-1 overflow-auto bg-muted/30 p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}

// ── Not Found ──
function NotFound() {
  const navigate = useNavigate()
  return (
    <div className="flex h-screen flex-col items-center justify-center gap-4">
      <h1 className="text-4xl font-bold">404</h1>
      <p className="text-muted-foreground">Page not found</p>
      <button className="text-primary underline" onClick={() => navigate({ to: '/' })}>
        Go home
      </button>
    </div>
  )
}

// ── Routes ──
const rootRoute = createRootRoute({
  component: () => <Outlet />,
  notFoundComponent: NotFound,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
})

const authRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'auth',
  component: AuthLayout,
  beforeLoad: async () => {
    const state = useAuthStore.getState()
    if (state.isLoading) await state.initialize()
    if (!useAuthStore.getState().isAuthenticated) {
      throw redirect({ to: '/login' })
    }
  },
})

const dashboardRoute = createRoute({ getParentRoute: () => authRoute, path: '/', component: DashboardPage })
const numbersRoute = createRoute({ getParentRoute: () => authRoute, path: '/numbers', component: NumbersPage })
const mbsSessionsRoute = createRoute({ getParentRoute: () => authRoute, path: '/mbs-sessions', component: MbsSessionsPage })
const proxiesRoute = createRoute({ getParentRoute: () => authRoute, path: '/proxies', component: ProxiesPage })
const contactsRoute = createRoute({ getParentRoute: () => authRoute, path: '/contacts', component: ContactsPage })
const templatesRoute = createRoute({ getParentRoute: () => authRoute, path: '/templates', component: TemplatesPage })
const campaignsRoute = createRoute({ getParentRoute: () => authRoute, path: '/campaigns', component: CampaignListPage })
const campaignNewRoute = createRoute({ getParentRoute: () => authRoute, path: '/campaigns/new', component: CampaignCreatePage })
const campaignDetailRoute = createRoute({ getParentRoute: () => authRoute, path: '/campaigns/$id', component: CampaignDetailPage })
const inboxRoute = createRoute({ getParentRoute: () => authRoute, path: '/inbox', component: InboxPage })
const settingsRoute = createRoute({ getParentRoute: () => authRoute, path: '/settings', component: SettingsPage })

const routeTree = rootRoute.addChildren([
  loginRoute,
  authRoute.addChildren([
    dashboardRoute,
    numbersRoute,
    mbsSessionsRoute,
    proxiesRoute,
    contactsRoute,
    templatesRoute,
    campaignsRoute,
    campaignNewRoute,
    campaignDetailRoute,
    inboxRoute,
    settingsRoute,
  ]),
])

const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

// ── App ──
export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <Toaster position="top-right" richColors />
    </QueryClientProvider>
  )
}
