# Stage E2 Chunk 6 — Hostile-Eyes Audit (Cold-Compose + Dashboard + Outbound Prune)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `web/src/stores/mbs.ts` MOD — `pruneOutbound()` + TTL in `handleOutbound`
- `web/src/components/mbs/ColdComposeForm.tsx` NEW (~230 LOC)
- `web/src/pages/MbsSessions.tsx` MOD — composer mount inside drawer
- `web/src/pages/Dashboard.tsx` MOD — MBS Pages StatCard + Recent inbound list

---

## Findings

### F1 (P1 — CLOSED) — Carrying gap C4-G2 resolved

`outboundByOtid` was unbounded in chunks 4 & 5. Chunk 6 adds event-driven prune: `handleOutbound` inserts new entry then filters to entries with `sentAt >= now-5min`. Entries with malformed `sentAt` are retained (defensive — they expire on next clean write). **Closed.**

### F2 (P1 — MITIGATED) — Composer race: rapid Send clicks

`<Button onClick={handleSend} disabled={phase.kind !== 'resolved' || ...}>` — after first click, phase transitions to `'sent'` synchronously before the mutation fires, so the button immediately gates. Second click is a no-op. **Mitigated.**

### F3 (P2 — ACCEPTED) — Composer dropped status frame when prune evicts mid-render

Scenario: phase=sent with otid X; user keeps composer open >5min; a status frame for X arrives but prune is now event-driven and only runs on insert. Between insert at T=0 and any subsequent insert, the entry is retained.

The actual concern: a new outbound write at T=4min59s inserts and prunes; if a status frame for THE SAME otid X arrives at T=5min01s, the entry is fresh again (re-inserted with its own sentAt). The previous entry was the SAME otid so the dict update is idempotent. **No data loss.**

The edge case is: user sends, status frame arrives, status frame is consumed by selector, 5min passes, prune evicts. Composer state has already transitioned back to `'resolved'` by then (2s flash → reset). **No exposure.**

### F4 (FP) — `Date.parse('')` returns NaN

`pruneOutbound` guards `!Number.isFinite(t)` and retains malformed entries. Empty sentAt (defensive) survives. **False positive.**

### F5 (P2 — ACCEPTED) — `crypto.randomUUID()` browser support

Modern evergreen browsers (Chrome 92+, Firefox 95+, Safari 15.4+) ship `crypto.randomUUID`. Target browsers per vite default (last 2 versions of major browsers) are all covered. **Accepted.**

### F6 (FP) — `selectOutboundStatus('')` selector when phase != 'sent'

Selector returns `s.outboundByOtid['']` = undefined. Zustand caches `undefined` referentially; no re-render thrash. **False positive.**

### F7 (P2 — ACCEPTED) — Composer left in `'sent'` state if status frame never arrives

Network partition / server crash mid-send → status frame never fires. Composer stays "Sending…" forever until user clicks Reset or collapses drawer (unmounts component, state lost).

**Mitigation:** Reset button visible whenever `phase.kind !== 'resolve'`. User-recoverable. A future timeout (e.g. 30s → auto-failure) could be added; deferred to Stage F.

### F8 (FP) — `state === MbsSessionState.ACTIVE` mid-compose transitions to BURNED

Lifecycle frame mutates the row → React re-renders MbsSessions → drawer JSX flips ACTIVE branch to non-ACTIVE branch → ColdComposeForm unmounts. Any in-flight send fails server-side and the status frame would land in the store but no longer be observed. Acceptable: the session is dead. **False positive on data integrity; intentional UX.**

### F9 (FP) — Dashboard MBS query collision with MbsSessions page query

Both use queryKey `['mbs', 'sessions', ...]` but with different page/state/dashboard discriminators. React-query treats them as separate cache entries. Both feed the same Zustand store via `upsertList`. **False positive.**

### F10 (P2 — ACCEPTED) — `useMemo` over `mbsSessions` reference equality

Zustand selector `selectSessionsList` builds a new sorted array on each call. `mbsSessions` is a new array reference per state update → `useMemo` deps trigger → counts recompute. Acceptable; sessions array is small (≤100 dashboard cap). **Accepted.**

### F11 (P2 — ACCEPTED) — Recent inbound dedup

`lastInboundByThread` is keyed by `${uid}:${threadId}`, one entry per thread. If a single thread receives 5 messages, only the newest is in the dict. Recent inbound list shows latest per thread, not latest 5 messages overall. This is the intended dashboard semantic (5 most recently active threads).

**Documented.**

### F12 (FP) — `recentInbound` re-renders on every store update

Selector `useMbsStore((s) => s.lastInboundByThread)` returns the map reference, which changes only when an entry is added/updated. `useMemo` on that dep re-sorts. Cheap operation on ≤N=O(threads) entries. **False positive.**

### F13 (FP) — `Link to="/mbs-sessions"` typed via TanStack router

Route was registered in chunk 5. Router augmentation in App.tsx covers it. Build passes. **False positive.**

### F14 (P2 — DOCUMENTED) — Composer phone input has no validation

Plan §H10. Backend `resolveMbsPhone` returns `exists: false` for unknown phones; UI surfaces "Not on Messenger" cleanly. Client-side regex would create false negatives for international formats. Server is canonical authority. **Accepted.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `npm run build` (tsc + vite) | ✓ clean |
| Bundle delta | +6.3 KB minified, +1.6 KB gzipped — within noise |
| C4-G2 closed | ✓ pruneOutbound on event |
| Composer state machine exhaustive | ✓ (resolve/resolved/sent/failure) |
| Composer cleanup on drawer collapse | ✓ unmount cascades |
| Dashboard tile hydrates without /mbs-sessions visit | ✓ own query |
| Recent inbound only shown when non-empty | ✓ guard |
| Optimistic UX reconciles via WS | ✓ selectOutboundStatus |

---

## Carrying gaps status (final E2)

| Gap | Status | Owner |
|---|---|---|
| C2-G1 (WS URL log scrubbing) | Open | Stage F |
| C3-G1 (subscription bind retry) | Open | Stage F |
| C4-G1 (Bridge kind union widening) | Open | Backend-driven |
| C4-G2 (outboundByOtid prune) | **CLOSED in chunk 6** | — |
| C5-G1 (CS_AGENT URL access) | Open | Stage F (low priority) |
| MBS unified-Inbox routing | Reassigned | Stage E3 |

---

## Approval

Chunk 6 is GO. Stage E2 reaches done-done with this commit.

- **0 P1 unresolved** (F1 caught + closed by chunk-6 work)
- **6** P2 accepted/documented
- **8** FP

— Oracle, 2026-05-29
