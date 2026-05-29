# Stage E3 chunk 5 — frontend channel surface + inbox MBS bridge

**Date:** 2026-05-29
**Author:** Oracle
**Builds on:** E3 chunks 1–4 (`cea5974`, `1aa234d`, `ef10e53`, `225e742`)
**Effort:** ~3h
**Target LOC:** ~280 (proto edits + handler/REST glue + TS types + Inbox.tsx + inbox.ts store + websocket.ts bridge + 2 small components)

---

## Goal

Land MBS conversations in the existing `/inbox` UI alongside WA so an agent can:
1. **See** MBS threads in the conversation list with a small `MBS` badge.
2. **Filter** the list by channel (`All`/`WA`/`MBS`).
3. **Read** MBS message history in the drawer (channel-aware metadata — page name, thread id).
4. **Reply** via the existing composer — the channel-aware backend (chunk 4) routes correctly.
5. **Realtime**: an incoming MBS message updates the inbox list **even without a full page refetch**, via a frontend bridge from the existing `mbs_new_message` WS frame.

---

## Non-goals (deferred)

- Dedicated MBS-only inbox view.
- Mid-conversation channel switching (impossible by design — a conversation IS a channel).
- Mobile-responsive inbox layout for MBS metadata.
- MBS outbound media (already documented as E3.4-G1).

---

## Contracts

### C5-K1 — Proto: add `channel` filter to ListConversations

Both `ListConversationsRequest` (gateway proto) and `InboxListConversationsRequest` (inbox proto) get an optional `channel` filter. Empty/UNSPECIFIED = both channels.

```proto
// gateway.proto::ListConversationsRequest
// + (after field 6 pagination)
InboxChannel channel = 7;  // UNSPECIFIED = all

// inbox.proto::InboxListConversationsRequest
// + (after field 7 pagination)
InboxChannel channel = 8;  // UNSPECIFIED = all
```

Need to verify `InboxChannel` enum lives in `common.proto` and is importable by both — chunk 1 already added it.

### C5-K2 — Gateway handler wiring

`handler.go::ListConversations` already constructs `InboxListConversationsRequest`; just thread the new field. REST adapter (`rest/handlers.go::listConversations`) reads `?channel=` query param and parses to enum.

### C5-K3 — TS API surface

`web/src/api/types.ts`:
- `ListConversationsRequest` doesn't exist as a type today (REST adapter passes object literal). The `listConversations` wrapper in `api/inbox.ts` adds an optional `channel?: InboxChannel` param.

`web/src/api/inbox.ts`:

```ts
export function listConversations(params: {
  workspaceId: string; status?: ConversationStatus; assignedTo?: string
  waNumberId?: string; search?: string
  channel?: InboxChannel                          // NEW
} & PageRequest) { /* … unchanged */ }
```

The query-string serializer (`qs`) already URL-encodes object keys 1:1, so the new param is auto-wired.

### C5-K4 — Inbox.tsx channel filter

Single Segmented control above the conversation list:

```
[ All ] [ WA ] [ MBS ]
```

Default `All`. Wired via React Query `queryKey: [..., channel]`. State lives in the page component (small enough not to pollute the store).

Selecting WA or MBS calls `listConversations({ ..., channel: 'INBOX_CHANNEL_WA' | 'INBOX_CHANNEL_MBS' })`.

### C5-K5 — Channel badge in conversation list

Tiny pill rendered next to the contact name when `conv.channel === 'INBOX_CHANNEL_MBS'`:

```tsx
{conv.channel === InboxChannel.MBS && (
  <span className="bg-blue-100 text-blue-700 text-[10px] px-1 rounded">MBS</span>
)}
```

WA badge omitted — WA is the default; visual noise. (Sam already pushed back on always-on labels in E2.)

### C5-K6 — Drawer MBS metadata

When the active conversation is MBS, the drawer header substitutes "WhatsApp number" with "Meta Business Suite":

