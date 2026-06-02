import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  MoreHorizontal,
  Plus,
  Flame,
  ChevronDown,
  ChevronRight,
  Loader2,
  Building2,
} from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useMbsStore, selectSessions, sortSessionsByLastSeen } from '@/stores/mbs'
import {
  listMbsSessions,
  getMbsSessionStatus,
  listMbsSessionAssets,
  burnMbsSession,
} from '@/api/mbs'
import { MbsSessionState } from '@/api/types'
import type { MbsSessionState as MbsSessionStateT } from '@/api/types'
import { MBS_STATUS } from '@/lib/constants'
import { ApiError } from '@/api/client'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { Pagination } from '@/components/shared/Pagination'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'
import { BridgeLoginDialog } from '@/components/mbs/BridgeLoginDialog'
import { ColdComposeForm } from '@/components/mbs/ColdComposeForm'
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from '@/components/ui/select'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'

const PAGE_SIZE = 20

const STATE_OPTIONS: { value: string; label: string }[] = [
  { value: 'ALL', label: 'All States' },
  { value: MbsSessionState.WARMING, label: 'Warming' },
  { value: MbsSessionState.ACTIVE, label: 'Active' },
  { value: MbsSessionState.RECONNECTING, label: 'Reconnecting' },
  { value: MbsSessionState.REFRESHING, label: 'Refreshing' },
  { value: MbsSessionState.SUSPENDED, label: 'Suspended' },
  { value: MbsSessionState.BURNED, label: 'Burned' },
]

function shortDate(iso: string): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    return d.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    })
  } catch {
    return iso
  }
}

function cookieFreshness(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso).getTime()
  const now = Date.now()
  const days = Math.round((d - now) / 86_400_000)
  if (days < 0) return `expired ${-days}d ago`
  if (days < 1) return 'today'
  return `in ${days}d`
}

