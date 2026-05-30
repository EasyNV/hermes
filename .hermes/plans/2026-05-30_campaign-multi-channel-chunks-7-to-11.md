# Campaign multi-channel + tag filter — Chunks 7 through 11

**Date:** 2026-05-30
**Author:** Oracle
**Predecessors:** Stage F follow-up chunks 1-5 (`667482b → 5bfe2e4 → 4deab03 → 1078cb8 → d99060f`)
**Branch:** `main`

---

## Why this plan exists

Sam reported two campaign-create bugs:

1. **Bug 1:** Cannot select MBS sessions (with WEC phone numbers) in the campaign wizard. Only WhatsApp numbers from `wa_numbers` show up.
2. **Bug 2:** Cannot filter contacts by tags in the campaign wizard. The `/contacts` page has this UI but `/campaigns/new` doesn't.

Sam picked **scope B** for Bug 1 (channel-first wizard — early step picks WhatsApp or Meta Business Suite, then sender selection is channel-specific) and **extract shared component** for Bug 2.

---

## Recon — what's already on disk

This is much further along than initial assumption. The MBS-channel campaign was **partially shipped already**:

| Layer | State | File / Notes |
|---|---|---|
| Proto `MbsCampaignSendTask` | ✅ Defined | `proto/hermes/v1/events.proto:419-438` — full shape including `uid`, `thread_id`/`phone`, `resolved_body`, `page_id_override`, `idempotency_key` |
| NATS subject `hermes.mbs.send.campaign.{tenant}` | ✅ Documented in proto, advertised in `mbs.proto:77` |
| hermes-mbs consumer | ✅ **Live and subscribed** | `cmd/mbs/send_consumers.go:43-82` — `startCampaignConsumer` subscribes, dispatches via `handler.SendMessage` with idempotency via `IdempotencyKey → ClientDedupeId` |
| `handler.SendMessage` | ✅ Works | `internal/mbs/handler/rpc_send_message.go` — accepts `oneof recipient {thread_id|phone}`, has dedupe cache, has resolver fallback |
| `MbsSendMessageRequest.recipient = phone` | ✅ Works | Server resolves via `ResolvePhoneToThreadID` |
| Campaign engine publisher to `hermes.mbs.send.*` | ❌ **MISSING** | `engine.go:231` only publishes `hermes.wa.send.campaign.*` |
| Proto `CampaignCreateRequest.mbs_session_uids` | ❌ Missing | Only `wa_number_ids` exists |
| DB `campaign_numbers` accommodation for MBS | ❌ Missing | Schema is WA-shaped (`wa_number_id UUID`) |
| Gateway REST `/campaigns` MBS plumbing | ❌ Missing | `handler.go:1183` only forwards `WaNumberIds` |
| Frontend channel selector | ❌ Missing | `CampaignCreate.tsx` is single-flow, WA-only |
| Tag filter in `StepSelectContacts` | ❌ Missing | `CampaignCreate.tsx:165-257` has no tag UI; backend already supports `tags` filter via `listContacts` |
| Shared `TagInput` component | ❌ Lives only inline in `Contacts.tsx:29-75` | Needs extraction to `components/shared/` |

**This changes the cost estimate substantially.** Chunk 8 was originally "3-4 chunks of work." Because the proto + NATS subject + MBS consumer are already done, the remaining work is mostly DB schema + engine publisher fork + UI. Probably 3 chunks not 4.

---

## Chunk 7 — Tag filter in campaign contact picker

### Surface

| File | Change |
|---|---|
| `web/src/components/shared/TagInput.tsx` | NEW — extracted from `Contacts.tsx` |
| `web/src/pages/Contacts.tsx` | Replace inline `TagInput` with import; delete the local copy |
| `web/src/pages/CampaignCreate.tsx::StepSelectContacts` | Add `filterTags` state + `TagInput` UI + pass `tags` to `listContacts` |

### Behaviour

