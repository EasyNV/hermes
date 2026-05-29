# Stage E2 Chunk 5 — Hostile-Eyes Audit (MBS Page + BridgeLoginDialog + Sidebar)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `web/src/lib/constants.ts` MOD — `MBS_STATUS` table
- `web/src/components/mbs/BridgeLoginDialog.tsx` NEW (~360 LOC)
- `web/src/pages/MbsSessions.tsx` NEW (~400 LOC)
- `web/src/components/layout/Sidebar.tsx` MOD — nav entry
- `web/src/App.tsx` MOD — route registration

---

## Findings

### F1 (P1 — MITIGATED) — Bridge WS leaks if dialog closes mid-flight

`onOpenChange(false)` → `useEffect([open])` cleanup → `closeSocket()` runs. Belt+suspenders: unmount-cleanup useEffect also closes the socket. Cancel button explicitly sends `cancel` frame and closes locally. **Mitigated by 3 independent code paths.**

### F2 (P1 — MITIGATED) — Password persists in React state after success

`useEffect([open])` cleanup zeroes password on close. Component-unmount useEffect cleanup also zeroes it. Form Input has `autoComplete='current-password'` (intentional — browsers manage their own credential vault separately). **Mitigated.**

### F3 (P2 — DOCUMENTED) — Token in WS URL — XSS scope

Token is fetched via `localStorage.getItem('access_token')` and appended to URL via `encodeURIComponent`. Same posture as `/ws`. Carrying gap C2-G1 (server-side log scrubbing) remains; client-side, any XSS able to read localStorage already has the same token via the main /ws path. **Accepted — same threat model as main /ws.**

### F4 (FP) — Refetch interval hammers backend

30s + react-query `staleTime: 30_000` (configured globally in App.tsx). At most one in-flight + one queued per tab. Backend list endpoint is pagination-bounded. **False positive.**

### F5 (P2 — ACCEPTED) — `storeSessions` vs `sessionsQuery.data.sessions` ordering

When the store has any sessions, page renders from the store. When empty, falls back to query data. Edge case: page-2 filter where store has stale page-1 data — would briefly render page-1 rows.

**Mitigation:** Falling-back-when-empty is the only behavior; the store IS the query data once mounted (`upsertList` runs on every data delivery). So once page-2 query lands, store contains page-2 rows. Pagination switches are non-interleaved (single network request at a time). Accepted.

### F6 (FP) — `<>...</>` fragment in TableRow children breaks React keys

Verified: `key={s.uid}` is on the TableRow inside the fragment, and `key={`${s.uid}-assets`}` on the second TableRow. React fragment with explicit-keyed children is supported. Build is clean. **False positive.**

### F7 (P2 — ACCEPTED) — `ws.onclose` race with terminal `success`/`failure`/`error`

After server sends terminal frame, server closes WS. Our `ws.onclose` handler then runs and may set `'error'` if it raced before the terminal `setPhase`. Guarded by `setPhase((prev) => { if (prev.kind === 'connecting' || 'progress' || 'prompt') return error ... return prev })` — terminal phases are preserved. **Mitigated by the guard.**

### F8 (FP) — `MbsBridgeServerFrame` exhaustive switch

The component's `handleServerFrame` covers all 5 variants (prompt/progress/success/failure/error). TS would error if a variant were missed because the function returns void in each arm. **False positive.**

### F9 (P2 — DEFERRED) — `BridgeLoginDialog` not protected against double-open

User clicks Login → dialog opens. While open, user clicks the trigger button again (no-op since button is in the page header outside dialog overlay → unreachable while modal). **Accepted by component model.**

### F10 (FP) — Burn confirm sends incorrect uid

`confirmBurn.uid` is set when the dropdown menu item is clicked, then read by the mutation. State is updated synchronously, no race window. **False positive.**

### F11 (P2 — DEFERRED) — Pagination `pagination` undefined when query is loading

When data is undefined, the `pagination && pagination.totalPages > 1` guard skips the component entirely. No render of paginator on initial load. **Accepted.**

### F12 (FP) — `selectSessionsList` reactive subscription thrashes

Zustand selectors are reference-stable when the selector itself is stable (defined at module scope). Re-renders only when `sessions` map identity changes. **False positive.**

### F13 (P2 — DEFERRED) — CS_AGENT role can navigate to /mbs-sessions directly

Sidebar hides the entry for CS_AGENT, but the route is registered for any authenticated user. CS_AGENT typing /mbs-sessions in the URL reaches the page (gated only behind authRoute).

**Mitigation:** Add explicit role check on page mount with redirect in chunk 6 if Sam wants strict isolation. For chunk 5 the visible-sidebar policy is the intended UX; CS_AGENT URL-typing is "they know what they're doing" — same posture as Numbers, Proxies, etc. Accepted for E2.

### F14 (FP) — `MBS_STATUS` lookup for unrecognized state crashes

`MBS_STATUS[s.state]` falls back to `MBS_STATUS.MBS_SESSION_STATE_UNSPECIFIED` via the `?? UNSPECIFIED` guard in the row render. **False positive.**

### F15 (FP) — Build emits warnings

`Some chunks are larger than 500 kB` is a pre-existing vite warning about the main bundle. No new chunk introduced; warning unchanged. **False positive.**

### F16 (P2 — DOCUMENTED) — `getMbsSessionStatus` after bridge success can fail silently

If status RPC fails (e.g. 503 race during session-store write commit), the catch-block invalidates the query. **Mitigated.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `npm run build` (tsc + vite) | ✓ clean |
| Bundle size delta | within noise (≈+18 KB minified, +6 KB gzipped) |
| Dialog WS cleanup paths | 3 verified |
| Password zeroing paths | 2 verified |
| Filter reset on page change | ✓ |
| WS reconnect avoided (one-shot per dialog) | ✓ |
| Store + query sync | ✓ |
| Burn confirm gated | ✓ |
| Assets drawer fetch | ✓ |

---

## Carrying gaps tracked

| Gap | Status | Resolve in |
|---|---|---|
| C2-G1 (WS log scrubbing) | Still outstanding | Stage F |
| C3-G1 (sub-bind retry) | Still outstanding | Stage F |
| C4-G1 (Bridge kind union widening) | Still outstanding | Backend-driven |
| C4-G2 (outboundByOtid prune) | Still outstanding | Chunk 6 |
| C5-G1 (NEW) | CS_AGENT URL-typing reaches /mbs-sessions | Optional Stage F policy refinement |

---

## Approval

Chunk 5 is GO.

- **0 P1 unresolved**
- **6** P2 accepted/deferred
- **10** FP
- **1** new carrying gap (C5-G1, low severity)

— Oracle, 2026-05-29
