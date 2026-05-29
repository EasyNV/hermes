import { useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Loader2, Send, CheckCircle2, AlertCircle, Search } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useMbsStore, selectOutboundStatus } from '@/stores/mbs'
import { resolveMbsPhone, sendMbsMessage } from '@/api/mbs'
import { ApiError } from '@/api/client'

// ─────────────────────────────────────────────────────────────────────
// ColdComposeForm (Stage E2 chunk 6)
//
// Inline composer for cold-starting a Messenger thread from a phone:
//   1. resolve phase  — phone input + Resolve button
//   2. resolved phase — show threadId, allow message entry + Send
//   3. sent phase     — optimistic "Sending…", reconcile via WS
//   4. failure        — surface server error inline
//
// otid is client-generated via crypto.randomUUID() — the server echoes
// it on the mbs_outbound_status WS frame so we can match the result
// back to this exact send attempt.
// ─────────────────────────────────────────────────────────────────────

type Phase =
  | { kind: 'resolve' }
  | { kind: 'resolved'; pageId: string; threadId: string; phone: string }
  | { kind: 'sent'; otid: string; threadId: string; pageId: string; phone: string }
  | { kind: 'failure'; message: string }

interface ColdComposeFormProps {
  uid: string
}

export function ColdComposeForm({ uid }: ColdComposeFormProps) {
  const [phone, setPhone] = useState('')
  const [text, setText] = useState('')
  const [phase, setPhase] = useState<Phase>({ kind: 'resolve' })

  const resolveMut = useMutation({
    mutationFn: () => resolveMbsPhone(uid, phone.trim()),
    onSuccess: (r) => {
      if (!r.exists) {
        setPhase({ kind: 'failure', message: 'This phone is not on Messenger.' })
        return
      }
      setPhase({ kind: 'resolved', pageId: r.pageId, threadId: r.threadId, phone: phone.trim() })
    },
    onError: (e: unknown) => {
      const msg = e instanceof ApiError ? e.message : 'Resolve failed'
      setPhase({ kind: 'failure', message: msg })
    },
  })

  const sendMut = useMutation({
    mutationFn: ({ threadId, otid }: { threadId: string; otid: string }) =>
      sendMbsMessage(uid, { threadId, text: text.trim(), otid }),
    onError: (e: unknown) => {
      const msg = e instanceof ApiError ? e.message : 'Send failed'
      setPhase({ kind: 'failure', message: msg })
    },
  })

  const handleResolve = () => {
    if (!phone.trim()) return
    resolveMut.mutate()
  }

  const handleSend = () => {
    if (phase.kind !== 'resolved') return
    if (!text.trim()) return
    const otid = crypto.randomUUID()
    setPhase({ kind: 'sent', otid, threadId: phase.threadId, pageId: phase.pageId, phone: phase.phone })
    sendMut.mutate({ threadId: phase.threadId, otid })
  }

  // Reconcile sent phase against the WS-driven store.
  const outboundStatus = useMbsStore(
    selectOutboundStatus(phase.kind === 'sent' ? phase.otid : ''),
  )

  // On success: 2s flash, then drop back to resolved so the operator
  // can send a follow-up to the same thread.
  useEffect(() => {
    if (phase.kind !== 'sent' || !outboundStatus) return
    if (!outboundStatus.ok) {
      setPhase({ kind: 'failure', message: outboundStatus.error || 'Send failed' })
      return
    }
    setText('')
    const t = setTimeout(() => {
      setPhase((prev) =>
        prev.kind === 'sent'
          ? { kind: 'resolved', threadId: prev.threadId, pageId: prev.pageId, phone: prev.phone }
          : prev,
      )
    }, 2000)
    return () => clearTimeout(t)
  }, [phase, outboundStatus])

  const handleReset = () => {
    setPhase({ kind: 'resolve' })
    setText('')
    // Keep phone so user can retry without re-typing.
  }

  return (
    <div className="space-y-3 text-sm">
      <div className="flex items-center justify-between">
        <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Cold compose
        </h4>
        {phase.kind !== 'resolve' && (
          <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={handleReset}>
            Reset
          </Button>
        )}
      </div>

      {/* Phone input + resolve */}
      <div className="flex items-end gap-2">
        <div className="flex-1 space-y-1">
          <Label htmlFor={`compose-phone-${uid}`} className="text-xs">
            Phone
          </Label>
          <Input
            id={`compose-phone-${uid}`}
            placeholder="+628123456789"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            disabled={
              resolveMut.isPending ||
              phase.kind === 'resolved' ||
              phase.kind === 'sent'
            }
          />
        </div>
        {phase.kind === 'resolve' && (
          <Button
            onClick={handleResolve}
            disabled={!phone.trim() || resolveMut.isPending}
          >
            {resolveMut.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Search className="mr-2 h-4 w-4" />
            )}
            Resolve
          </Button>
        )}
      </div>

      {/* Resolved state — show thread + message composer */}
      {(phase.kind === 'resolved' || phase.kind === 'sent') && (
        <>
          <div className="rounded-md bg-muted/60 px-3 py-2 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Thread</span>
              <span className="font-mono">{phase.threadId.slice(0, 16)}…</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">Page</span>
              <span className="font-mono">{phase.pageId || '—'}</span>
            </div>
          </div>
          <div className="space-y-1">
            <Label htmlFor={`compose-text-${uid}`} className="text-xs">
              Message
            </Label>
            <Textarea
              id={`compose-text-${uid}`}
              rows={3}
              value={text}
              onChange={(e) => setText(e.target.value)}
              disabled={phase.kind === 'sent' && !outboundStatus}
              placeholder="Hi, this is..."
            />
          </div>
          <div className="flex items-center justify-between">
            <div className="text-xs">
              {phase.kind === 'sent' && !outboundStatus && (
                <span className="inline-flex items-center gap-1 text-muted-foreground">
                  <Loader2 className="h-3 w-3 animate-spin" />
                  Sending…
                </span>
              )}
              {phase.kind === 'sent' && outboundStatus?.ok && (
                <span className="inline-flex items-center gap-1 text-green-600">
                  <CheckCircle2 className="h-3 w-3" />
                  Sent ({outboundStatus.latencyMs} ms)
                </span>
              )}
            </div>
            <Button
              onClick={handleSend}
              disabled={
                phase.kind !== 'resolved' ||
                !text.trim() ||
                sendMut.isPending
              }
            >
              <Send className="mr-2 h-4 w-4" />
              Send
            </Button>
          </div>
        </>
      )}

      {/* Failure surface */}
      {phase.kind === 'failure' && (
        <div className="flex items-start gap-2 rounded-md bg-red-50 px-3 py-2 text-xs dark:bg-red-950/40">
          <AlertCircle className="mt-0.5 h-3 w-3 shrink-0 text-red-600" />
          <span>{phase.message}</span>
        </div>
      )}
    </div>
  )
}