- Extract `TagInput` (lines 29-75 of `Contacts.tsx`) verbatim into `components/shared/TagInput.tsx` with the existing `TagInputProps` interface
- `Contacts.tsx` imports from the new location
- `StepSelectContacts` adds:
  - `const [filterTags, setFilterTags] = useState<string[]>([])`
  - `TagInput` rendered next to the search input in the toolbar row
  - Page reset to 1 on tag change (matches search behaviour)
  - `filterTags` in React Query `queryKey` for cache invalidation
  - `tags: filterTags.length > 0 ? filterTags : undefined` in the `listContacts` call

### Contracts

- **C7-G1** — Backend unchanged. `listContacts` + `internal/contacts/handler/store.go:189-194` already filter by tags with AND-cardinality (contact must have all selected tags).
- **C7-G2** — Selected contact IDs persist across filter changes. Selection state lives in the wizard's `form.contactIds`, independent of the listing query.
- **C7-G3** — `Contacts.tsx` behaviour is byte-identical post-extraction. The page already used the inline `TagInput` with the same shape; only the import path changes.
- **C7-G4** — Tag filter does NOT apply to the campaign's eventual contact set. The wizard collects explicit `contactIds`; tag filter only narrows the picker view.

### Verification gates

1. `npx tsc --noEmit` clean
2. `vite build` clean (no new chunk warnings)
3. `/contacts` page works identically (tag filter, search, pagination)
4. `/campaigns/new` step 2 has a tag input above the table; selecting a tag narrows results
5. Combining search + tag filter narrows further (AND)
6. Selecting contacts under tag A, switching to tag B, selecting more — total selection persists

### Out of scope

- Tag autocomplete (would need `listTags` API wired in — separate chunk if you want it later)
- Tag count badges in the filter UI (cosmetic, not blocking)

---

## Chunk 8 — Proto + DB schema for MBS-channel campaigns

This chunk is **schema only**, no behaviour change yet. Splitting it from chunk 9 (engine fork) so the migration lands clean and reviewable before the dispatch path moves.

### Surface

| File | Change |
|---|---|
| `proto/hermes/v1/campaign.proto` + `docs/contracts/proto/campaign.proto` | Add `channel`, `mbs_session_uids` to `CampaignCreateRequest`. Add `channel` to `CampaignRow` (response shape). Add `add_mbs_session_uids`/`remove_mbs_session_uids` to `CampaignUpdateNumbersRequest` (or new `CampaignUpdateSendersRequest`). |
| `proto/hermes/v1/gateway.proto` + `docs/contracts/proto/gateway.proto` | Mirror the above for `CreateCampaignRequest` + `Campaign` message. |
| `migrations/campaign/000002_mbs_channel.up.sql` + `.down.sql` | NEW migration: `campaigns.channel TEXT NOT NULL DEFAULT 'wa' CHECK (channel IN ('wa', 'mbs'))`. Replace `campaign_numbers.wa_number_id UUID` constraint with a discriminated `sender_kind TEXT + sender_id TEXT` pair, OR add a parallel `campaign_mbs_sessions(campaign_id, mbs_session_uid)` table. **Decision below.** |
| `internal/campaign/handler/store.go` | Read/write `channel` column; new methods `AddCampaignMbsSessions` / `GetActiveCampaignMbsSessions` / etc. |
| `internal/campaign/handler/handler.go::CreateCampaign` | Validate channel/sender ID consistency, persist `channel`, dispatch to right add method |
| `internal/gateway/handler/handler.go::CreateCampaign` | Forward `channel` + `mbs_session_uids` to campaign client |
| `internal/gateway/rest/handlers.go` | No change needed (proto-driven through `readProto`) |

### Schema decision — confirmed: discriminated column (option α)

Sam picked **α (discriminated column)**. `campaign_numbers` becomes
`campaign_senders` with `sender_kind` + `sender_id` (TEXT). Single
rotation/dispatch query no matter the channel; future channels drop in
cleanly. The data migration on the small senders table is acceptable
(rows = num_campaigns × num_senders_per_campaign, low cardinality).

