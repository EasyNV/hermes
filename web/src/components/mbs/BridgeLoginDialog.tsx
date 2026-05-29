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
import type { MbsSessionState } from '@/api/types'

// ─────────────────────────────────────────────────────────────────────
// BridgeLoginDialog (Stage E2 chunk 5)
//
// Bidirectional WS-driven Meta Business Suite login flow.
// Path: /ws/mbs/bridge-login (mounted OUTSIDE /api/v1; inline JWT
// validation server-side).
//
// State machine:
//   idle       -> form (identifier + password)
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

type Phase =
  | { kind: 'idle' }
  | { kind: 'connecting' }
  | { kind: 'progress'; stage: string; detail: string }
  | { kind: 'prompt'; stepId: string; promptKind: string; prompt: string; value: string }
  | { kind: 'success'; uid: string; fbid: string; state: MbsSessionState }
  | { kind: 'failure'; code: string; message: string }
  | { kind: 'error'; code: string; message: string }

interface BridgeLoginDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: (s: { uid: string; fbid: string; state: MbsSessionState }) => void
}

export function BridgeLoginDialog({ open, onOpenChange, onSuccess }: BridgeLoginDialogProps) {
  const tenantId = useAuthStore((s) => s.tenant?.id) ?? ''

  const [identifier, setIdentifier] = useState('')
  const [password, setPassword] = useState('')
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' })
  const wsRef = useRef<WebSocket | null>(null)

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
      // Don't reset identifier (user might retry with same one); DO
      // zero password.
      setPassword('')
      setPhase({ kind: 'idle' })
    }
  }, [open])

  // On component unmount: belt + suspenders.
  useEffect(() => {
    return () => {
      closeSocket()
      setPassword('')
    }
  }, [])

  // On success: notify parent + auto-close.
  useEffect(() => {
    if (phase.kind === 'success') {
      onSuccess({ uid: phase.uid, fbid: phase.fbid, state: phase.state })
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
      case 'prompt':
        setPhase({
          kind: 'prompt',
          stepId: frame.payload.stepId,
          promptKind: frame.payload.kind,
          prompt: frame.payload.prompt,
          value: '',
        })
        break
      case 'progress':
        setPhase({ kind: 'progress', stage: frame.payload.stage, detail: frame.payload.detail })
        break
      case 'success':
        setPhase({
          kind: 'success',
          uid: frame.payload.uid,
          fbid: frame.payload.fbid,
          state: frame.payload.state,
        })
        closeSocket()
        break
      case 'failure':
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
    if (!identifier || !password) return

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
        payload: { tenantId, identifier, password },
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
    if (!phase.value) return
    sendFrame({ type: 'input', payload: { stepId: phase.stepId, value: phase.value } })
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
            <Label htmlFor="bridge-identifier">Email or phone</Label>
            <Input
              id="bridge-identifier"
              autoComplete="username"
              placeholder="user@example.com"
              value={identifier}
              onChange={(e) => setIdentifier(e.target.value)}
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
            <div className="grid gap-2 rounded-md border border-blue-200 bg-blue-50 p-3 dark:border-blue-900 dark:bg-blue-950/40">
              <Label htmlFor="bridge-prompt" className="text-xs uppercase tracking-wide">
                {phase.promptKind === 'otp_2fa'
                  ? 'Two-factor code'
                  : phase.promptKind === 'checkpoint'
                  ? 'Security checkpoint'
                  : phase.promptKind === 'recovery'
                  ? 'Account recovery'
                  : 'Required input'}
              </Label>
              <p className="text-sm">{phase.prompt}</p>
              <div className="flex gap-2">
                <Input
                  id="bridge-prompt"
                  autoFocus
                  value={phase.value}
                  onChange={(e) =>
                    setPhase({ ...phase, value: e.target.value })
                  }
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') handlePromptSubmit()
                  }}
                />
                <Button onClick={handlePromptSubmit} disabled={!phase.value}>
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
            <Button onClick={handleStart} disabled={!identifier || !password}>
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
