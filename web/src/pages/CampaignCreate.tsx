import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth'
import { listTemplates } from '@/api/templates'
import { listContacts } from '@/api/contacts'
import { listWaNumbers } from '@/api/numbers'
import { listMbsSessions } from '@/api/mbs'
import { createCampaign, startCampaign } from '@/api/campaigns'
import type { Template, Contact, WaNumber, MbsSession, RotationStrategy } from '@/api/types'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Pagination } from '@/components/shared/Pagination'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { TagInput } from '@/components/shared/TagInput'
import { WA_STATUS } from '@/lib/constants'
import { cn, formatPhone, formatNumber, truncate } from '@/lib/utils'

type Channel = 'wa' | 'mbs'

// Chunk 10 — channel-first wizard. New step 0 picks the channel, then
// step 3 conditionally renders WA-numbers or MBS-sessions. Existing
// 5-step WA flow is preserved; MBS flow follows the same shape.
const STEPS = ['Select Channel', 'Select Template', 'Select Contacts', 'Select Senders', 'Configure', 'Review & Launch'] as const
const PAGE_SIZE = 10

interface FormState {
  channel: Channel
  templateId: string
  contactIds: string[]
  waNumberIds: string[]
  mbsSessionUids: string[]
  name: string
  dailyCapPerNum: number
  banPauseThreshold: number
  rotationStrategy: RotationStrategy
  delayMinMs: number
  delayMaxMs: number
}

const initialForm: FormState = {
  channel: 'wa',
  templateId: '',
  contactIds: [],
  waNumberIds: [],
  mbsSessionUids: [],
  name: '',
  dailyCapPerNum: 200,
  banPauseThreshold: 3,
  rotationStrategy: 'ROTATION_STRATEGY_ROUND_ROBIN',
  delayMinMs: 3000,
  delayMaxMs: 8000,
}

function StepIndicator({ current }: { current: number }) {
  return (
    <nav className="flex items-center justify-center gap-2 mb-8">
      {STEPS.map((label, i) => {
        const isActive = i === current
        const isCompleted = i < current
        return (
          <div key={label} className="flex items-center gap-2">
            {i > 0 && (
              <div className={cn('h-px w-8', isCompleted ? 'bg-primary' : 'bg-border')} />
            )}
            <div className="flex items-center gap-2">
              <div
                className={cn(
                  'flex h-8 w-8 items-center justify-center rounded-full border-2 text-sm font-medium',
                  isActive && 'border-primary bg-primary text-primary-foreground',
                  isCompleted && 'border-primary bg-primary text-primary-foreground',
                  !isActive && !isCompleted && 'border-muted-foreground/30 text-muted-foreground',
                )}
              >
                {isCompleted ? '\u2713' : i + 1}
              </div>
              <span
                className={cn(
                  'hidden text-sm sm:inline',
                  isActive ? 'font-semibold text-foreground' : 'text-muted-foreground',
                )}
              >
                {label}
              </span>
            </div>
          </div>
        )
      })}
    </nav>
  )
}