```tsx
{conv.channel === InboxChannel.MBS
  ? <div>Via Meta Business Suite · Thread {conv.mbsThreadId.slice(-8)}</div>
  : <div>Via WA {waNumber.phoneE164}</div>}
```

If `conv.mbsPageId` is non-empty, show as a sub-row.

### C5-K7 — WS bridge: mbs_new_message → inbox.handleNewMessage

The gateway already broadcasts `mbs_new_message` (E2-C3). Today it only feeds the dashboard tile + MBS store. We add a bridge in `useInboxStore.handleMbsNewMessage`:

1. Find conversation by `(uid, threadId)` via `mbsSessionUid + mbsThreadId` index.
2. If found: synthesize a `WsNewMessagePayload`-shaped object with `conversationId = found.id` + the MBS message body and dispatch through the existing `handleNewMessage`. **No new render path.**
3. If not found: invalidate the inbox list query so the next refetch picks up the new conversation row.

Implementation in `websocket.ts::handleEvent`:

```ts
case 'mbs_new_message':
  mbs.handleInbound(event.payload)                // existing
  inbox.handleMbsNewMessage(event.payload)         // NEW — synthesizes new_message shape
  break
```

The `handleMbsNewMessage` shim lives in `inbox.ts`. It does NOT replace the existing WA `new_message` handler — the WA path is the gateway converting the WaInboundMessageEvent to a `new_message` frame on its own. For MBS, the gateway has NOT been wired to do that conversion (only `mbs_new_message`), so we bridge frontend-side. Cleaner than adding a fan-out subscriber in gateway.

### C5-K8 — Message status (outbound) for MBS

Currently `message_status_updated` WS frame is WA-only. For MBS we have `mbs_outbound_status` (E2-C3). Inbox needs to route THAT to `updateMessageStatus` for the active conversation. Bridge:

```ts
case 'mbs_outbound_status':
  mbs.handleOutbound(event.payload)                // existing
  // Find messageId in inbox messages by mbs_mid; update status
  inbox.handleMbsOutboundStatus(event.payload)     // NEW
  break
```

`handleMbsOutboundStatus(payload)` finds the local message by `mbs_mid` (chunk 4 SetMbsMID populated this from the outbound event) AND by `payload.ok ? 'sent' : 'failed'` and dispatches the status. There's a race: if the inbox refetch hasn't run yet, the local message may still have empty mbs_mid. **Fallback:** check the message list for any pending outbound where conversation.channel === 'mbs' and update; ID-by-ID resolution happens on the eventual refetch.

Actually simpler: the inbox-service outbound consumer (chunk 4) does the DB UPDATE. The next list refetch surfaces the right state. The WS bridge is just a UX speed-up. **Decision: ship the bridge but accept it as best-effort.**

### C5-K9 — Composer behavior

The composer in Inbox.tsx already calls `sendMessage(conv.id, { contentType, body })`. The backend (chunk 4) routes based on `conv.channel`. **No frontend change required.**

Disable image/document buttons when `conv.channel === MBS` (text-only per E3.4-G1):

```tsx
const canSendMedia = conv.channel !== InboxChannel.MBS
{canSendMedia && <AttachButton/>}
```

### C5-K10 — Error semantics

| Error | Behavior |
|---|---|
| `listConversations({ channel: 'mbs' })` with no MBS rows | Empty list, no error |
| Unknown channel value in URL | Server returns full list (UNSPECIFIED fallback) |
| WS `mbs_new_message` for unknown conv + list query in-flight | Skipped; next refetch reconciles |
| Composer send on MBS conv with media attachment | Backend returns `Unimplemented`; UI shows error toast (existing pattern) |

---

## Files touched

