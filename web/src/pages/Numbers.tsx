import { useState, useEffect, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '@/stores/auth'
import { listWaNumbers, registerWaNumber, getQRCode, disconnectWaNumber, reconnectWaNumber, deleteWaNumber } from '@/api/numbers'
import { listWorkspaces } from '@/api/workspaces'
import type { WaNumber, WaNumberStatus } from '@/api/types'
import { WA_STATUS } from '@/lib/constants'
import { formatPhone, formatNumber } from '@/lib/utils'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { Pagination } from '@/components/shared/Pagination'
import { ConfirmDialog } from '@/components/shared/ConfirmDialog'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { MoreHorizontal, Plus, QrCode, Wifi, WifiOff, Trash2, Loader2 } from 'lucide-react'

const PAGE_SIZE = 20

const STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: 'ALL', label: 'All Statuses' },
  { value: 'WA_NUMBER_STATUS_ACTIVE', label: 'Active' },
  { value: 'WA_NUMBER_STATUS_BANNED', label: 'Banned' },
  { value: 'WA_NUMBER_STATUS_DISCONNECTED', label: 'Disconnected' },
  { value: 'WA_NUMBER_STATUS_COOLDOWN', label: 'Cooldown' },
]

function healthScoreColor(score: number): string {
  if (score >= 80) return 'text-green-600'
  if (score >= 50) return 'text-yellow-600'
  return 'text-red-600'
}

