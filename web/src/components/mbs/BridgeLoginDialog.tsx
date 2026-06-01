import { useEffect, useRef, useState } from 'react'
import { Loader2, CheckCircle2, AlertCircle, XCircle } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAuthStore } from '@/stores/auth'
import type {
  MbsBridgeClientFrame,
  MbsBridgeServerFrame,
} from '@/api/mbs'

// ─────────────────────────────────────────────────────────────────────
// BridgeLoginDialog (Stage E2 chunk 5)
//
// Bidirectional WS-driven Meta Business Suite login flow.
// Path: /ws/mbs/bridge-login (mounted OUTSIDE /api/v1; inline JWT
// validation server-side).
//
// State machine:
//   idle       -> form (email + password + optional 2FA secret)
//   connecting -> WS opened, awaiting first server frame
//   progress   -> server emitting progress frames; show stage text
//   prompt     -> server requested input (OTP/checkpoint/recovery)
//   success    -> auto-close, callback with session info
//   failure    -> show error, retry available (resets to idle)
//   error      -> transport/auth error, retry available
//
// Resource hygiene:
//   - WebSocket lifecycle owned entirely by this component.
//   - Cancel button OR dialog close cleans up the socket.
//   - Password is held in state and zeroed on success / unmount.
// ─────────────────────────────────────────────────────────────────────

type PromptField = { id: string; name: string; type: string }

type Phase =
  | { kind: 'idle' }
  | { kind: 'connecting' }
  | { kind: 'progress'; stage: string; detail: string }
  | {
      kind: 'prompt'
      stepId: string
      instructions: string
      fields: PromptField[]
      // values keyed by field id
      values: Record<string, string>
    }
  | { kind: 'success'; uid: string; displayName: string; pageCount: number }
  | { kind: 'failure'; code: string; message: string }
  | { kind: 'error'; code: string; message: string }

interface BridgeLoginDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: (s: { uid: string; displayName: string; pageCount: number }) => void
}