function StepSelectTemplate({
  workspaceId,
  selectedId,
  onSelect,
}: {
  workspaceId: string
  selectedId: string
  onSelect: (id: string) => void
}) {
  const [page, setPage] = useState(1)

  const { data, isLoading } = useQuery({
    queryKey: ['templates', workspaceId, page],
    queryFn: () => listTemplates({ workspaceId, page, pageSize: PAGE_SIZE }),
  })

  if (isLoading) {
    return <p className="text-center text-muted-foreground py-8">Loading templates...</p>
  }

  const templates = data?.templates ?? []

  if (templates.length === 0) {
    return <p className="text-center text-muted-foreground py-8">No templates found. Create a template first.</p>
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">Choose a message template for this campaign.</p>
      <div className="grid gap-3">
        {templates.map((t: Template) => (
          <Card
            key={t.id}
            className={cn(
              'cursor-pointer transition-colors hover:border-primary/50',
              selectedId === t.id && 'border-primary ring-2 ring-primary/20',
            )}
            onClick={() => onSelect(t.id)}
          >
            <CardContent className="p-4">
              <div className="flex items-start justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <div
                      className={cn(
                        'h-4 w-4 rounded-full border-2 flex items-center justify-center',
                        selectedId === t.id ? 'border-primary' : 'border-muted-foreground/30',
                      )}
                    >
                      {selectedId === t.id && <div className="h-2 w-2 rounded-full bg-primary" />}
                    </div>
                    <h4 className="font-medium">{t.name}</h4>
                  </div>
                  <p className="mt-1 text-sm text-muted-foreground pl-6">{truncate(t.body, 120)}</p>
                  {t.variables.length > 0 && (
                    <div className="mt-2 flex gap-1 pl-6 flex-wrap">
                      {t.variables.map((v) => (
                        <span key={v} className="rounded bg-muted px-1.5 py-0.5 text-xs font-mono">
                          {`{{${v}}}`}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
                {t.mediaType && (
                  <span className="text-xs text-muted-foreground rounded bg-muted px-2 py-0.5">
                    {t.mediaType}
                  </span>
                )}
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
      {data?.pagination && <Pagination pagination={data.pagination} onPageChange={setPage} />}
    </div>
  )
}

function StepSelectChannel({
  value,
  onChange,
}: {
  value: Channel
  onChange: (next: Channel) => void
}) {
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Choose the dispatch channel. WhatsApp uses your connected WA numbers; Meta Business Suite
        sends through registered Page WhatsApp Business accounts (WEC).
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        {(
          [
            {
              key: 'wa' as Channel,
              title: 'WhatsApp',
              desc: 'Send via your linked WA numbers (whatsmeow). Best for high-volume outbound.',
            },
            {
              key: 'mbs' as Channel,
              title: 'Meta Business Suite',
              desc: 'Send via registered Page WEC accounts (Stage F). Lower throughput, higher trust.',
            },
          ] as const
        ).map((opt) => (
          <Card
            key={opt.key}
            className={cn(
              'cursor-pointer transition-colors hover:border-primary/50',
              value === opt.key && 'border-primary ring-2 ring-primary/20',
            )}
            onClick={() => onChange(opt.key)}
            data-testid={`channel-${opt.key}`}
          >
            <CardContent className="p-5">
              <div className="flex items-start gap-3">
                <div
                  className={cn(
                    'mt-0.5 h-4 w-4 rounded-full border-2 flex items-center justify-center',
                    value === opt.key ? 'border-primary' : 'border-muted-foreground/30',
                  )}
                >
                  {value === opt.key && <div className="h-2 w-2 rounded-full bg-primary" />}
                </div>
                <div className="flex-1">
                  <h4 className="font-medium">{opt.title}</h4>
                  <p className="mt-1 text-sm text-muted-foreground">{opt.desc}</p>
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}

function StepSelectContacts({
  tenantId,
  selectedIds,
  onToggle,
}: {
  tenantId: string
  selectedIds: string[]
  onToggle: (id: string) => void
}) {
  const [page, setPage] = useState(1)
  const [search, setSearch] = useState('')
  // Chunk 7: tag filter for the picker. Filter applies to the LIST view only;
  // selection persists across filter changes because `selectedIds` is the
  // wizard's `form.contactIds` (lifted state). Backend filters with AND
  // cardinality — a contact must carry every selected tag to appear.
  const [filterTags, setFilterTags] = useState<string[]>([])

  const { data, isLoading } = useQuery({
    queryKey: ['contacts', tenantId, page, search, filterTags],
    queryFn: () => listContacts({
      tenantId,
      search: search || undefined,
      tags: filterTags.length > 0 ? filterTags : undefined,
      page,
      pageSize: PAGE_SIZE,
    }),
  })

  const contacts = data?.contacts ?? []
  const selectedSet = new Set(selectedIds)

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex flex-1 flex-col gap-2 sm:flex-row sm:items-center">
          <Input
            placeholder="Search contacts by name or phone..."
            value={search}
            onChange={(e) => {
              setSearch(e.target.value)
              setPage(1)
            }}
            className="sm:max-w-sm"
          />
          <div className="flex-1 sm:max-w-md">
            <TagInput
              value={filterTags}
              onChange={(next) => {
                setFilterTags(next)
                setPage(1)
              }}
              placeholder="Filter by tags (Enter to add)"
            />
          </div>
        </div>
        <span className="text-sm font-medium text-muted-foreground whitespace-nowrap">
          {formatNumber(selectedIds.length)} selected
        </span>
      </div>

      {isLoading ? (
        <p className="text-center text-muted-foreground py-8">Loading contacts...</p>
      ) : contacts.length === 0 ? (
        <p className="text-center text-muted-foreground py-8">No contacts found.</p>
      ) : (
        <div className="rounded-md border">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b bg-muted/50">
                <th className="w-12 p-3" />
                <th className="p-3 text-left font-medium text-muted-foreground">Name</th>
                <th className="p-3 text-left font-medium text-muted-foreground">Phone</th>
                <th className="p-3 text-left font-medium text-muted-foreground">Tags</th>
              </tr>
            </thead>
            <tbody>
              {contacts.map((c: Contact) => (
                <tr
                  key={c.id}
                  className={cn(
                    'border-b cursor-pointer hover:bg-muted/50 transition-colors',
                    selectedSet.has(c.id) && 'bg-primary/5',
                  )}
                  onClick={() => onToggle(c.id)}
                >
                  <td className="p-3 text-center">
                    <input
                      type="checkbox"
                      checked={selectedSet.has(c.id)}
                      onChange={() => onToggle(c.id)}
                      className="h-4 w-4 rounded border-gray-300"
                    />
                  </td>
                  <td className="p-3 font-medium">{c.name || '-'}</td>
                  <td className="p-3 text-muted-foreground">{formatPhone(c.phone)}</td>
                  <td className="p-3">
                    <div className="flex gap-1 flex-wrap">
                      {c.tags.slice(0, 3).map((tag) => (
                        <span key={tag} className="rounded bg-muted px-1.5 py-0.5 text-xs">{tag}</span>
                      ))}
                      {c.tags.length > 3 && (
                        <span className="text-xs text-muted-foreground">+{c.tags.length - 3}</span>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {data?.pagination && <Pagination pagination={data.pagination} onPageChange={setPage} />}
    </div>
  )
}

function StepSelectNumbers({
  tenantId,
  workspaceId,
  selectedIds,
  onToggle,
}: {
  tenantId: string
  workspaceId: string
  selectedIds: string[]
  onToggle: (id: string) => void
}) {
  const [page, setPage] = useState(1)

  const { data, isLoading } = useQuery({
    queryKey: ['waNumbers', tenantId, workspaceId, page],
    queryFn: () => listWaNumbers({ tenantId, workspaceId, page, pageSize: PAGE_SIZE }),
  })

  const numbers = data?.waNumbers ?? []
  const selectedSet = new Set(selectedIds)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">Select WhatsApp numbers to send from.</p>
        <span className="text-sm font-medium text-muted-foreground">
          {formatNumber(selectedIds.length)} selected
        </span>
      </div>

      {isLoading ? (
        <p className="text-center text-muted-foreground py-8">Loading numbers...</p>
      ) : numbers.length === 0 ? (
        <p className="text-center text-muted-foreground py-8">No WhatsApp numbers available.</p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {numbers.map((n: WaNumber) => {
            const statusCfg = WA_STATUS[n.status]
            return (
              <Card
                key={n.id}
                className={cn(
                  'cursor-pointer transition-colors hover:border-primary/50',
                  selectedSet.has(n.id) && 'border-primary ring-2 ring-primary/20',
                )}
                onClick={() => onToggle(n.id)}
              >
                <CardContent className="p-4">
                  <div className="flex items-start gap-3">
                    <input
                      type="checkbox"
                      checked={selectedSet.has(n.id)}
                      onChange={() => onToggle(n.id)}
                      className="mt-1 h-4 w-4 rounded border-gray-300"
                    />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center justify-between gap-2">
                        <h4 className="font-medium truncate">{n.displayName || n.phone}</h4>
                        <StatusBadge
                          label={statusCfg.label}
                          variant={statusCfg.variant}
                          dot={statusCfg.dot}
                        />
                      </div>
                      <p className="text-sm text-muted-foreground mt-0.5">{formatPhone(n.phone)}</p>
                      <div className="flex gap-4 mt-2 text-xs text-muted-foreground">
                        <span>Today: {formatNumber(n.dailySentCount)}</span>
                        <span>Total: {formatNumber(n.totalSent)}</span>
                        <span>Health: {n.healthScore}%</span>
                      </div>
                    </div>
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      {data?.pagination && <Pagination pagination={data.pagination} onPageChange={setPage} />}
    </div>
  )
}

// StepSelectMbsSessions — chunk 10. Renders only when form.channel === 'mbs'.
// Pickability gate (corrected 2026-06-02): a session is dispatchable when the
// send path's actual requirements are met. The MBS send path
// (internal/mbs/session/lightspeed.go) needs ONLY waba_id + page_id +
// wec_mailbox_id on the primary asset — it does NOT use wec_phone_number.
// The old gate blocked on wecPhoneNumber/wecAccountRegistered, which wrongly
// disabled healthy accounts (e.g. one that had already sent live) whose WEC
// phone field happened to be empty. wec_phone_number is now display-only.
// Filter rules:
//   - Non-ACTIVE sessions are excluded (state filter + belt-and-suspenders
//     client guard below) — you can't dispatch from a burned/suspended one.
//   - ACTIVE + primary asset has waba_id & page_id & wec_mailbox_id → PICKABLE.
//   - Otherwise SHOWN DISABLED with the specific missing-field reason.
function StepSelectMbsSessions({
  tenantId,
  selectedUids,
  onToggle,
}: {
  tenantId: string
  selectedUids: string[]
  onToggle: (uid: string) => void
}) {
  const [page, setPage] = useState(1)

  const { data, isLoading } = useQuery({
    queryKey: ['mbsSessions', tenantId, page],
    queryFn: () => listMbsSessions({
      tenantId,
      state: 'MBS_SESSION_STATE_ACTIVE',
      page,
      pageSize: PAGE_SIZE,
    }),
    enabled: !!tenantId,
  })

  const sessions = data?.sessions ?? []
  const selectedSet = new Set(selectedUids)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          Select Meta Business Suite sessions to send from. Only active sessions
          with a linked WEC mailbox can be selected.
        </p>
        <span className="text-sm font-medium text-muted-foreground whitespace-nowrap">
          {formatNumber(selectedUids.length)} selected
        </span>
      </div>

      {isLoading ? (
        <p className="text-center text-muted-foreground py-8">Loading MBS sessions...</p>
      ) : sessions.length === 0 ? (
        <p className="text-center text-muted-foreground py-8">No active MBS sessions available.</p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          {sessions.map((s: MbsSession) => {
            const asset = s.primaryAsset
            const wecPhone = asset?.wecPhoneNumber ?? ''
            const wecMailboxId = asset?.wecMailboxId ?? ''
            const wabaId = asset?.wabaId ?? ''
            const pageId = asset?.pageId ?? ''
            const isActive = s.state === 'MBS_SESSION_STATE_ACTIVE'
            // Pickability gate mirrors the send path's real requirements
            // (lightspeed.go): waba_id + page_id + wec_mailbox_id, on an
            // ACTIVE session. wec_phone_number is NOT required to dispatch.
            const disabled = !isActive || !asset || !wecMailboxId || !wabaId || !pageId
            const reason = !isActive
              ? 'Session is not active — cannot dispatch'
              : !asset
                ? 'No primary asset linked yet'
                : !wabaId
                  ? 'No WABA linked to this session'
                  : !pageId
                    ? 'No page linked to this session'
                    : !wecMailboxId
                      ? 'No WEC mailbox on this session — cannot route sends'
                      : ''
            const checked = selectedSet.has(s.uid)
            return (
              <Card
                key={s.uid}
                className={cn(
                  'transition-colors',
                  disabled
                    ? 'opacity-60 cursor-not-allowed border-muted'
                    : 'cursor-pointer hover:border-primary/50',
                  !disabled && checked && 'border-primary ring-2 ring-primary/20',
                )}
                onClick={() => {
                  if (!disabled) onToggle(s.uid)
                }}
                title={disabled ? reason : ''}
                data-testid={`mbs-session-${s.uid}`}
                data-disabled={disabled ? 'true' : 'false'}
              >
                <CardContent className="p-4">
                  <div className="flex items-start gap-3">
                    <input
                      type="checkbox"
                      checked={checked}
                      disabled={disabled}
                      onChange={() => onToggle(s.uid)}
                      className="mt-1 h-4 w-4 rounded border-gray-300"
                    />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center justify-between gap-2">
                        <h4 className="font-medium truncate">
                          {asset?.pageName || `Session ${s.uid}`}
                        </h4>
                        {asset?.isPrimary && (
                          <StatusBadge label="PRIMARY" variant="default" />
                        )}
                      </div>
                      <p className="text-sm text-muted-foreground mt-0.5">
                        {wecPhone ? formatPhone(`+${wecPhone}`) : '— no WEC phone —'}
                      </p>
                      <div className="flex gap-3 mt-2 text-xs text-muted-foreground flex-wrap">
                        <span>UID: {s.uid}</span>
                        {s.loginEmail && <span>Email: {s.loginEmail}</span>}
                        {asset?.businessName && <span>Biz: {asset.businessName}</span>}
                        <span className={wecMailboxId ? 'text-green-600' : 'text-amber-600'}>
                          Mailbox: {wecMailboxId ? '✓' : '✗'}
                        </span>
                      </div>
                      {disabled && reason && (
                        <p className="mt-2 text-xs text-amber-600">{reason}</p>
                      )}
                    </div>
                  </div>
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      {data?.pagination && <Pagination pagination={data.pagination} onPageChange={setPage} />}
    </div>
  )
}

function StepConfigure({
  form,
  onChange,
}: {
  form: FormState
  onChange: (updates: Partial<FormState>) => void
}) {
  return (
    <div className="space-y-6 max-w-lg">
      <div className="space-y-2">
        <Label htmlFor="name">Campaign Name</Label>
        <Input
          id="name"
          placeholder="e.g., April Promo Blast"
          value={form.name}
          onChange={(e) => onChange({ name: e.target.value })}
        />
      </div>

      <Separator />

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="dailyCap">Daily Cap per Number</Label>
          <Input
            id="dailyCap"
            type="number"
            min={1}
            value={form.dailyCapPerNum}
            onChange={(e) => onChange({ dailyCapPerNum: parseInt(e.target.value, 10) || 0 })}
          />
          <p className="text-xs text-muted-foreground">Max messages per number per day</p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="banThreshold">Ban Pause Threshold</Label>
          <Input
            id="banThreshold"
            type="number"
            min={1}
            value={form.banPauseThreshold}
            onChange={(e) => onChange({ banPauseThreshold: parseInt(e.target.value, 10) || 0 })}
          />
          <p className="text-xs text-muted-foreground">Bans before auto-pausing campaign</p>
        </div>
      </div>

      <div className="space-y-2">
        <Label htmlFor="rotation">Rotation Strategy</Label>
        <Select
          value={form.rotationStrategy}
          onValueChange={(val) => onChange({ rotationStrategy: val as RotationStrategy })}
        >
          <SelectTrigger id="rotation">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="ROTATION_STRATEGY_ROUND_ROBIN">Round Robin</SelectItem>
            <SelectItem value="ROTATION_STRATEGY_LEAST_USED">Least Used</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground">How to distribute sends across numbers</p>
      </div>

      <Separator />

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="delayMin">Delay Min (ms)</Label>
          <Input
            id="delayMin"
            type="number"
            min={500}
            value={form.delayMinMs}
            onChange={(e) => onChange({ delayMinMs: parseInt(e.target.value, 10) || 0 })}
          />
        </div>

        <div className="space-y-2">
          <Label htmlFor="delayMax">Delay Max (ms)</Label>
          <Input
            id="delayMax"
            type="number"
            min={500}
            value={form.delayMaxMs}
            onChange={(e) => onChange({ delayMaxMs: parseInt(e.target.value, 10) || 0 })}
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        Random delay between sends (ms). Higher values reduce ban risk.
      </p>
    </div>
  )
}

function StepReview({
  form,
  templates,
  contactCount,
  senderCount,
}: {
  form: FormState
  templates: Template[]
  contactCount: number
  senderCount: number
}) {
  const template = templates.find((t) => t.id === form.templateId)
  const strategyLabel =
    form.rotationStrategy === 'ROTATION_STRATEGY_ROUND_ROBIN' ? 'Round Robin' : 'Least Used'
  const channelLabel = form.channel === 'wa' ? 'WhatsApp' : 'Meta Business Suite'
  const senderLabel = form.channel === 'wa' ? 'WhatsApp numbers' : 'MBS sessions'

  return (
    <div className="space-y-6 max-w-lg">
      <div>
        <h3 className="text-lg font-semibold">{form.name || 'Unnamed Campaign'}</h3>
        <p className="text-sm text-muted-foreground mt-1">Review the details below before launching.</p>
      </div>

      <Separator />

      <div className="space-y-4">
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Channel</span>
          <span className="text-sm font-medium">{channelLabel}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Template</span>
          <span className="text-sm font-medium">{template?.name ?? 'Unknown'}</span>
        </div>
        {template && (
          <div className="rounded-md bg-muted p-3 text-sm">{truncate(template.body, 200)}</div>
        )}
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Contacts</span>
          <span className="text-sm font-medium">{formatNumber(contactCount)} contacts</span>
        </div>
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Senders</span>
          <span className="text-sm font-medium">{formatNumber(senderCount)} {senderLabel}</span>
        </div>

        <Separator />

        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Daily Cap / Sender</span>
          <span className="text-sm font-medium">{formatNumber(form.dailyCapPerNum)}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Ban Pause Threshold</span>
          <span className="text-sm font-medium">{form.banPauseThreshold} bans</span>
        </div>
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Rotation Strategy</span>
          <span className="text-sm font-medium">{strategyLabel}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Send Delay</span>
          <span className="text-sm font-medium">
            {formatNumber(form.delayMinMs)} - {formatNumber(form.delayMaxMs)} ms
          </span>
        </div>
      </div>
    </div>
  )
}

export default function CampaignCreate() {
  const navigate = useNavigate()
  const workspace = useAuthStore((s) => s.workspace)
  const tenant = useAuthStore((s) => s.tenant)
  const [step, setStep] = useState(0)
  const [form, setForm] = useState<FormState>(initialForm)
  const [error, setError] = useState<string | null>(null)

  const workspaceId = workspace?.id ?? ''
  const tenantId = tenant?.id ?? ''

  const templatesQuery = useQuery({
    queryKey: ['templates', workspaceId, 'all'],
    queryFn: () => listTemplates({ workspaceId, pageSize: 100 }),
    enabled: !!workspaceId,
  })

  const updateForm = (updates: Partial<FormState>) => {
    setForm((prev) => ({ ...prev, ...updates }))
  }

  const toggleContact = (id: string) => {
    setForm((prev) => ({
      ...prev,
      contactIds: prev.contactIds.includes(id)
        ? prev.contactIds.filter((c) => c !== id)
        : [...prev.contactIds, id],
    }))
  }

  const toggleNumber = (id: string) => {
    setForm((prev) => ({
      ...prev,
      waNumberIds: prev.waNumberIds.includes(id)
        ? prev.waNumberIds.filter((n) => n !== id)
        : [...prev.waNumberIds, id],
    }))
  }

  const toggleMbsSession = (uid: string) => {
    setForm((prev) => ({
      ...prev,
      mbsSessionUids: prev.mbsSessionUids.includes(uid)
        ? prev.mbsSessionUids.filter((u) => u !== uid)
        : [...prev.mbsSessionUids, uid],
    }))
  }

  // Channel switch — C10-G1: prompt before clearing non-empty sender list,
  // then clear the off-channel selection so the wizard doesn't try to send
  // both lists to the server (which would 400 InvalidArgument).
  const handleChannelChange = (next: Channel) => {
    if (next === form.channel) return
    const otherSelectedCount = next === 'wa' ? form.mbsSessionUids.length : form.waNumberIds.length
    if (otherSelectedCount > 0) {
      const ok = window.confirm(
        `You have ${otherSelectedCount} ${form.channel === 'wa' ? 'WhatsApp number' : 'MBS session'}` +
        `${otherSelectedCount === 1 ? '' : 's'} selected. Switching channel will clear that selection. Continue?`
      )
      if (!ok) return
    }
    setForm((prev) => ({
      ...prev,
      channel: next,
      waNumberIds: next === 'wa' ? prev.waNumberIds : [],
      mbsSessionUids: next === 'mbs' ? prev.mbsSessionUids : [],
    }))
  }

  // canProceed — chunk 10: step indices shift +1 vs the old WA-only flow.
  // 0 = Channel, 1 = Template, 2 = Contacts, 3 = Senders (WA|MBS),
  // 4 = Configure, 5 = Review.
  const canProceed = (): boolean => {
    switch (step) {
      case 0:
        return form.channel === 'wa' || form.channel === 'mbs'
      case 1:
        return form.templateId !== ''
      case 2:
        return form.contactIds.length > 0
      case 3:
        return form.channel === 'wa'
          ? form.waNumberIds.length > 0
          : form.mbsSessionUids.length > 0
      case 4:
        return form.name.trim() !== '' && form.delayMinMs < form.delayMaxMs
      case 5:
        return true
      default:
        return false
    }
  }

  const launchMutation = useMutation({
    mutationFn: async () => {
      const createRes = await createCampaign({
        workspaceId,
        templateId: form.templateId,
        name: form.name,
        dailyCapPerNum: form.dailyCapPerNum,
        banPauseThreshold: form.banPauseThreshold,
        rotationStrategy: form.rotationStrategy,
        delayMinMs: form.delayMinMs,
        delayMaxMs: form.delayMaxMs,
        contactIds: form.contactIds,
        channel: form.channel,
        // C10-G4: backend rejects mixed-channel payloads. The wizard's
        // channel-switch handler already clears the off-channel list, but
        // we explicitly omit it here as defense in depth — undefined is
        // serialized away by client.ts's qs/JSON helpers.
        waNumberIds: form.channel === 'wa' ? form.waNumberIds : undefined,
        mbsSessionUids: form.channel === 'mbs' ? form.mbsSessionUids : undefined,
      })
      const campaignId = createRes.campaign.id
      await startCampaign(campaignId)
      return campaignId
    },
    onSuccess: (campaignId) => {
      navigate({ to: '/campaigns/$id', params: { id: campaignId } })
    },
    onError: (err: Error) => {
      setError(err.message || 'Failed to launch campaign')
    },
  })

  return (
    <div className="container max-w-4xl py-8">
      <div className="mb-6">
        <h1 className="text-3xl font-bold tracking-tight">Create Campaign</h1>
        <p className="text-muted-foreground mt-1">Set up and launch a new WhatsApp campaign.</p>
      </div>

      <StepIndicator current={step} />

      <Card>
        <CardHeader>
          <CardTitle className="text-xl">{STEPS[step]}</CardTitle>
          <CardDescription>
            Step {step + 1} of {STEPS.length}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {step === 0 && (
            <StepSelectChannel value={form.channel} onChange={handleChannelChange} />
          )}

          {step === 1 && (
            <StepSelectTemplate
              workspaceId={workspaceId}
              selectedId={form.templateId}
              onSelect={(id) => updateForm({ templateId: id })}
            />
          )}

          {step === 2 && (
            <StepSelectContacts
              tenantId={tenantId}
              selectedIds={form.contactIds}
              onToggle={toggleContact}
            />
          )}

          {step === 3 && form.channel === 'wa' && (
            <StepSelectNumbers
              tenantId={tenantId}
              workspaceId={workspaceId}
              selectedIds={form.waNumberIds}
              onToggle={toggleNumber}
            />
          )}

          {step === 3 && form.channel === 'mbs' && (
            <StepSelectMbsSessions
              tenantId={tenantId}
              selectedUids={form.mbsSessionUids}
              onToggle={toggleMbsSession}
            />
          )}

          {step === 4 && <StepConfigure form={form} onChange={updateForm} />}

          {step === 5 && (
            <StepReview
              form={form}
              templates={templatesQuery.data?.templates ?? []}
              contactCount={form.contactIds.length}
              senderCount={form.channel === 'wa' ? form.waNumberIds.length : form.mbsSessionUids.length}
            />
          )}

          {error && (
            <div className="mt-4 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
              {error}
            </div>
          )}

          <Separator className="my-6" />

          <div className="flex items-center justify-between">
            <Button
              variant="outline"
              onClick={() => {
                setError(null)
                setStep((s) => s - 1)
              }}
              disabled={step === 0}
            >
              Back
            </Button>

            {step < STEPS.length - 1 ? (
              <Button
                onClick={() => {
                  setError(null)
                  setStep((s) => s + 1)
                }}
                disabled={!canProceed()}
              >
                Next
              </Button>
            ) : (
              <Button
                onClick={() => launchMutation.mutate()}
                disabled={launchMutation.isPending}
              >
                {launchMutation.isPending ? 'Launching...' : 'Launch Campaign'}
              </Button>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
