# Stage E2 Chunk 4 — Hostile-Eyes Audit (Frontend API + Store)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `web/src/api/types.ts` MOD — `MbsSessionState` enum, 7 resource/REST interfaces, 3 WS payload interfaces, 3 WsEvent arms.
- `web/src/api/mbs.ts` NEW — 6 REST wrappers + 2 bridge frame unions.
- `web/src/stores/mbs.ts` NEW — Zustand store + 4 selectors.
- `web/src/stores/websocket.ts` MOD — 3 new switch arms.

---

## Findings

### F1 (P2 — ACCEPTED) — `qs()` emits `state=MBS_SESSION_STATE_UNSPECIFIED` if caller passes it

`qs()` drops null/undefined/empty-string but treats any non-empty string (including `'MBS_SESSION_STATE_UNSPECIFIED'`) as a legitimate filter. Callers that pass `state: MbsSessionState.UNSPECIFIED` thinking "any" will instead filter to UNSPECIFIED-state sessions (i.e. nothing).

**Mitigation:** JSDoc on `listMbsSessions` explicitly says "Pass `state` to filter; omit it (do NOT pass UNSPECIFIED) for any state". Chunk 5 hook will omit on the all-states tab.

### F2 (FP) — `MbsSessionState` enum drift vs proto

Verified against `proto/hermes/v1/mbs.proto` and chunk-2 `parseStateFilter`. All 7 values present with matching string serialization. **False positive.**

### F3 (P2 — DOCUMENTED) — Bridge frame `kind` union too narrow for forward-compat

`MbsBridgePromptKind = 'otp_2fa' | 'checkpoint' | 'recovery'`. Backend introducing a 4th value (e.g. `'security_question'`) would surface as TS exhaustive-switch breakage in chunk-5 consumers.

**Trade-off:** Loose `string` loses exhaustive switch (which we want in the dialog). Narrow union forces a re-deploy when backend extends. Chose narrow because forward-extension is a coordinated change.

### F4 (FP) — uid as `string` everywhere

Verified: every `uid` field in types.ts is `string`. WS payloads, REST shapes, store keys all consistent. **False positive.**

### F5 (P2 — ACCEPTED) — `lastInboundByThread` key uses `${uid}:${threadId}` without uid escaping

If a uid or threadId contained `:`, key collisions are possible. uids are decimal numerics (`'1674772559'`) — no colon possible. threadIds are FBID-keyed strings without `:` in any observed sample.

**Mitigation:** Documented assumption. Acceptable until a real collision is observed.

### F6 (FP) — Store reducer race

Zustand `set((s) => ...)` is functional → applies in order on the same tick. Three handlers triggered by three WS frames in rapid succession update independent buckets (`sessions`, `lastInboundByThread`, `outboundByOtid`) so even concurrent updates from different keys don't conflict. **False positive.**

### F7 (P1 — DROPPED-BY-DESIGN) — Lifecycle for unknown uid drops the event

`handleSessionLifecycle` returns the unchanged state when `existing` is undefined. If the lifecycle frame is the first signal that a new session was created, it's lost.

**Mitigation:** Documented as intended. Chunk-5 page subscribes to lifecycle via the WS hook AND mounts a react-query subscription that polls / refetches on focus — a missed CREATE lifecycle is recovered within the next refetch (≤30s under default react-query config). Synthesizing a partial row would surface empty `fbid`, `cookieExpiresAt`, etc. in the UI as broken rows — worse failure mode.

### F8 (FP) — `outboundByOtid` unbounded memory growth

Documented in store comment + plan. Chunk 6 composer will prune. **False positive for chunk 4.**

### F9 (FP) — WS store calls `useMbsStore.getState()` per event

Matches `useInboxStore.getState()` / `useCampaignsStore.getState()` pattern. Zustand's `getState()` is sync and zero-cost. **False positive.**

### F10 (P2 — DEFERRED to chunk 5) — No exhaustive-switch enforcement on `WsEvent`

The `default: break` arm hides missing cases at compile time. TS won't error if a future WsEvent variant is added but not handled.

**Trade-off:** Adding an exhaustive `never`-check would require touching every existing arm. Out of scope for chunk 4. Chunk 5 will add `assertNever(event)` in the default if practical.

### F11 (FP) — `MbsResolvePhoneResponse.exists=false` consumed correctly

Resolve-phone returns `{ threadId: '', pageId: '', exists: false }` when phone not found. Chunk-6 cold-compose UX needs to check `exists` before attempting send. Type forces consideration. **False positive.**

### F12 (P2 — DOCUMENTED) — Bridge frame consumer (chunk 5 dialog) must serialize JSON before sending

`MbsBridgeClientFrame` is a TS type, not a runtime serializer. Chunk-5 dialog will use `JSON.stringify(frame)` before `ws.send`. Documented in chunk-5 plan stub.

### F13 (FP) — `MbsBurnSessionResponse` returns full updated session

Verified against chunk-2 backend handler. Returns `{ session: MbsSession }` (where session has `state: BURNED, burnedAt: <now>, burnedReason: ...`). Store consumer should call `upsertOne` with the returned session. **False positive.**

### F14 (FP) — `MbsListSessionsResponse.pagination` field name

Used `pagination` (matching `ListWaNumbersResponse`). Verified the backend chunk-2 REST handler returns the same shape via the existing `PageResponse`. **False positive.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `npx tsc --noEmit` (full repo type-check) | ✓ clean |
| New types declared with consistent enum-table pattern | ✓ |
| Wire shape locked against chunk-3 frame names | ✓ |
| Path uid → REST uses path params consistently | ✓ (never sends uid in body) |
| Store mutators functional (no race surface) | ✓ |
| WS store routing extended without breaking existing arms | ✓ |
| No new deps added | ✓ |

---

## Carrying gaps tracked

| Gap | Reference | Resolve in |
|---|---|---|
| C2-G3 → CLOSED | TS frame type names match chunk-3 frame names (`mbs_new_message`, `mbs_outbound_status`, `mbs_session_lifecycle`) | This chunk |
| C2-G1 (log scrubbing) | Still outstanding | Stage F |
| C3-G1 (sub-bind retry) | Still outstanding | Stage F |
| C4-G1 (NEW) | Bridge `MbsBridgePromptKind` union must widen if backend adds a kind | When/if it happens |
| C4-G2 (NEW) | `outboundByOtid` prune window | Chunk 6 |

---

## Approval

Chunk 4 is GO.

- **0 P1 unresolved** (F7 is by-design with documented mitigation path)
- **6** P2 documented + accepted
- **8** FP
- **1** carrying gap closed (C2-G3)
- **2** new carrying gaps (C4-G1, C4-G2)

— Oracle, 2026-05-29
