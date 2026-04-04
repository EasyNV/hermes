import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth'
import { listTemplates } from '@/api/templates'
import { listContacts } from '@/api/contacts'
import { listWaNumbers } from '@/api/numbers'
import { createCampaign, startCampaign } from '@/api/campaigns'
import type { Template, Contact, WaNumber, RotationStrategy } from '@/api/types'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Pagination } from '@/components/shared/Pagination'
import { StatusBadge } from '@/components/shared/StatusBadge'
import { WA_STATUS } from '@/lib/constants'
import { cn, formatPhone, formatNumber, truncate } from '@/lib/utils'

const STEPS = ['Select Template', 'Select Contacts', 'Select Numbers', 'Configure', 'Review & Launch'] as const
const PAGE_SIZE = 10

interface FormState {
  templateId: string
  contactIds: string[]
  waNumberIds: string[]
  name: string
  dailyCapPerNum: number
  banPauseThreshold: number
  rotationStrategy: RotationStrategy
  delayMinMs: number
  delayMaxMs: number
}

const initialForm: FormState = {
  templateId: '',
  contactIds: [],
  waNumberIds: [],
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

  const { data, isLoading } = useQuery({
    queryKey: ['contacts', tenantId, page, search],
    queryFn: () => listContacts({ tenantId, search: search || undefined, page, pageSize: PAGE_SIZE }),
  })

  const contacts = data?.contacts ?? []
  const selectedSet = new Set(selectedIds)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-4">
        <Input
          placeholder="Search contacts by name or phone..."
          value={search}
          onChange={(e) => {
            setSearch(e.target.value)
            setPage(1)
          }}
          className="max-w-sm"
        />
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
  numberCount,
}: {
  form: FormState
  templates: Template[]
  contactCount: number
  numberCount: number
}) {
  const template = templates.find((t) => t.id === form.templateId)
  const strategyLabel =
    form.rotationStrategy === 'ROTATION_STRATEGY_ROUND_ROBIN' ? 'Round Robin' : 'Least Used'

  return (
    <div className="space-y-6 max-w-lg">
      <div>
        <h3 className="text-lg font-semibold">{form.name || 'Unnamed Campaign'}</h3>
        <p className="text-sm text-muted-foreground mt-1">Review the details below before launching.</p>
      </div>

      <Separator />

      <div className="space-y-4">
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
          <span className="text-sm text-muted-foreground">Numbers</span>
          <span className="text-sm font-medium">{formatNumber(numberCount)} numbers</span>
        </div>

        <Separator />

        <div className="flex justify-between">
          <span className="text-sm text-muted-foreground">Daily Cap / Number</span>
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

  const canProceed = (): boolean => {
    switch (step) {
      case 0:
        return form.templateId !== ''
      case 1:
        return form.contactIds.length > 0
      case 2:
        return form.waNumberIds.length > 0
      case 3:
        return form.name.trim() !== '' && form.delayMinMs < form.delayMaxMs
      case 4:
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
        waNumberIds: form.waNumberIds,
        contactIds: form.contactIds,
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
            <StepSelectTemplate
              workspaceId={workspaceId}
              selectedId={form.templateId}
              onSelect={(id) => updateForm({ templateId: id })}
            />
          )}

          {step === 1 && (
            <StepSelectContacts
              tenantId={tenantId}
              selectedIds={form.contactIds}
              onToggle={toggleContact}
            />
          )}

          {step === 2 && (
            <StepSelectNumbers
              tenantId={tenantId}
              workspaceId={workspaceId}
              selectedIds={form.waNumberIds}
              onToggle={toggleNumber}
            />
          )}

          {step === 3 && <StepConfigure form={form} onChange={updateForm} />}

          {step === 4 && (
            <StepReview
              form={form}
              templates={templatesQuery.data?.templates ?? []}
              contactCount={form.contactIds.length}
              numberCount={form.waNumberIds.length}
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
