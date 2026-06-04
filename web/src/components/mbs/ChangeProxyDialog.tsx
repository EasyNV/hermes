import { useMemo, useState, useEffect } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Loader2 } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useMbsStore } from '@/stores/mbs'
import { listProxies } from '@/api/proxies'
import { setMbsSessionProxy } from '@/api/mbs'
import { ProxyStatus, ProxyType } from '@/api/types'
import type { Proxy } from '@/api/types'
import { ApiError } from '@/api/client'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogFooter,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from '@/components/ui/select'

const AUTO = '__auto__'

// proxyLabel renders a credential-free "host:port (type)" string for the picker.
function proxyLabel(p: Proxy): string {
  const scheme = p.type === ProxyType.HTTP ? 'http' : 'socks5'
  return `${p.host}:${p.port} (${scheme})`
}

function statusDot(status: ProxyStatus): string {
  switch (status) {
    case ProxyStatus.ACTIVE:
      return 'bg-emerald-500'
    case ProxyStatus.DEAD:
      return 'bg-red-500'
    case ProxyStatus.FLAGGED:
      return 'bg-amber-500'
    default:
      return 'bg-muted-foreground'
  }
}

interface Props {
  open: boolean
  uid: string
  /** The session's currently-pinned proxyId, if any (preselects it). */
  currentProxyId?: string
  onOpenChange: (open: boolean) => void
}

/**
 * ChangeProxyDialog assigns or changes the sticky proxy a MBS session connects
 * through. "Auto from pool" (default) sends an empty proxyId so the backend
 * picks the cleanest/least-loaded proxy. The backend pins the choice + triggers
 * an immediate reconnect through the new proxy; we reconcile the returned
 * session into the store. Admins only (the gateway enforces RBAC; a cs_agent
 * caller gets a 403 surfaced as a toast).
 */
export function ChangeProxyDialog({ open, uid, currentProxyId, onOpenChange }: Props) {
  const tenantId = useAuthStore((s) => s.tenant?.id) ?? ''
  const [selected, setSelected] = useState<string>(currentProxyId || AUTO)

  // Reset the selection whenever the dialog re-opens for a (possibly different)
  // session so we don't carry a stale pick across rows.
  useEffect(() => {
    if (open) setSelected(currentProxyId || AUTO)
  }, [open, currentProxyId])

  const proxiesQuery = useQuery({
    queryKey: ['proxies', 'list', tenantId],
    queryFn: () => listProxies({ tenantId, page: 1, pageSize: 100 }),
    enabled: open && !!tenantId,
  })

  const proxies = useMemo(
    () => proxiesQuery.data?.proxies ?? [],
    [proxiesQuery.data],
  )

  const setProxyMut = useMutation({
    mutationFn: () =>
      setMbsSessionProxy(uid, selected === AUTO ? undefined : selected),
    onSuccess: ({ session }) => {
      useMbsStore.getState().upsertOne(session)
      toast.success(
        session.proxyLabel
          ? `Proxy set to ${session.proxyLabel}`
          : 'Proxy assigned — reconnecting',
      )
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      const msg = e instanceof ApiError ? e.message : 'Failed to set proxy'
      toast.error(msg)
    },
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Change proxy</DialogTitle>
          <DialogDescription>
            Pick the proxy this session connects through. The choice is sticky —
            reused across reconnects and self-heal. Changing it reconnects the
            session immediately.
          </DialogDescription>
        </DialogHeader>

        <div className="py-2">
          {proxiesQuery.isLoading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading proxies…
            </div>
          ) : (
            <Select value={selected} onValueChange={setSelected}>
              <SelectTrigger>
                <SelectValue placeholder="Select a proxy" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={AUTO}>Auto (best from pool)</SelectItem>
                {proxies.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    <span className="flex items-center gap-2">
                      <span
                        className={`inline-block h-2 w-2 shrink-0 rounded-full ${statusDot(p.status)}`}
                      />
                      <span>{proxyLabel(p)}</span>
                      <span className="text-xs text-muted-foreground">
                        · {p.assignedCount} assigned
                      </span>
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {proxiesQuery.isError && (
            <p className="mt-2 text-xs text-amber-600 dark:text-amber-400">
              Couldn't load the proxy pool. You can still pick "Auto".
            </p>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={setProxyMut.isPending}
          >
            Cancel
          </Button>
          <Button onClick={() => setProxyMut.mutate()} disabled={setProxyMut.isPending}>
            {setProxyMut.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            Apply
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