export default function MbsSessions() {
  const tenantId = useAuthStore((s) => s.tenant?.id) ?? ''
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  // Default to ACTIVE — the common operator view. Burned/suspended sessions
  // are noise unless explicitly filtered for. 'ALL' is still available in the
  // dropdown.
  const [stateFilter, setStateFilter] = useState<string>(MbsSessionState.ACTIVE)
  const [bridgeOpen, setBridgeOpen] = useState(false)
  const [confirmBurn, setConfirmBurn] = useState<{ open: boolean; uid: string }>({
    open: false,
    uid: '',
  })
  const [expandedUid, setExpandedUid] = useState<string>('')

  // ── Sessions list ─────────────────────────────────────────────
  const sessionsQuery = useQuery({
    queryKey: ['mbs', 'sessions', tenantId, page, stateFilter],
    queryFn: () =>
      listMbsSessions({
        tenantId,
        state:
          stateFilter === 'ALL' ? undefined : (stateFilter as MbsSessionStateT),
        page,
        pageSize: PAGE_SIZE,
      }),
    enabled: !!tenantId,
    refetchInterval: 30_000, // recovers lifecycle events dropped for unknown uids
  })

  // Sync react-query results into Zustand so WS lifecycle frames
  // mutate the same rows the table is rendering.
  useEffect(() => {
    if (sessionsQuery.data?.sessions) {
      useMbsStore.getState().upsertList(sessionsQuery.data.sessions)
    }
  }, [sessionsQuery.data])

  // Read from store so WS push updates show without a refetch.
  // Subscribe to the raw dict (stable ref) and derive the sorted view
  // in a useMemo — a selector that re-sorts on every call would return
  // a fresh array per render and trip Zustand v5's Object.is check,
  // causing React error #185 (max update depth).
  const storeSessionsDict = useMbsStore(selectSessions)
  const storeSessions = useMemo(
    () => sortSessionsByLastSeen(storeSessionsDict),
    [storeSessionsDict],
  )

  // Prefer store data once we have it; fall back to the raw query
  // until first hydrate.
  const sessions = storeSessions.length > 0 ? storeSessions : sessionsQuery.data?.sessions ?? []
  const pagination = sessionsQuery.data?.pagination

  // ── Mutations ─────────────────────────────────────────────────
  const burnMut = useMutation({
    mutationFn: ({ uid }: { uid: string }) => burnMbsSession(uid),
    onSuccess: ({ session }) => {
      useMbsStore.getState().upsertOne(session)
      toast.success(`Session ${session.uid} burned`)
      setConfirmBurn({ open: false, uid: '' })
      queryClient.invalidateQueries({ queryKey: ['mbs', 'sessions'] })
    },
    onError: (e: unknown) => {
      const msg = e instanceof ApiError ? e.message : 'Burn failed'
      toast.error(msg)
    },
  })

  // ── Assets drawer fetch ───────────────────────────────────────
  // staleTime:0 + refetchOnMount guarantees a fresh fetch every time the
  // drawer expands. Without this, an empty result cached before assets were
  // discovered (e.g. drawer opened seconds after login) sticks around and
  // shows a false "No assets attached" until a hard refresh.
  const assetsQuery = useQuery({
    queryKey: ['mbs', 'assets', expandedUid],
    queryFn: () => listMbsSessionAssets(expandedUid),
    enabled: !!expandedUid,
    staleTime: 0,
    refetchOnMount: 'always',
  })

  // ── Manual status refetch on demand (used after bridge success) ─
  const refreshSession = useCallback(async (uid: string) => {
    const { session } = await getMbsSessionStatus(uid)
    useMbsStore.getState().upsertOne(session)
  }, [])

  const handleBridgeSuccess = useCallback(({ uid }: { uid: string }) => {
    // Pull a fresh status so the row reflects state, podId, etc.
    refreshSession(uid).catch(() => {
      // Refetch fallback if status RPC is flaky.
      queryClient.invalidateQueries({ queryKey: ['mbs', 'sessions'] })
    })
    // Stable toast id: dedupes if the success effect ever re-fires for
    // the same uid (sonner replaces same-id toasts instead of stacking).
    toast.success(`Logged in as ${uid}`, { id: `mbs-login-${uid}` })
  }, [queryClient, refreshSession])

  // Reset to page 1 whenever filter changes.
  const onFilterChange = (v: string) => {
    setStateFilter(v)
    setPage(1)
    setExpandedUid('')
  }

  // Memoize visible sessions. The backend already filters by stateFilter, but
  // the table renders from the Zustand store (storeSessions), which WS
  // lifecycle frames mutate via upsertOne — and those frames can carry ANY
  // state (e.g. a burn arriving while you're viewing "Active"). Without a
  // client-side guard, a non-matching session would leak into a filtered view.
  // Re-apply the filter here so store/WS updates stay consistent with the
  // selected filter. 'ALL' shows everything.
  const visibleSessions = useMemo(() => {
    if (stateFilter === 'ALL') return sessions
    return sessions.filter((s) => s.state === stateFilter)
  }, [sessions, stateFilter])

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">MBS Pages</h1>
          <p className="text-sm text-muted-foreground">
            Meta Business Suite sessions used for native Pages/WEC messaging.
          </p>
        </div>
        <Button onClick={() => setBridgeOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          Login new account
        </Button>
      </div>

      {/* Filter row */}
      <div className="flex items-center gap-2">
        <Select value={stateFilter} onValueChange={onFilterChange}>
          <SelectTrigger className="w-[200px]">
            <SelectValue placeholder="Filter state" />
          </SelectTrigger>
          <SelectContent>
            {STATE_OPTIONS.map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {sessionsQuery.isFetching && (
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        )}
      </div>

      {/* Table */}
      <div className="rounded-md border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-10" />
              <TableHead>Status</TableHead>
              <TableHead>UID / FBID</TableHead>
              <TableHead>Pod</TableHead>
              <TableHead>Last seen</TableHead>
              <TableHead>Cookie</TableHead>
              <TableHead className="w-10 text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sessionsQuery.isLoading ? (
              <TableRow>
                <TableCell colSpan={7} className="py-8 text-center text-sm text-muted-foreground">
                  <Loader2 className="mx-auto h-5 w-5 animate-spin" />
                </TableCell>
              </TableRow>
            ) : visibleSessions.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="py-12 text-center text-sm text-muted-foreground">
                  <Building2 className="mx-auto mb-2 h-8 w-8 opacity-40" />
                  No MBS sessions yet. Click <span className="font-medium">Login new account</span> to begin.
                </TableCell>
              </TableRow>
            ) : (
              visibleSessions.map((s) => {
                const cfg = MBS_STATUS[s.state] ?? MBS_STATUS.MBS_SESSION_STATE_UNSPECIFIED
                const isExpanded = expandedUid === s.uid
                return (
                  <>
                    <TableRow key={s.uid}>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-8 w-8 p-0"
                          onClick={() => setExpandedUid(isExpanded ? '' : s.uid)}
                          aria-label={isExpanded ? 'Collapse' : 'Expand'}
                        >
                          {isExpanded ? (
                            <ChevronDown className="h-4 w-4" />
                          ) : (
                            <ChevronRight className="h-4 w-4" />
                          )}
                        </Button>
                      </TableCell>
                      <TableCell>
                        <StatusBadge
                          label={cfg.label}
                          variant={cfg.variant}
                          dot={cfg.dot}
                          pulse={
                            s.state === MbsSessionState.WARMING ||
                            s.state === MbsSessionState.RECONNECTING
                          }
                        />
                      </TableCell>
                      <TableCell>
                        <div className="font-mono text-xs">
                          <div>{s.uid}</div>
                          <div className="text-muted-foreground">{s.fbid || '—'}</div>
                          {s.loginEmail && (
                            <div className="text-muted-foreground font-sans mt-0.5">
                              {s.loginEmail}
                            </div>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {s.podId || '—'}
                      </TableCell>
                      <TableCell className="text-xs">{shortDate(s.lastSeenAt)}</TableCell>
                      <TableCell className="text-xs">
                        {cookieFreshness(s.cookieExpiresAt)}
                      </TableCell>
                      <TableCell className="text-right">
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
                              <MoreHorizontal className="h-4 w-4" />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem
                              onClick={() => setExpandedUid(isExpanded ? '' : s.uid)}
                            >
                              {isExpanded ? 'Hide assets' : 'Show assets'}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem
                              onClick={() => setConfirmBurn({ open: true, uid: s.uid })}
                              disabled={s.state === MbsSessionState.BURNED}
                              className="text-destructive focus:text-destructive"
                            >
                              <Flame className="mr-2 h-4 w-4" />
                              Burn session
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </TableCell>
                    </TableRow>
                    {isExpanded && (
                      <TableRow key={`${s.uid}-assets`}>
                        <TableCell colSpan={7} className="bg-muted/40 p-4">
                          {s.state === MbsSessionState.BURNED && s.burnedReason && (
                            <div className="mb-3 rounded-md bg-red-50 px-3 py-2 text-xs text-red-700 dark:bg-red-950/30 dark:text-red-300">
                              <span className="font-medium">Burned:</span>{' '}
                              {s.burnedReason}{' '}
                              <span className="text-muted-foreground">
                                ({shortDate(s.burnedAt)})
                              </span>
                            </div>
                          )}
                          {assetsQuery.isLoading ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : assetsQuery.data?.assets?.length ? (
                            <div className="space-y-1">
                              <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                                Assets ({assetsQuery.data.assets.length})
                              </div>
                              <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
                                {assetsQuery.data.assets.map((a) => (
                                  <li
                                    key={a.pageId || a.wecMailboxId || a.wabaId}
                                    className={`rounded border bg-card px-2 py-1.5 text-xs ${
                                      a.isPrimary ? 'ring-1 ring-primary' : ''
                                    }`}
                                  >
                                    <div className="flex items-center justify-between font-medium">
                                      <span className="truncate">
                                        {a.pageName || a.pageId || a.wecMailboxId || '—'}
                                      </span>
                                      {a.isPrimary && (
                                        <span className="ml-1 shrink-0 rounded bg-primary/15 px-1 text-[9px] font-semibold uppercase tracking-wide text-primary">
                                          Primary
                                        </span>
                                      )}
                                    </div>
                                    <div className="mt-0.5 space-y-0.5 text-[10px] text-muted-foreground">
                                      {a.pageId && (
                                        <div>
                                          <span className="uppercase tracking-wide">Page</span>
                                          <span className="ml-1 font-mono">{a.pageId}</span>
                                        </div>
                                      )}
                                      {a.businessName && <div>biz: {a.businessName}</div>}
                                      {a.businessId && !a.businessName && (
                                        <div>
                                          <span className="uppercase tracking-wide">Biz</span>
                                          <span className="ml-1 font-mono">{a.businessId}</span>
                                        </div>
                                      )}
                                      {a.wabaId && (
                                        <div>
                                          <span className="uppercase tracking-wide">WABA</span>
                                          <span className="ml-1 font-mono">{a.wabaId}</span>
                                        </div>
                                      )}
                                      {a.wecPhoneNumber && (
                                        <div>
                                          <span className="uppercase tracking-wide">WEC</span>
                                          <span className="ml-1 font-mono">
                                            +{a.wecPhoneNumber}
                                          </span>
                                          <span
                                            className={`ml-1 ${
                                              a.wecAccountRegistered
                                                ? 'text-emerald-500'
                                                : 'text-amber-500'
                                            }`}
                                            title={
                                              a.wecAccountRegistered
                                                ? 'WEC account registered (send-to-phone enabled)'
                                                : 'WEC account NOT registered (send-to-phone disabled)'
                                            }
                                          >
                                            {a.wecAccountRegistered ? '✓' : '✗'}
                                          </span>
                                        </div>
                                      )}
                                    </div>
                                  </li>
                                ))}
                              </ul>
                            </div>
                          ) : assetsQuery.isError ? (
                            <div className="flex items-center gap-2 text-xs text-amber-600 dark:text-amber-400">
                              <span>Couldn't load assets.</span>
                              <button
                                type="button"
                                className="underline underline-offset-2 hover:text-amber-700 dark:hover:text-amber-300"
                                onClick={() => assetsQuery.refetch()}
                              >
                                Retry
                              </button>
                            </div>
                          ) : (
                            <p className="text-xs text-muted-foreground">
                              No assets attached to this session.
                            </p>
                          )}

                          {/* Cold-compose composer — only on ACTIVE sessions.
                              Drawer collapse unmounts and resets composer state. */}
                          {s.state === MbsSessionState.ACTIVE ? (
                            <div className="mt-4 rounded-md border bg-card p-3">
                              <ColdComposeForm uid={s.uid} />
                            </div>
                          ) : (
                            <p className="mt-4 text-xs text-muted-foreground">
                              Cold compose available only on active sessions.
                            </p>
                          )}
                        </TableCell>
                      </TableRow>
                    )}
                  </>
                )
              })
            )}
          </TableBody>
        </Table>
      </div>

      {/* Pagination */}
      {pagination && pagination.totalPages > 1 && (
        <Pagination pagination={pagination} onPageChange={setPage} />
      )}

      {/* Bridge-login dialog */}
      <BridgeLoginDialog
        open={bridgeOpen}
        onOpenChange={setBridgeOpen}
        onSuccess={handleBridgeSuccess}
      />

      {/* Burn confirm */}
      <ConfirmDialog
        open={confirmBurn.open}
        onClose={() => setConfirmBurn({ open: false, uid: '' })}
        onConfirm={() => burnMut.mutate({ uid: confirmBurn.uid })}
        title="Burn this MBS session?"
        description={
          'The session will be marked BURNED and the encrypted credentials at rest will be zeroized. ' +
          'You will need to login again to use this account.'
        }
        confirmLabel="Burn"
        destructive
        loading={burnMut.isPending}
      />
    </div>
  )
}
