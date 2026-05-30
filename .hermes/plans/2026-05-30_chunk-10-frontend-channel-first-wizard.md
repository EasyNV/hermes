# Chunk 10 — Frontend channel-first campaign wizard

**Date:** 2026-05-30
**Author:** Oracle
**Predecessors:** chunks 7 (`25a422f`), 8 (`474b287`), 9 (`b30f5e7`)
**Branch:** `main`

---

## Goal

Make `/campaigns/new` channel-first: user picks WhatsApp or Meta Business Suite
as step 0, then the sender-selection step renders the right picker. WA flow
preserved byte-identical at user level. New MBS picker honors the chunk-10 D3
rule (show disabled with tooltip for sessions where WEC isn't registered).

---

## Surface

| File | Change |
|---|---|
| `web/src/api/types.ts` | `Campaign.channel: 'wa'\|'mbs'`. `MbsSession.primaryAsset?: MbsAsset` (already returned inline by `/api/v1/mbs-sessions`, just missing on the TS shape). |
| `web/src/api/campaigns.ts` | `createCampaign()` signature accepts `channel?`, `waNumberIds?`, `mbsSessionUids?`. Both lists now optional — caller passes whichever matches the channel. |
| `web/src/pages/CampaignCreate.tsx` | New 6-step flow; new `StepSelectChannel`; new `StepSelectMbsSessions`; channel-switch confirmation; per-channel `canProceed`; mutation passes only the right ID list. |

No backend changes — all of this rides on chunks 8 + 9 which already accept `channel` + `mbsSessionUids`.

---

## Behaviour

### Step order

Old (WA-only, 5 steps):
```
Template → Contacts → Numbers → Configure → Review
```

New (channel-first, 6 steps):
```
Channel → Template → Contacts → Senders → Configure → Review
```

Step index shifts +1 everywhere. `canProceed()` rewritten with per-channel
sender check at step 3.

### StepSelectChannel

Two cards side-by-side. Click flips `form.channel`. On change, if the
off-channel sender list is non-empty, a `window.confirm` prompts before
clearing — matches C10-G1 "no silent data loss".

### StepSelectMbsSessions

- Fetches `listMbsSessions({state: MBS_SESSION_STATE_ACTIVE})` — sessions in
  bridging/burned states are hidden entirely.
- Per session, derives:
  - `wecPhone` = `primaryAsset.wecPhoneNumber`
  - `wecRegistered` = `primaryAsset.wecAccountRegistered`
  - `disabled = !wecPhone || !wecRegistered`
- Disabled cards are rendered with `opacity-60`, `cursor-not-allowed`,
  amber inline reason hint, and a `title=` tooltip explaining why.
- Selectable cards show the same primary-asset metadata as the MBS sessions
  page drawer: page name, formatted WEC phone, biz name, UID, PRIMARY badge,
  WEC ✓/✗ indicator.
- Checkbox + card click both toggle selection; `disabled` blocks both.

### Channel-aware mutation

```ts
createCampaign({
  ...,
  channel: form.channel,
  waNumberIds: form.channel === 'wa' ? form.waNumberIds : undefined,
  mbsSessionUids: form.channel === 'mbs' ? form.mbsSessionUids : undefined,
})
```

Even though the switch handler already clears the off-channel list, we
explicitly omit it here as defense in depth against the chunk-8 server-side
mutual-exclusion rejection (C10-G4).

### StepReview rename

`numberCount` → `senderCount`. Added "Channel" row showing
"WhatsApp" or "Meta Business Suite". Sender row now says either
"N WhatsApp numbers" or "N MBS sessions".

---

## Contracts

- **C10-G1** — Channel switch with senders selected MUST prompt (avoid silent
  data loss). Implemented via `window.confirm` in `handleChannelChange`.
- **C10-G2** — `canProceed()` validates per-channel at step 3.
- **C10-G3** — MBS picker show-disabled-with-tooltip for unregistered WEC /
  no-phone / no-asset sessions (D3 decision).
- **C10-G4** — Mutation omits the off-channel ID list explicitly.
- **C10-G5** — WA flow unchanged at user level. Verified: tsc + vite build
  clean, all existing WA-flow code paths intact.

---

## Verification gates

1. ✅ `npx tsc --noEmit` clean
2. ✅ `npm run build` clean (`vite build` produces dist/ as before — same
    pre-existing chunk-size warning, no new warnings)
3. ✅ Web image rebuilt + deployed (`hermes-web:b30f5e7-dirty`)
4. ⚠️ Browser E2E smoke partially deferred — this local prod-compose stack's
    web container nginx is SPA-only and doesn't proxy `/api/*` to the gateway
    (production setup uses an external reverse proxy). Direct API smoke
    proves the contract works (chunks 8 + 9 already verified end-to-end).
5. ✅ Backend contracts verified in chunk 8 + 9 — wizard rides on those.

---

## Out of scope

- Per-campaign `page_id_override` UI surface (proto field exists; UX iteration
  later)
- Spintax-aware MBS template warnings (engine resolves spintax regardless
  of channel)
- MBS-specific ban-pause threshold (proto doesn't surface one; Stage G if
  it's ever needed)
- Web container nginx API-proxy wiring (separate deploy chunk if Sam wants
  the local prod stack to be browser-testable without a fronting proxy)

---

## Commit message (target)

```
feat(web): chunk 10 — channel-first campaign wizard with MBS picker

Restructures CampaignCreate.tsx to a 6-step channel-first flow:
  Channel → Template → Contacts → Senders → Configure → Review

New components:
  StepSelectChannel       two-card WA/MBS picker (data-testid="channel-wa|mbs")
  StepSelectMbsSessions   MBS picker filtered to ACTIVE state, with D3
                          show-disabled-with-tooltip rule for sessions
                          where wecAccountRegistered=false or wecPhoneNumber=""

API surface:
  api/types.ts: Campaign.channel ('wa'|'mbs'), MbsSession.primaryAsset
  api/campaigns.ts: createCampaign() accepts channel + mbsSessionUids;
                    waNumberIds and mbsSessionUids both optional, exactly
                    one populated per channel.

Behaviour:
  - Channel switch prompts before clearing non-empty sender list (C10-G1)
  - canProceed() per-channel sender check at step 3 (C10-G2)
  - MBS picker filter: hidden if non-ACTIVE; disabled if no primary
    asset / no wec phone / WEC not registered (C10-G3)
  - createCampaign() omits the off-channel ID list — defense in depth
    against backend mutual-exclusion rejection (C10-G4)
  - Existing WA flow byte-identical at the user level (C10-G5)

tsc --noEmit + vite build clean.

Plan: .hermes/plans/2026-05-30_chunk-10-frontend-channel-first-wizard.md
```