| # | File | Change | LOC |
|---|---|---|---|
| 1 | `proto/hermes/v1/gateway.proto` + `docs/contracts/proto/gateway.proto` | Add `InboxChannel channel = 7` to `ListConversationsRequest` | ~4 |
| 2 | `proto/hermes/v1/inbox.proto` + `docs/contracts/proto/inbox.proto` | Add `InboxChannel channel = 8` to `InboxListConversationsRequest` | ~4 |
| 3 | `make proto-gen` | Regen Go + TS stubs | — |
| 4 | `internal/gateway/handler/handler.go` | Thread channel into inboxReq | ~3 |
| 5 | `internal/gateway/rest/handlers.go` | Parse `?channel=` query param | ~6 |
| 6 | `internal/inbox/handler/handler.go` | Pass `req.Channel` (string form) to `store.ListConversations` (already accepts channel arg from chunk 2) | ~4 |
| 7 | `internal/inbox/handler/handler_test.go` | 1 new test for channel filter | ~30 |
| 8 | `web/src/api/inbox.ts` | Add `channel?` param to `listConversations` | ~2 |
| 9 | `web/src/stores/inbox.ts` | `handleMbsNewMessage` + `handleMbsOutboundStatus` shims | ~50 |
| 10 | `web/src/stores/websocket.ts` | Bridge dispatch | ~3 |
| 11 | `web/src/pages/Inbox.tsx` | Channel filter, badge, drawer metadata, composer media gate | ~100 |
| 12 | `web/src/components/inbox/ChannelBadge.tsx` (new) | Reusable tiny badge | ~25 |
| 13 | `web/src/components/inbox/ChannelFilter.tsx` (new) | Segmented control | ~45 |

---

## Implementation steps

### Step 1 — proto changes + regen

Edit both proto pairs, then `PATH=~/go/bin:$PATH buf generate`.

### Step 2 — Gateway handler + REST adapter

Wire the new field through gateway.ListConversations and the REST adapter's query-param parsing.

### Step 3 — Inbox handler test

Add `TestListConversations_ChannelFilter` to handler_test.go using the existing mockStore (which already accepts the `channel` arg from chunk 2).

### Step 4 — TS types + API client

Edit `api/inbox.ts` to expose the new optional param.

### Step 5 — Inbox store shims

Add `handleMbsNewMessage` and `handleMbsOutboundStatus` methods to `inbox.ts`. Both find by `(mbsSessionUid + mbsThreadId)` or `mbsMid` respectively. NO state mutation if no local match.

### Step 6 — Two new components

`ChannelBadge.tsx` and `ChannelFilter.tsx`. Both small enough to be self-contained, no Zustand dependency.

### Step 7 — Inbox.tsx integration

Hardest step — patch in 4 places:
1. Add channel filter state + segmented control above conversation list
2. Pass `channel` to `listConversations` query
3. Render `<ChannelBadge channel={conv.channel}/>` next to contact name
4. Conditionalize drawer metadata + composer attachments by `conv.channel`

### Step 8 — WebSocket bridge

Add the 2 new dispatch calls in `websocket.ts::handleEvent`.

### Step 9 — Build + verify

- `npm run build` — verifies tsc + bundle clean
- `go build ./...` — backend changes compile
- `go vet ./...`
- `go test -race -count=2 ./internal/inbox/... ./internal/gateway/...`

---

## Hostile-eyes pre-audit (caught at plan stage)