export default function Numbers() {
  const tenant = useAuthStore((s) => s.tenant)
  const tenantId = tenant?.id ?? ''
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [statusFilter, setStatusFilter] = useState<string>('ALL')
  const [registerOpen, setRegisterOpen] = useState(false)
  const [qrModal, setQrModal] = useState<{ open: boolean; waNumberId: string; qrCode: string }>({
    open: false,
    waNumberId: '',
    qrCode: '',
  })
  const [confirmDelete, setConfirmDelete] = useState<{ open: boolean; number: WaNumber | null }>({
    open: false,
    number: null,
  })

  // --- Register form state ---
  const [regPhone, setRegPhone] = useState('')
  const [regDisplayName, setRegDisplayName] = useState('')
  const [regWorkspaceIds, setRegWorkspaceIds] = useState<string[]>([])

  // --- Workspace list for multi-select ---
  const { data: workspacesData } = useQuery({
    queryKey: ['workspaces', tenantId],
    queryFn: () => listWorkspaces({ tenantId, pageSize: 100 }),
    enabled: !!tenantId,
  })
  const workspaces = workspacesData?.workspaces ?? []

  // Auto-select if only one workspace exists.
  useEffect(() => {
    if (workspaces.length === 1 && regWorkspaceIds.length === 0) {
      setRegWorkspaceIds([workspaces[0].id])
    }
  }, [workspaces]) // eslint-disable-line react-hooks/exhaustive-deps

  const statusParam = statusFilter === 'ALL' ? undefined : (statusFilter as WaNumberStatus)

  // --- Queries ---
  const { data, isLoading } = useQuery({
    queryKey: ['wa-numbers', tenantId, page, statusFilter],
    queryFn: () => listWaNumbers({ tenantId, page, pageSize: PAGE_SIZE, status: statusParam }),
    enabled: !!tenantId,
  })

  // --- QR code polling ---
  const { data: qrData } = useQuery({
    queryKey: ['qr-code', qrModal.waNumberId],
    queryFn: () => getQRCode(qrModal.waNumberId),
    enabled: qrModal.open && !!qrModal.waNumberId,
    refetchInterval: 3000,
  })

  useEffect(() => {
    if (qrData?.isLinked) {
      setQrModal({ open: false, waNumberId: '', qrCode: '' })
      queryClient.invalidateQueries({ queryKey: ['wa-numbers'] })
    } else if (qrData?.qrCode) {
      setQrModal((prev) => ({ ...prev, qrCode: qrData.qrCode }))
    }
  }, [qrData, queryClient])

  // --- Mutations ---
  const registerMutation = useMutation({
    mutationFn: registerWaNumber,
    onSuccess: (res) => {
      setRegisterOpen(false)
      resetRegisterForm()
      queryClient.invalidateQueries({ queryKey: ['wa-numbers'] })
      if (res.qrCode) {
        setQrModal({ open: true, waNumberId: res.waNumber.id, qrCode: res.qrCode })
      }
    },
  })

  const disconnectMutation = useMutation({
    mutationFn: disconnectWaNumber,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['wa-numbers'] }),
  })

  const reconnectMutation = useMutation({
    mutationFn: reconnectWaNumber,
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ['wa-numbers'] })
      if (res.qrCode) {
        setQrModal({ open: true, waNumberId: res.waNumber.id, qrCode: res.qrCode })
      }
    },
  })

  const deleteMutation = useMutation({
    mutationFn: deleteWaNumber,
    onSuccess: () => {
      setConfirmDelete({ open: false, number: null })
      queryClient.invalidateQueries({ queryKey: ['wa-numbers'] })
    },
  })

  const resetRegisterForm = useCallback(() => {
    setRegPhone('')
    setRegDisplayName('')
    setRegWorkspaceIds([])
  }, [])

  function handleRegister() {
    registerMutation.mutate({
      tenantId,
      phone: regPhone,
      displayName: regDisplayName,
      workspaceIds: regWorkspaceIds,
    })
  }

  function toggleWorkspace(wsId: string) {
    setRegWorkspaceIds((prev) =>
      prev.includes(wsId) ? prev.filter((id) => id !== wsId) : [...prev, wsId]
    )
  }

  function handleStatusFilterChange(value: string) {
    setStatusFilter(value)
    setPage(1)
  }

  const numbers = data?.waNumbers ?? []
  const pagination = data?.pagination ?? { total: 0, page: 1, pageSize: PAGE_SIZE, totalPages: 1 }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">WhatsApp Numbers</h1>
          <p className="text-muted-foreground">Manage your WhatsApp sessions and connections.</p>
        </div>
        <Button onClick={() => setRegisterOpen(true)}>
          <Plus className="mr-2 h-4 w-4" />
          Register Number
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
      ) : numbers.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
          <p>No numbers found.</p>
        </div>
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Phone</TableHead>
                <TableHead>Display Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Health Score</TableHead>
                <TableHead>Daily Sent</TableHead>
                <TableHead>Proxy</TableHead>
                <TableHead>Pod</TableHead>
                <TableHead className="w-[50px]" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {numbers.map((num) => {
                const statusCfg = WA_STATUS[num.status]
                return (
                  <TableRow key={num.id}>
                    <TableCell className="font-mono">{formatPhone(num.phone)}</TableCell>
                    <TableCell>{num.displayName || '-'}</TableCell>
                    <TableCell>
                      <StatusBadge
                        label={statusCfg.label}
                        variant={statusCfg.variant}
                        dot={statusCfg.dot}
                        pulse={num.status === 'WA_NUMBER_STATUS_ACTIVE'}
                      />
                    </TableCell>
                    <TableCell>
                      <span className={`font-semibold ${healthScoreColor(num.healthScore)}`}>
                        {num.healthScore}%
                      </span>
                    </TableCell>
                    <TableCell>{formatNumber(num.dailySentCount)}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {num.proxyId ? num.proxyId.slice(0, 8) : '-'}
                    </TableCell>
                    <TableCell className="text-xs">{num.podId || '-'}</TableCell>
                    <TableCell>
                      <NumberActions
                        number={num}
                        onDisconnect={() => disconnectMutation.mutate(num.id)}
                        onReconnect={() => reconnectMutation.mutate(num.id)}
                        onDelete={() => setConfirmDelete({ open: true, number: num })}
                        onShowQr={() => {
                          setQrModal({ open: true, waNumberId: num.id, qrCode: '' })
                        }}
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

      {/* Register Dialog */}
      <Dialog open={registerOpen} onOpenChange={(v) => { if (!v) { setRegisterOpen(false); resetRegisterForm() } }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Register New Number</DialogTitle>
            <DialogDescription>Add a new WhatsApp number to your tenant.</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 py-4">
            <div className="grid gap-2">
              <Label htmlFor="reg-phone">Phone Number</Label>
              <Input
                id="reg-phone"
                placeholder="628123456789"
                value={regPhone}
                onChange={(e) => setRegPhone(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="reg-display">Display Name</Label>
              <Input
                id="reg-display"
                placeholder="Sales Bot 1"
                value={regDisplayName}
                onChange={(e) => setRegDisplayName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>Workspaces</Label>
              {workspaces.length === 0 ? (
                <p className="text-sm text-muted-foreground">No workspaces found. Create a workspace first.</p>
              ) : (
                <div className="space-y-2 rounded-md border p-3 max-h-40 overflow-y-auto">
                  {workspaces.map((ws) => (
                    <label key={ws.id} className="flex items-center gap-2 cursor-pointer text-sm">
                      <input
                        type="checkbox"
                        checked={regWorkspaceIds.includes(ws.id)}
                        onChange={() => toggleWorkspace(ws.id)}
                        className="rounded border-gray-300"
                      />
                      <span>{ws.name}</span>
                      <span className="text-xs text-muted-foreground">({ws.id.slice(0, 8)}...)</span>
                    </label>
                  ))}
                </div>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setRegisterOpen(false); resetRegisterForm() }}>
              Cancel
            </Button>
            <Button
              onClick={handleRegister}
              disabled={!regPhone || registerMutation.isPending}
            >
              {registerMutation.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Registering...
                </>
              ) : (
                'Register'
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* QR Code Modal */}
      <Dialog open={qrModal.open} onOpenChange={(v) => { if (!v) setQrModal({ open: false, waNumberId: '', qrCode: '' }) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Scan QR Code</DialogTitle>
            <DialogDescription>
              Open WhatsApp on your phone and scan this QR code to link the number.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col items-center gap-4 py-4">
            {qrModal.qrCode ? (
              <img
                src={`data:image/png;base64,${qrModal.qrCode}`}
                alt="WhatsApp QR Code"
                className="h-64 w-64 rounded-lg border"
              />
            ) : (
              <div className="flex h-64 w-64 items-center justify-center rounded-lg border">
                <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
              </div>
            )}
            <p className="text-sm text-muted-foreground">
              Waiting for scan... Polling every 3 seconds.
            </p>
          </div>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <ConfirmDialog
        open={confirmDelete.open}
        onClose={() => setConfirmDelete({ open: false, number: null })}
        onConfirm={() => {
          if (confirmDelete.number) {
            deleteMutation.mutate(confirmDelete.number.id)
          }
        }}
        title="Delete Number"
        description={
          confirmDelete.number
            ? `Are you sure you want to delete ${formatPhone(confirmDelete.number.phone)}? This will disconnect the session and remove all associated data.`
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

interface NumberActionsProps {
  number: WaNumber
  onDisconnect: () => void
  onReconnect: () => void
  onDelete: () => void
  onShowQr: () => void
}

function NumberActions({ number, onDisconnect, onReconnect, onDelete, onShowQr }: NumberActionsProps) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" className="h-8 w-8 p-0">
          <MoreHorizontal className="h-4 w-4" />
          <span className="sr-only">Open menu</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {number.status === 'WA_NUMBER_STATUS_ACTIVE' && (
          <DropdownMenuItem onClick={onDisconnect}>
            <WifiOff className="mr-2 h-4 w-4" />
            Disconnect
          </DropdownMenuItem>
        )}
        {(number.status === 'WA_NUMBER_STATUS_DISCONNECTED' || number.status === 'WA_NUMBER_STATUS_BANNED') && (
          <DropdownMenuItem onClick={onReconnect}>
            <Wifi className="mr-2 h-4 w-4" />
            Reconnect
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onClick={onShowQr}>
          <QrCode className="mr-2 h-4 w-4" />
          Show QR Code
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