For `campaign_contacts` (chunk 9) Sam picked **9-α (additive BIGINT
column)** to AVOID renaming the potentially-large contacts log table.
The asymmetry is intentional: discriminate the small table, keep the
large one additive.

### Behaviour

**`CampaignCreateRequest` validation rules added in `handler.go::CreateCampaign`:**
- `channel = "wa"` → `wa_number_ids` MUST be non-empty, `mbs_session_uids` MUST be empty
- `channel = "mbs"` → `mbs_session_uids` MUST be non-empty, `wa_number_ids` MUST be empty
- Empty `channel` defaults to `"wa"` (backward-compat for any existing wire callers)
- Invalid combinations → `InvalidArgument`

**`Campaign` response shape:**
- Adds `string channel = N` field
- Existing fields preserved

### Contracts

- **C8-G1** — Migration is additive. Existing campaigns get `channel='wa'` via the NOT NULL DEFAULT. Down migration drops the column AND the new table (no data loss for WA).
- **C8-G2** — Wire-compat: old clients omitting `channel` continue to work (server defaults to 'wa'). New `mbs_session_uids` field is `repeated int64` (matches `mbs_sessions.uid` shape).
- **C8-G3** — `wa_number_ids` and `mbs_session_uids` are mutually exclusive at the API boundary. Server REJECTS mixed-channel campaigns with `InvalidArgument`.
- **C8-G4** — `proto-gen` + Go build green. No callers of `CampaignRow` get type errors from the new field (Go embeds the default).
- **C8-G5** — Tenant boundary enforced: gateway resolves `tenantID` from JWT, campaign service must check that every `mbs_session_uid` belongs to the same tenant (mirrors the WA-number tenant check already in place).

### Verification gates

1. `make proto-gen` (with the documented PATH hack) clean
2. `go build ./...` + `go test ./internal/campaign/...` green
3. Migration applies cleanly against the live stack: `migrate up` then `migrate down` then `migrate up` again — no data loss on intermediate WA-only campaigns
4. POST to `/api/v1/campaigns` with `channel="mbs"` + `mbs_session_uids=[61590134170831]` returns 200 with `campaign.channel == "mbs"`
5. POST with `channel="wa"` + `wa_number_ids=[…]` continues to work (regression check)
6. POST with both `wa_number_ids` and `mbs_session_uids` set returns 400 InvalidArgument

### Out of scope (deferred to chunk 9)

- Engine actually dispatching MBS campaigns — chunk 8 just persists the data, chunk 9 wires the dispatch loop

---

## Chunk 9 — Campaign engine MBS dispatcher

### Surface

| File | Change |
|---|---|
| `internal/campaign/engine/engine.go::dispatchLoop` | Branch on `campaign.Channel`. For `'mbs'`, call the new `dispatchMbsBatch`; for `'wa'`, current behaviour. |
| `internal/campaign/engine/engine_mbs.go` | NEW — MBS-specific dispatch (rotation across MBS sessions, build `MbsCampaignSendTask`, publish to `hermes.mbs.send.campaign.{tenant}`) |
| `internal/campaign/engine/rotation_mbs.go` | NEW — rotation strategies for MBS sessions (round-robin + least-used over `mbs_session_uid` instead of `wa_number_id`). Likely copy + edit of `rotation.go`. |
| `internal/campaign/handler/store.go` | NEW methods: `GetActiveCampaignMbsSessions`, `UpdateMbsSessionSentCount`, `UpdateContactSentMbs` |
| `migrations/campaign/000003_campaign_mbs_session_stats.up.sql` (if needed) | Adds `sent_count`/`failed_count` columns to `campaign_mbs_sessions` (mirrors `campaign_numbers` per-sender stats) |

### Behaviour