export function BridgeLoginDialog({ open, onOpenChange, onSuccess }: BridgeLoginDialogProps) {
  const tenantId = useAuthStore((s) => s.tenant?.id) ?? ''

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [totpSecret, setTotpSecret] = useState('')
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' })
  const wsRef = useRef<WebSocket | null>(null)
  // Guards the success side-effect so onSuccess + auto-close fire exactly
  // once per login, even if the effect re-runs (e.g. onSuccess identity
  // churn or a re-render while phase is still 'success'). Reset on close.
  const successFiredRef = useRef(false)

  // Cleanup helper — close socket, no state change.
  const closeSocket = () => {
    if (wsRef.current) {
      try {
        wsRef.current.close()
      } catch {
        /* socket already closed */
      }
      wsRef.current = null
    }
  }

  // On dialog close: tear everything down.
  useEffect(() => {
    if (!open) {
      closeSocket()
      successFiredRef.current = false
      // Don't reset email (user might retry with same one); DO zero
      // the secrets.
      setPassword('')
      setTotpSecret('')
      setPhase({ kind: 'idle' })
    }
  }, [open])

  // On component unmount: belt + suspenders.
  useEffect(() => {
    return () => {
      closeSocket()
      setPassword('')
      setTotpSecret('')
    }
  }, [])

  // On success: notify parent + auto-close. Guarded so it fires once
  // even if this effect re-runs while phase is still 'success' (the
  // onSuccess callback identity can churn on parent re-render).
  useEffect(() => {
    if (phase.kind === 'success' && !successFiredRef.current) {
      successFiredRef.current = true
      onSuccess({ uid: phase.uid, displayName: phase.displayName, pageCount: phase.pageCount })
      const t = setTimeout(() => onOpenChange(false), 1500)
      return () => clearTimeout(t)
    }
  }, [phase, onSuccess, onOpenChange])

  const sendFrame = (frame: MbsBridgeClientFrame) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify(frame))
  }

  const handleServerFrame = (frame: MbsBridgeServerFrame) => {
    switch (frame.type) {
      case 'bridge_login_prompt': {
        const fields = frame.payload.fields ?? []
        setPhase({
          kind: 'prompt',
          stepId: frame.payload.stepId,
          instructions: frame.payload.instructions,
          fields,
          values: Object.fromEntries(fields.map((f) => [f.id, ''])),
        })
        break
      }
      case 'bridge_login_progress':
        setPhase({ kind: 'progress', stage: frame.payload.stage, detail: frame.payload.detail })
        break
      case 'bridge_login_success':
        setPhase({
          kind: 'success',
          uid: frame.payload.uid,
          displayName: frame.payload.displayName ?? '',
          pageCount: frame.payload.pageCount ?? 0,
        })
        closeSocket()
        break
      case 'bridge_login_failure':
        setPhase({ kind: 'failure', code: frame.payload.code, message: frame.payload.message })
        closeSocket()
        break
      case 'error':
        setPhase({ kind: 'error', code: frame.payload.code, message: frame.payload.message })
        closeSocket()
        break
    }
  }

  const handleStart = () => {
    if (!email || !password) return

    successFiredRef.current = false

    const token = localStorage.getItem('access_token')
    if (!token) {
      setPhase({ kind: 'error', code: 'NO_TOKEN', message: 'Not authenticated' })
      return
    }

    setPhase({ kind: 'connecting' })

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    // Carrying gap C2-G1: token in URL is the same posture as the
    // main /ws fan-out. Server-side log scrubbing is Stage F work.
    const url = `${proto}//${location.host}/ws/mbs/bridge-login?token=${encodeURIComponent(token)}`

    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      sendFrame({
        type: 'start',
        payload: {
          tenantId,
          email,
          password,
          // Only send the secret if the operator provided one — an
          // empty string would make hermes-mbs try to derive a code
          // from a zero-length secret.
          ...(totpSecret.trim() ? { totpSecret: totpSecret.trim() } : {}),
        },
      })
    }

    ws.onmessage = (ev) => {
      try {
        const frame = JSON.parse(ev.data) as MbsBridgeServerFrame
        handleServerFrame(frame)
      } catch {
        setPhase({ kind: 'error', code: 'PARSE_ERROR', message: 'Malformed server frame' })
        closeSocket()
      }
    }

    ws.onerror = () => {
      // onclose fires after onerror; let it set the phase unless
      // we already have a terminal state.
    }

    ws.onclose = () => {
      // If the socket closed unexpectedly before a terminal frame,
      // surface it as a transport error.
      setPhase((prev) => {
        if (prev.kind === 'connecting' || prev.kind === 'progress' || prev.kind === 'prompt') {
          return { kind: 'error', code: 'WS_CLOSED', message: 'Connection lost' }
        }
        return prev
      })
    }
  }

  const handlePromptSubmit = () => {
    if (phase.kind !== 'prompt') return
    // Submit one input frame per field. The gateway forwards each as a
    // BridgeLoginInput{field_id, value}; hermes-mbs matches field_id
    // against the live bridge prompt.
    const filled = phase.fields.filter((f) => phase.values[f.id]?.length)
    if (filled.length === 0) return
    for (const f of filled) {
      sendFrame({ type: 'input', payload: { fieldId: f.id, value: phase.values[f.id] } })
    }
    setPhase({ kind: 'progress', stage: 'Submitting…', detail: '' })
  }

  const handleCancel = () => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      sendFrame({ type: 'cancel' })
    }
    closeSocket()
    onOpenChange(false)
  }

  const handleRetry = () => {
    closeSocket()
    setPhase({ kind: 'idle' })
  }

  // ── Render ─────────────────────────────────────────────────────

  const isBusy =
    phase.kind === 'connecting' || phase.kind === 'progress' || phase.kind === 'prompt'

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? onOpenChange(true) : handleCancel())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Login to Meta Business Suite</DialogTitle>
          <DialogDescription>
            Authenticate a new MBS session. Two-factor / checkpoint prompts will appear here
            in real time.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4 py-4">
          {/* Credentials form (always visible, disabled when busy) */}
          <div className="grid gap-2">
            <Label htmlFor="bridge-email">Email or phone</Label>
            <Input
              id="bridge-email"
              autoComplete="username"
              placeholder="user@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              disabled={isBusy || phase.kind === 'success'}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="bridge-password">Password</Label>
            <Input
              id="bridge-password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={isBusy || phase.kind === 'success'}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="bridge-totp">
              2FA secret <span className="text-muted-foreground">(optional)</span>
            </Label>
            <Input
              id="bridge-totp"
              type="password"
              autoComplete="off"
              placeholder="base32 TOTP secret (e.g. JNLA 4HU6 …)"
              value={totpSecret}
              onChange={(e) => setTotpSecret(e.target.value)}
              disabled={isBusy || phase.kind === 'success'}
            />
            <p className="text-xs text-muted-foreground">
              If this account has two-factor authentication, paste the base32 secret here and
              codes will be generated automatically. Leave blank to enter codes manually when
              prompted.
            </p>
          </div>

          {/* Progress / prompt / terminal state panel */}
          {phase.kind === 'connecting' && (
            <div className="flex items-center gap-2 rounded-md bg-muted px-3 py-2 text-sm">
              <Loader2 className="h-4 w-4 animate-spin" />
              <span>Connecting…</span>
            </div>
          )}

          {phase.kind === 'progress' && (
            <div className="flex items-center gap-2 rounded-md bg-muted px-3 py-2 text-sm">
              <Loader2 className="h-4 w-4 animate-spin" />
              <div className="flex flex-col">
                <span className="font-medium">{phase.stage}</span>
                {phase.detail && (
                  <span className="text-xs text-muted-foreground">{phase.detail}</span>
                )}
              </div>
            </div>
          )}

          {phase.kind === 'prompt' && (
            <div className="grid gap-3 rounded-md border border-blue-200 bg-blue-50 p-3 dark:border-blue-900 dark:bg-blue-950/40">
              {phase.instructions && <p className="text-sm">{phase.instructions}</p>}
              {phase.fields.map((f, idx) => (
                <div key={f.id} className="grid gap-1">
                  <Label htmlFor={`bridge-field-${f.id}`} className="text-xs uppercase tracking-wide">
                    {f.name || f.id}
                  </Label>
                  <Input
                    id={`bridge-field-${f.id}`}
                    autoFocus={idx === 0}
                    type={f.type === 'password' ? 'password' : 'text'}
                    inputMode={f.type === 'code' ? 'numeric' : undefined}
                    value={phase.values[f.id] ?? ''}
                    onChange={(e) =>
                      setPhase({
                        ...phase,
                        values: { ...phase.values, [f.id]: e.target.value },
                      })
                    }
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') handlePromptSubmit()
                    }}
                  />
                </div>
              ))}
              <div className="flex justify-end">
                <Button
                  onClick={handlePromptSubmit}
                  disabled={!phase.fields.some((f) => phase.values[f.id]?.length)}
                >
                  Submit
                </Button>
              </div>
            </div>
          )}

          {phase.kind === 'success' && (
            <div className="flex items-center gap-2 rounded-md bg-green-50 px-3 py-2 text-sm dark:bg-green-950/40">
              <CheckCircle2 className="h-4 w-4 text-green-600" />
              <span>
                Logged in as <span className="font-mono">{phase.uid}</span>
              </span>
            </div>
          )}

          {phase.kind === 'failure' && (
            <div className="flex items-start gap-2 rounded-md bg-red-50 px-3 py-2 text-sm dark:bg-red-950/40">
              <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-red-600" />
              <div className="flex flex-col">
                <span className="font-medium">Login failed</span>
                <span className="text-xs text-muted-foreground">
                  {phase.message} ({phase.code})
                </span>
              </div>
            </div>
          )}

          {phase.kind === 'error' && (
            <div className="flex items-start gap-2 rounded-md bg-amber-50 px-3 py-2 text-sm dark:bg-amber-950/40">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
              <div className="flex flex-col">
                <span className="font-medium">Transport error</span>
                <span className="text-xs text-muted-foreground">
                  {phase.message} ({phase.code})
                </span>
              </div>
            </div>
          )}
        </div>

        <DialogFooter className="flex items-center justify-between gap-2 sm:justify-between">
          <Button variant="outline" onClick={handleCancel} disabled={phase.kind === 'success'}>
            Cancel
          </Button>
          {phase.kind === 'idle' && (
            <Button onClick={handleStart} disabled={!email || !password}>
              Login
            </Button>
          )}
          {(phase.kind === 'failure' || phase.kind === 'error') && (
            <Button onClick={handleRetry}>Retry</Button>
          )}
          {(phase.kind === 'connecting' ||
            phase.kind === 'progress' ||
            phase.kind === 'prompt') && (
            <Button disabled>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              Working…
            </Button>
          )}
          {phase.kind === 'success' && (
            <Button onClick={() => onOpenChange(false)}>Done</Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
