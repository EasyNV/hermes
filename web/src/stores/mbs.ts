import { create } from 'zustand'
import { MbsSessionState } from '@/api/types'
import type {
  MbsSession,
  WsMbsSessionLifecyclePayload,
  WsMbsNewMessagePayload,
  WsMbsOutboundStatusPayload,
} from '@/api/types'

// ─────────────────────────────────────────────────────────────────────
// MBS sessions store (Stage E2 chunk 4)
//
// Pure state container — no fetch logic. Components hydrate via
// react-query and hand results to upsertList/upsertOne. WS frames
// route in via stores/websocket.ts.
//
// Keys:
//   sessions             — by uid
//   lastInboundByThread  — by `${uid}:${threadId}`
//   outboundByOtid       — by otid
//
// Lifecycle frames that arrive for an unknown uid are DROPPED rather
// than synthesizing a partial session row — chunk-5 page is expected
// to refetch on lifecycle when the session isn't in the store.
// outboundByOtid is unbounded in chunk 4 — chunk 6 composer will prune
// entries older than 5 minutes once the inline send UX lands.
// ─────────────────────────────────────────────────────────────────────

interface MbsStoreState {
  sessions: Record<string, MbsSession>
  lastInboundByThread: Record<string, WsMbsNewMessagePayload>
  outboundByOtid: Record<string, WsMbsOutboundStatusPayload>
  loadedAt: string

  upsertList: (sessions: MbsSession[]) => void
  upsertOne: (session: MbsSession) => void
  removeOne: (uid: string) => void

  handleSessionLifecycle: (p: WsMbsSessionLifecyclePayload) => void
  handleInbound: (p: WsMbsNewMessagePayload) => void
  handleOutbound: (p: WsMbsOutboundStatusPayload) => void
}

// outboundByOtid is bounded by a 5-minute TTL relative to sentAt.
// Pruning is event-driven (runs inside handleOutbound on every write)
// so we never need a background timer. The just-arrived entry is
// preserved by including it in the input map before prune runs.
const OUTBOUND_TTL_MS = 5 * 60 * 1000

function pruneOutbound(
  map: Record<string, WsMbsOutboundStatusPayload>,
): Record<string, WsMbsOutboundStatusPayload> {
  const cutoff = Date.now() - OUTBOUND_TTL_MS
  const out: Record<string, WsMbsOutboundStatusPayload> = {}
  for (const [k, v] of Object.entries(map)) {
    const t = Date.parse(v.sentAt || '')
    // Retain entries with malformed timestamps (defensive — they'll
    // expire on the next clean write); retain entries newer than the
    // cutoff.
    if (!Number.isFinite(t) || t >= cutoff) out[k] = v
  }
  return out
}

export const useMbsStore = create<MbsStoreState>((set) => ({
  sessions: {},
  lastInboundByThread: {},
  outboundByOtid: {},
  loadedAt: '',

  upsertList: (sessions) =>
    set(() => ({
      sessions: Object.fromEntries(sessions.map((s) => [s.uid, s])),
      loadedAt: new Date().toISOString(),
    })),

  upsertOne: (session) =>
    set((s) => ({
      sessions: { ...s.sessions, [session.uid]: session },
    })),

  removeOne: (uid) =>
    set((s) => {
      const next = { ...s.sessions }
      delete next[uid]
      return { sessions: next }
    }),

  handleSessionLifecycle: (p) =>
    set((s) => {
      const existing = s.sessions[p.uid]
      // Drop lifecycle frames for sessions we haven't loaded yet.
      // Synthesizing partial rows (empty fbid, cookieExpiresAt, etc.)
      // would surface broken UI; the consuming page is expected to
      // refetch when it observes a lifecycle for an unknown uid.
      if (!existing) return s

      const burned = p.newState === MbsSessionState.BURNED
      return {
        sessions: {
          ...s.sessions,
          [p.uid]: {
            ...existing,
            state: p.newState,
            podId: p.podId || existing.podId,
            lastConnackRc: p.lastConnackRc,
            burnedReason: burned ? (p.reason || existing.burnedReason) : existing.burnedReason,
            burnedAt: burned ? p.timestamp : existing.burnedAt,
            lastSeenAt: p.timestamp,
          },
        },
      }
    }),

  handleInbound: (p) =>
    set((s) => ({
      lastInboundByThread: {
        ...s.lastInboundByThread,
        [`${p.uid}:${p.threadId}`]: p,
      },
    })),

  handleOutbound: (p) =>
    set((s) => ({
      outboundByOtid: pruneOutbound({
        ...s.outboundByOtid,
        [p.otid]: p,
      }),
    })),
}))

// ─────────────────────────────────────────────────────────────────────
// Selectors
// ─────────────────────────────────────────────────────────────────────

/** Returns an array of sessions sorted by lastSeenAt desc. */
export const selectSessionsList = (s: MbsStoreState): MbsSession[] =>
  Object.values(s.sessions).sort((a, b) => (b.lastSeenAt || '').localeCompare(a.lastSeenAt || ''))

export const selectSessionByUid = (uid: string) => (s: MbsStoreState): MbsSession | undefined =>
  s.sessions[uid]

export const selectLastInbound = (uid: string, threadId: string) => (s: MbsStoreState) =>
  s.lastInboundByThread[`${uid}:${threadId}`]

export const selectOutboundStatus = (otid: string) => (s: MbsStoreState) =>
  s.outboundByOtid[otid]