| # | Issue | Resolution |
|---|---|---|
| C5-P1 | **Filter state lives in the page component.** If user navigates away and back, filter resets. | P2 — accepted; URL search param could fix it but adds complexity. Sam's current usage pattern is "set once, work". |
| C5-P2 | **`mbs_new_message` and `new_message` could double-fire for MBS** if we wire the gateway side later. | Documented constraint. The bridge is frontend-only. If the gateway ever fans MBS into `new_message`, remove the frontend shim. |
| C5-P3 | **`handleMbsOutboundStatus` bridge is best-effort** because mbs_mid may be empty pre-correlation. | Accepted — chunk-4 SetMbsMID populates it via the next-tick refetch. UI shows pending until then; ~30s lag acceptable. |
| C5-P4 | **Composer disables media buttons but doesn't show why.** | Add a tooltip: "MBS doesn't support media in this version." |
| C5-P5 | **Channel filter on REST adapter expects enum string `INBOX_CHANNEL_MBS`.** Query param parsing must map "mbs" → enum or accept the full enum string. | Adopt full enum string (`INBOX_CHANNEL_WA`/`INBOX_CHANNEL_MBS`) for consistency with status filter pattern already in use. |
| C5-P6 | **Store interface `ListConversations` `channel` param expects "wa"/"mbs" (string per chunk 2).** Handler must map proto enum → string. | Add mapping function `channelEnumToStr` in inbox handler (analogous to existing converters). |
| C5-P7 | **Refetch invalidation cadence**: when WS fires for an unknown MBS conv, we invalidate the list query. Many incoming threads at once = N invalidations = N refetches. | React Query dedupes refetches within 0ms cadence by default. Acceptable. |
| C5-P8 | **`ChannelFilter` component imports `InboxChannel` enum from `@/api/types`.** Already exported. |  |
| C5-P9 | **Existing `Inbox.tsx` `if (payload.isNewConversation)` synth path defaults `channel: 'INBOX_CHANNEL_WA'`.** This is wrong for MBS. | The MBS bridge synthesizes its own payload with `channel: 'INBOX_CHANNEL_MBS'`, so the WA-defaulting branch is only hit by genuine WA new conversations. Document the asymmetry. |
| C5-P10 | **TS strict mode: `WsMbsNewMessagePayload` shape vs `WsNewMessagePayload`.** Different fields. The bridge must construct a `WsNewMessagePayload` with synthesized values (waNumberId=""). | Map fields explicitly in `handleMbsNewMessage`. Type-check fails loudly if shape diverges. |

---

## Open issues / carrying gaps

| # | Gap | Severity | Owner | Plan |
|---|---|---|---|---|
| **E3.5-G1** | Channel filter not persisted in URL. | L | future | URL search param. |
| **E3.5-G2** | Composer media buttons disabled silently for MBS. | L | this chunk | Add tooltip. |
| **E3.5-G3** | WS bridge is best-effort; status reconciliation may lag a refetch interval. | L | future | gateway-side MBS→new_message fan-out. |

---

## Verification gates

```
PATH=~/go/bin:$PATH buf generate     # regenerate Go + TS stubs
go vet  ./internal/inbox/... ./internal/gateway/...
go build ./...
go test -race -count=2 ./internal/inbox/... ./internal/gateway/...
cd web && npm run build              # tsc + bundle
```

---

## Commit message

```
web(inbox): channel-aware filter + badge + MBS WS bridge (Stage E3 chunk 5)

Backend:
- ListConversationsRequest + InboxListConversationsRequest gain a
  channel filter (UNSPECIFIED = both).
- gateway REST adapter parses ?channel= query param.
- gateway.ListConversations threads channel to inbox.

Frontend:
- listConversations(channel?) wired in api/inbox.ts.
- New ChannelFilter (segmented control: All/WA/MBS) above conversation list.
- ChannelBadge ("MBS" pill) shown next to contact name on MBS conversations.
- Drawer header renders MBS metadata (page id, thread id tail) for MBS.
- Composer disables media buttons + tooltips on MBS (text-only per E3.4-G1).
- WebSocket bridge: mbs_new_message → inbox.handleMbsNewMessage finds the
  local conversation by (uid, threadId) and routes through handleNewMessage.
  mbs_outbound_status → inbox.handleMbsOutboundStatus updates pending
  outbound row by mbs_mid (best-effort; refetch reconciles laggards).

E3.5-G1: filter not URL-persisted.
E3.5-G3: WS bridge is best-effort; ~30s lag possible.

Refs: .hermes/plans/2026-05-29_stage-e3-chunk5-frontend-channel-surface.md
```

— Oracle, 2026-05-29
