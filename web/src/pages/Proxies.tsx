import { useState, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth'
import { listProxies, addProxies, getProxyHealth, deleteProxy } from '@/api/proxies'
import type { ProxyStatus, ProxyInput, ProxyType, Proxy, GetProxyHealthResponse } from '@/api/types'
import { PROXY_STATUS } from '@/lib/constants'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { Pagination } from '@/components/shared/Pagination'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { MoreHorizontal, Plus, Activity, Trash2, Loader2 } from 'lucide-react'

const PAGE_SIZE = 20

const STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: 'ALL', label: 'All Statuses' },
  { value: 'PROXY_STATUS_ACTIVE', label: 'Active' },
  { value: 'PROXY_STATUS_DEAD', label: 'Dead' },
  { value: 'PROXY_STATUS_FLAGGED', label: 'Flagged' },
]

function formatDate(dateStr: string | undefined): string {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

interface ParsedProxy {
  host: string
  port: number
  username: string
  password: string
}

function parseProxyLines(text: string): { parsed: ParsedProxy[]; errors: string[] } {
  const lines = text.split('\n').map((l) => l.trim()).filter(Boolean)
  const parsed: ParsedProxy[] = []
  const errors: string[] = []

  for (let i = 0; i < lines.length; i++) {
    const parts = lines[i].split(':')
    if (parts.length < 2) {
      errors.push(`Line ${i + 1}: invalid format (expected host:port or host:port:user:pass)`)
      continue
    }

    const host = parts[0]
    const port = parseInt(parts[1], 10)
    if (isNaN(port) || port < 1 || port > 65535) {
      errors.push(`Line ${i + 1}: invalid port "${parts[1]}"`)
      continue
    }

    parsed.push({
      host,
      port,
      username: parts[2] ?? '',
      password: parts[3] ?? '',
    })
  }

  return { parsed, errors }
}

export default function Proxies() {
  const tenant = useAuthStore((s) => s.tenant)
  const tenantId = tenant?.id ?? ''
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<string>('ALL')
  const [uploadOpen, setUploadOpen] = useState(false)
  const [uploadText, setUploadText] = useState('')
  const [uploadType, setUploadType] = useState<ProxyType>('PROXY_TYPE_SOCKS5')
  const [uploadResult, setUploadResult] = useState<{ added: number; skipped: number } | null>(null)
  const [uploadErrors, setUploadErrors] = useState<string[]>([])
  const [confirmDelete, setConfirmDelete] = useState<{ open: boolean; proxy: Proxy | null }>({
    open: false,
    proxy: null,
  })
  const [healthResult, setHealthResult] = useState<{
    open: boolean
    data: GetProxyHealthResponse | null
    loading: boolean
    error: string | null
  }>({ open: false, data: null, loading: false, error: null })

  const statusParam = statusFilter === 'ALL' ? undefined : (statusFilter as ProxyStatus)

  // --- Queries ---
  const { data, isLoading } = useQuery({
    queryKey: ['proxies', tenantId, page, statusFilter],
    queryFn: () => listProxies({ tenantId, page, pageSize: PAGE_SIZE, status: statusParam }),
    enabled: !!tenantId,
  })

  // --- Mutations ---
  const addMutation = useMutation({
    mutationFn: (proxies: ProxyInput[]) => addProxies(tenantId, proxies),
    onSuccess: (res) => {
      setUploadResult({ added: res.proxies.length, skipped: res.skippedCount })
      queryClient.invalidateQueries({ queryKey: ['proxies'] })
    },
  })

  const deleteMutation = useMutation({
    mutationFn: deleteProxy,
    onSuccess: () => {
      setConfirmDelete({ open: false, proxy: null })
      queryClient.invalidateQueries({ queryKey: ['proxies'] })
    },
  })

  const resetUploadForm = useCallback(() => {
    setUploadText('')
    setUploadType('PROXY_TYPE_SOCKS5')
    setUploadResult(null)
    setUploadErrors([])
  }, [])

  function handleBulkUpload() {
    const { parsed, errors } = parseProxyLines(uploadText)
    if (errors.length > 0) {
      setUploadErrors(errors)
      return
    }
    if (parsed.length === 0) {
      setUploadErrors(['No valid proxy lines found.'])
      return
    }
    setUploadErrors([])

    const proxies: ProxyInput[] = parsed.map((p) => ({
      host: p.host,
      port: p.port,
      username: p.username,
      password: p.password,
      type: uploadType,
    }))

    addMutation.mutate(proxies)
  }

  function handleHealthCheck(proxyId: string) {
    setHealthResult({ open: true, data: null, loading: true, error: null })
    getProxyHealth(proxyId)
      .then((res) => {
        setHealthResult({ open: true, data: res, loading: false, error: null })
      })
      .catch((err: Error) => {
        setHealthResult({ open: true, data: null, loading: false, error: err.message })
      })
  }

  function handleStatusFilterChange(value: string) {
    setStatusFilter(value)
    setPage(1)
  }

  const proxies = data?.proxies ?? []
  const pagination = data?.pagination ?? { total: 0, page: 1, pageSize: PAGE_SIZE, totalPages: 1 }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Proxies</h1>
          <p className="text-muted-foreground">Manage your proxy pool for WhatsApp connections.</p>
        </div>
        <Button onClick={() => setUploadOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          Add Proxies
        </Button>
      </div>

      {/* Filters */}
      <div className="flex items-center gap-4">
        <Select value={statusFilter} onValueChange={handleStatusFilterChange}>
          <SelectTrigger className="w-[200px]">
            <SelectValue placeholder="Filter by status" />
          </SelectTrigger>
          <SelectContent>
            {STATUS_OPTIONS.map((opt) => (
              <SelectItem key={opt.value} value={opt.value}>
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* Table */}
      {isLoading ? (
        <div className="flex items-center justify-center py-12">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      ) : proxies.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
          <p>No proxies found.</p>
        </div>
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Host:Port</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Ban Count</TableHead>
                <TableHead>Assigned</TableHead>
                <TableHead>Last Health Check</TableHead>
                <TableHead className="w-[50px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {proxies.map((proxy) => {
                const statusCfg = PROXY_STATUS[proxy.status]
                return (
                  <TableRow key={proxy.id}>
                    <TableCell className="font-mono">
                      {proxy.host}:{proxy.port}
                    </TableCell>
                    <TableCell>
                      {proxy.type === 'PROXY_TYPE_SOCKS5' ? 'SOCKS5' : proxy.type === 'PROXY_TYPE_HTTP' ? 'HTTP' : 'Unknown'}
                    </TableCell>
                    <TableCell>
                      <StatusBadge
                        label={statusCfg.label}
                        variant={statusCfg.variant}
                        dot={statusCfg.dot}
                        pulse={proxy.status === 'PROXY_STATUS_ACTIVE'}
                      />
                    </TableCell>
                    <TableCell>
                      <span className={proxy.banCount > 0 ? 'font-semibold text-red-600' : ''}>
                        {proxy.banCount}
                      </span>
                    </TableCell>
                    <TableCell>{proxy.assignedCount}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {formatDate(proxy.lastHealthCheck)}
                    </TableCell>
                    <TableCell>
                      <ProxyActions
                        onHealthCheck={() => handleHealthCheck(proxy.id)}
                        onDelete={() => setConfirmDelete({ open: true, proxy })}
                      />
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>

          <Pagination pagination={pagination} onPageChange={setPage} />
        </>
      )}

      {/* Bulk Upload Dialog */}
      <Dialog open={uploadOpen} onOpenChange={(v) => { if (!v) { setUploadOpen(false); resetUploadForm() } }}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Add Proxies</DialogTitle>
            <DialogDescription>
              Paste one proxy per line in the format: host:port:username:password
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-4">
            <div className="grid gap-2">
              <Label htmlFor="proxy-type">Proxy Type</Label>
              <Select value={uploadType} onValueChange={(v) => setUploadType(v as ProxyType)}>
                <SelectTrigger id="proxy-type">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="PROXY_TYPE_SOCKS5">SOCKS5</SelectItem>
                  <SelectItem value="PROXY_TYPE_HTTP">HTTP</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="proxy-list">Proxy List</Label>
              <Textarea
                id="proxy-list"
                placeholder={"1.2.3.4:1080:user:pass\n5.6.7.8:1080:user:pass"}
                rows={8}
                value={uploadText}
                onChange={(e) => setUploadText(e.target.value)}
                className="font-mono text-sm"
              />
            </div>
            {uploadErrors.length > 0 && (
              <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive">
                {uploadErrors.map((err, i) => (
                  <p key={i}>{err}</p>
                ))}
              </div>
            )}
            {uploadResult && (
              <div className="rounded-md bg-green-50 p-3 text-sm text-green-700 dark:bg-green-950/30 dark:text-green-400">
                <p>{uploadResult.added} proxies added, {uploadResult.skipped} skipped (duplicates).</p>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setUploadOpen(false); resetUploadForm() }}>
              {uploadResult ? 'Close' : 'Cancel'}
            </Button>
            {!uploadResult && (
              <Button
                onClick={handleBulkUpload}
                disabled={!uploadText.trim() || addMutation.isPending}
              >
                {addMutation.isPending ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    Uploading...
                  </>
                ) : (
                  'Upload'
                )}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Health Check Result Dialog */}
      <Dialog open={healthResult.open} onOpenChange={(v) => { if (!v) setHealthResult({ open: false, data: null, loading: false, error: null }) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Health Check Result</DialogTitle>
          </DialogHeader>
          <div className="py-4">
            {healthResult.loading && (
              <div className="flex items-center justify-center py-8">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
                <span className="ml-2 text-muted-foreground">Checking proxy...</span>
              </div>
            )}
            {healthResult.error && (
              <div className="rounded-md bg-destructive/10 p-4 text-sm text-destructive">
                Health check failed: {healthResult.error}
              </div>
            )}
            {healthResult.data && (
              <div className="space-y-3">
                <div className="flex items-center justify-between rounded-md border p-3">
                  <span className="text-sm text-muted-foreground">Reachable</span>
                  <span className={`font-semibold ${healthResult.data.reachable ? 'text-green-600' : 'text-red-600'}`}>
                    {healthResult.data.reachable ? 'Yes' : 'No'}
                  </span>
                </div>
                <div className="flex items-center justify-between rounded-md border p-3">
                  <span className="text-sm text-muted-foreground">Latency</span>
                  <span className={`font-mono font-semibold ${
                    healthResult.data.latencyMs < 200
                      ? 'text-green-600'
                      : healthResult.data.latencyMs < 500
                        ? 'text-yellow-600'
                        : 'text-red-600'
                  }`}>
                    {healthResult.data.latencyMs}ms
                  </span>
                </div>
                <div className="flex items-center justify-between rounded-md border p-3">
                  <span className="text-sm text-muted-foreground">Status</span>
                  <StatusBadge
                    label={PROXY_STATUS[healthResult.data.proxy.status].label}
                    variant={PROXY_STATUS[healthResult.data.proxy.status].variant}
                    dot={PROXY_STATUS[healthResult.data.proxy.status].dot}
                  />
                </div>
                <div className="flex items-center justify-between rounded-md border p-3">
                  <span className="text-sm text-muted-foreground">Checked At</span>
                  <span className="text-sm">{formatDate(healthResult.data.checkedAt)}</span>
                </div>
              </div>
            )}
          </div>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <ConfirmDialog
        open={confirmDelete.open}
        onClose={() => setConfirmDelete({ open: false, proxy: null })}
        onConfirm={() => {
          if (confirmDelete.proxy) {
            deleteMutation.mutate(confirmDelete.proxy.id)
          }
        }}
        title="Delete Proxy"
        description={
          confirmDelete.proxy
            ? `Are you sure you want to delete proxy ${confirmDelete.proxy.host}:${confirmDelete.proxy.port}? Any numbers assigned to this proxy will need to be reassigned.`
            : ''
        }
        confirmLabel="Delete"
        destructive
        loading={deleteMutation.isPending}
      />
    </div>
  )
}

// --- Row Actions Dropdown ---

interface ProxyActionsProps {
  onHealthCheck: () => void
  onDelete: () => void
}

function ProxyActions({ onHealthCheck, onDelete }: ProxyActionsProps) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
          <MoreHorizontal className="h-4 w-4" />
          <span className="sr-only">Open menu</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onClick={onHealthCheck}>
          <Activity className="mr-2 h-4 w-4" />
          Health Check
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={onDelete} className="text-destructive focus:text-destructive">
          <Trash2 className="mr-2 h-4 w-4" />
          Delete
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