**MBS dispatch path (`dispatchMbsBatch`):**
1. Load active MBS sessions for the campaign (`GetActiveCampaignMbsSessions`)
2. For each pending contact:
   - Pick a session via rotation strategy (round-robin or least-used)
   - Apply daily cap check (per-session, like WA's per-number cap)
   - Build `MbsCampaignSendTask`:
     - `uid` = picked session's mbs_session_uid
     - `recipient_phone` = contact.Phone (E.164 minus leading `+`, matching the consumer's expectation)
     - `thread_id` = "" (let hermes-mbs resolve; future optimization: pre-resolve in batch)
     - `resolved_body` = spintax-resolved template body
     - `page_id_override` = "" (let primary asset rule pick — future: per-campaign page override)
     - `idempotency_key` = `campaignID + ":" + contactID`
   - Publish to `hermes.mbs.send.campaign.{tenantID}` with `nats.MsgId(meta.event_id)`
   - Update `campaign_mbs_sessions.sent_count` + `campaign_contacts.status='sent'` + `mbs_session_uid`

**Existing WA path stays untouched** — chunk 9 is additive.

### Schema followups in `campaign_contacts`

The existing `campaign_contacts.wa_number_id UUID` column is WA-shaped. For MBS campaigns we need to know which session sent the message (for retry/audit). Two options:

- **9-α:** Add `mbs_session_uid BIGINT` column alongside (NULL when WA). Discriminate by channel.
- **9-β:** Rename to `sender_id TEXT` and store the appropriate ID. Same trade-off as chunk 8 schema decision.

**Recommendation: 9-α (additive column).** Same logic as chunk 8 β — keep the two channels honest, don't pre-merge. Migration: `ALTER TABLE campaign_contacts ADD COLUMN mbs_session_uid BIGINT`.

### Contracts

- **C9-G1** — MBS dispatch is feature-flagged behind `campaign.channel`. WA campaigns continue to use the existing `dispatchLoop` body verbatim.
- **C9-G2** — `MbsCampaignSendTask.idempotency_key` MUST be deterministic (`campaignID + ":" + contactID`) so consumer-side dedupe cache (`internal/mbs/handler/rpc_send_message.go` dedupe by `(uid, client_dedupe_id)`) suppresses redeliveries.
- **C9-G3** — NATS subject MUST be `hermes.mbs.send.campaign.{tenant_id}` exactly (the consumer's subject filter on `cmd/mbs/send_consumers.go:39` is `hermes.mbs.send.campaign.*`).
- **C9-G4** — Per-session daily cap honored. Rotation skips sessions over cap.
- **C9-G5** — On every send, update `campaign_mbs_sessions.sent_count` + `campaign_contacts(mbs_session_uid, status='sent')` + `campaigns.sent_count`. Failures (cap exhausted, no active sessions) mark campaign as `paused` and emit lifecycle event (mirror WA behavior).

### Verification gates

1. `go vet ./...` + `go test ./internal/campaign/...` green
2. End-to-end smoke: create MBS campaign with 1 session + 1 contact + simple template, start, verify `hermes.mbs.send.campaign.<tenant>` subject receives the task (`nats sub` inspector), verify `mbs_session_outbound_status` event fires back, verify `campaign_contacts.status='sent'`
3. Regression: existing WA campaign creation + dispatch unchanged (run an existing integration test or smoke a WA campaign in the running stack)
4. Failure path: campaign with one MBS session at daily cap → engine marks paused, emits status event

### Out of scope

- Pre-resolution batch (resolve all contacts' phones to thread_ids before publishing — optimization for later)
- Per-page-override at campaign level (the proto field exists, but the wizard doesn't surface it yet — chunk 10 territory)
- MBS-specific ban-pause threshold (WEC rate limits don't surface as ban events the same way)

---

## Chunk 10 — Frontend channel-first wizard

### Surface

| File | Change |
|---|---|
| `web/src/api/types.ts` | Add `channel: 'wa' \| 'mbs'` to `Campaign`. Add `MbsSessionSummary` (subset of MbsSession needed for picker). |
| `web/src/api/campaigns.ts::createCampaign` | Accept `channel` + `mbsSessionUids` |
| `web/src/api/mbs.ts` (existing) | Already has `listMbsSessions` — reuse. Filter for `state === 'active' AND assets has a wecPhoneNumber` happens client-side |
| `web/src/pages/CampaignCreate.tsx` | Restructure: add `StepSelectChannel` as step 0, conditionally render `StepSelectNumbers` (WA) or `StepSelectMbsSessions` (MBS) based on channel |
| `web/src/pages/CampaignCreate.tsx::StepSelectMbsSessions` | NEW component — lists MBS sessions filtered to active+with-phone, shows page name + WEC phone + WABA badge |
| `web/src/components/shared/SenderCard.tsx` (optional) | Extract shared card UI for WA/MBS senders |

### Behaviour

**New step order:** `Select Channel` → `Select Template` → `Select Contacts` → `Select Senders` → `Configure` → `Review`

**`StepSelectChannel`:** two large cards, one for WhatsApp and one for Meta Business Suite. Selecting flips `form.channel`. Resets `waNumberIds` + `mbsSessionUids` if user switches channel mid-flow (with a "you have N senders selected, switching will clear them" confirm dialog if non-empty).

**`StepSelectSenders`:** dispatches on `form.channel` to render either the existing `StepSelectNumbers` (WA) or the new `StepSelectMbsSessions` (MBS).

**`StepSelectMbsSessions`:**
- Calls `listMbsSessions({tenantId, state: MbsSessionState.ACTIVE})` (state filter respects the new proto enum)
- Per row also calls `listMbsSessionAssets(uid)` (or accept asset embedding on `listMbsSessions` response — chunk-4 proto already returns `primaryAsset` inline)
- Filter: only show sessions with at least one asset with `wecPhoneNumber !== ""` (those can receive sends)
- Show: session UID, primary asset's page name, WEC phone (formatted), WABA badge
- Selection multi-select like WA numbers, gives `form.mbsSessionUids`

**Review step:** shows channel in the summary; sender count labelled accordingly ("3 WhatsApp numbers" or "3 MBS sessions").

### Contracts

- **C10-G1** — Channel switch with senders selected MUST prompt (avoid silent data loss).
- **C10-G2** — Wizard's `canProceed()` validates per-channel: WA needs `waNumberIds.length > 0`, MBS needs `mbsSessionUids.length > 0`.
- **C10-G3** — MBS picker filters to sessions with a `wecPhoneNumber` populated. Sessions in `bridging` / `burned` / no-asset states are hidden (or shown disabled with explanation).
- **C10-G4** — `createCampaign` mutation passes `channel`, `waNumberIds` (only for WA), `mbsSessionUids` (only for MBS). Backend rejects the wrong combination, frontend prevents sending the wrong one in the first place.
- **C10-G5** — No regression on WA flow: opening `/campaigns/new`, picking WhatsApp, proceeding through the existing 5 steps lands a working campaign.

### Verification gates

1. `npx tsc --noEmit` clean
2. `vite build` clean
3. WA campaign create end-to-end works (regression)
4. MBS campaign create end-to-end works:
   - Navigate to `/campaigns/new`
   - Pick "Meta Business Suite" channel
   - Select template, contacts (with tag filter from chunk 7), MBS session 61590134170831, configure, launch
   - Verify campaign row created with `channel='mbs'`
   - Verify `campaign_mbs_sessions` populated
   - Verify campaign dispatches via `hermes.mbs.send.campaign.<tenant>`
5. Browser eyeball + screenshot of the new channel selector step

### Out of scope

- Spintax in MBS templates (the engine resolves spintax regardless of channel; the wizard doesn't need to gate on it)
- Per-campaign page_id_override (proto field exists; UI surface for it is a later UX iteration)

---

## Chunk 11 — Verification + audit + cleanup

### Surface

| File | Change |
|---|---|
| `docs/research/mbs-campaign-channel-hostile-audit-2026-05-30.md` | Hostile audit: tenant isolation, cross-channel data leak, idempotency, NATS poison handling, rate-limit interactions |
| `docs/research/mbs-campaign-channel-verification-2026-05-30.md` | UI walkthrough + screenshots of full flow |

### What gets audited

1. **Cross-tenant MBS session smuggling.** Can a CreateCampaign request with `mbs_session_uids=[other-tenant-session-uid]` succeed? (Should fail — verify in chunk 8 server-side check.)
2. **Channel field tampering.** Can a client set `channel='mbs'` but pass `wa_number_ids`? (Should fail with InvalidArgument.)
3. **Idempotency under retry.** Cancel + restart a paused MBS campaign — should NOT double-send contacts. Verify `client_dedupe_id` short-circuits in `internal/mbs/handler/rpc_send_message.go`.
4. **NATS poison messages.** Malformed `MbsCampaignSendTask` published to the subject → consumer should Ack + drop (verified in `cmd/mbs/send_consumers.go:130`).
5. **Session burn during dispatch.** What happens if the MBS session is burned mid-campaign? Engine should mark the session inactive, rotation skips, campaign continues with remaining sessions.
6. **WEC unregistered.** Picker filters by `wecPhoneNumber` presence, but `wec_account_registered` could be false. Should the picker hide those too? (Yes — `wec_account_registered=false` means send-to-phone is disabled per Stage B.2.)

### Verification gates

1. All chunks 7-10 committed and pushed
2. Hostile audit doc covers the 6 surfaces above with explicit pass/fail per attacker scenario
3. UI verification doc with screenshots: channel selector, MBS sender picker, contact tag filter, review screen, launched campaign detail
4. Stack still 12/12 healthy
5. No new lint/test failures from any chunk

---

## Execution order

```
Chunk 7  (tag filter + TagInput extract) — small, ships solo, ~30 min
   ↓
Chunk 8  (proto + DB schema for channel) — moderate, foundation for 9 + 10
   ↓
Chunk 9  (engine MBS dispatcher) — depends on chunk 8 schema
   ↓
Chunk 10 (frontend channel-first wizard) — depends on chunks 8 + 9
   ↓
Chunk 11 (audit + verification + cleanup) — depends on all the above
```

Sub-commits at chunk boundaries. Each chunk has its own commit message + commit hash; chunk 11's audit doc references all four.

## Decisions Sam needs to confirm before chunk 8 starts

| # | Question | My recommendation | Cost if I'm wrong |
|---|---|---|---|
| D1 | Chunk 8 schema: parallel table (β) or discriminated column (α)? | **β (parallel table)** | Re-migration if you change your mind later — but rolling back β is trivial vs. α |
| D2 | Chunk 9 `campaign_contacts.mbs_session_uid`: additive column (9-α) or rename to generic `sender_id` (9-β)? | **9-α (additive)** | Same as D1 — additive is reversible |
| D3 | Chunk 10 picker filter: hide sessions with `wec_account_registered=false`, or show disabled with tooltip? | **Show disabled with tooltip** — explains why send-to-phone is unavailable, easier to debug than missing rows | Cosmetic — easy to change later |
| D4 | Tag filter cardinality: AND (current backend) or OR? | **Keep AND** — matches Contacts page, less surprising | Backend already does AND; OR would need new query path |

Default if you say "go" without qualification: β + 9-α + show-disabled + keep-AND. Total ~5 chunks, sequenced, with audit at the end.

## What I'm explicitly NOT doing

- Phone normalization in the picker (sessions store raw phone; trusted to be canonical from Stage B asset discovery)
- Per-campaign page_id_override UI (proto supports it; we'll add it when there's a clear UX need)
- Multi-channel campaigns (Sam picked B; A is parked unless we change scope later)
- MBS-side ban-pause analogue (different semantics — separate chunk if Stage G surfaces a need)

Ready to start chunk 7 on green light.
