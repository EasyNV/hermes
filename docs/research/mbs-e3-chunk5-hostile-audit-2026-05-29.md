# Hostile-eyes audit — Stage E3 chunk 5 (frontend channel surface + inbox MBS bridge)

**Date:** 2026-05-29
**Scope:** All code changes in commit-pending state for chunk 5:
- `proto/hermes/v1/gateway.proto` + `inbox.proto` + their `docs/contracts/proto/` mirrors (add `InboxChannel channel` filter)
- `gen/` regenerated via `buf generate`
- `internal/gateway/handler/handler.go` — threads channel into `InboxListConversationsRequest`
- `internal/gateway/rest/handlers.go` — parses `?channel=` query param (and `?status=` which was previously dropped — see F1)
- `internal/inbox/handler/handler.go` — new `inboxChannelToStr` helper + thread channel into store
- `internal/inbox/handler/handler_test.go` — `TestListConversations_ChannelFilter` (3 sub-tests)
- `web/src/api/inbox.ts` — `channel?` param on listConversations
- `web/src/stores/inbox.ts` — `handleMbsNewMessage` + `handleMbsOutboundStatus` shims (~60 LOC)
- `web/src/stores/websocket.ts` — bridge dispatches
- `web/src/pages/Inbox.tsx` — channel filter state + UI, badge in list + drawer header, MBS metadata sub-row
- `web/src/components/inbox/ChannelBadge.tsx` (new)
- `web/src/components/inbox/ChannelFilter.tsx` (new)

**Reviewer mindset:** chunk 5 is the user-visible surface. What surprises a CS agent at 09:01 Monday?

---

## Findings

### P0 / P1 — none unresolved

### P1 — caught during build

| # | Finding | Resolution |
|---|---|---|
| F1 | **REST adapter `listConversations` was IGNORING `?status=` query param entirely** (predates E3 — pre-existing bug). Existing tests didn't exercise it because the frontend passes status as part of `qs(params)`. Caught while extending the same handler for channel. | Added inline status parsing alongside channel. Otherwise the new channel filter would have shipped via REST while status remained silently dropped. Documented as bonus pre-existing fix in commit message. |

### P2 — Accepted with documented trade-off

| # | Finding | Severity | Resolution |
|---|---|---|---|
| F2 | **Channel filter state is local to the page component.** Navigate away → state reset. | P2 — E3.5-G1 carries it. URL search-param fix is a 1-line follow-up. |
| F3 | **`ChannelFilter` "All" represents `undefined`, not `INBOX_CHANNEL_UNSPECIFIED`.** Type sig `value: InboxChannel | undefined`. | Intentional — qs() drops undefined keys so the server gets no `channel` param at all, which maps to UNSPECIFIED server-side. Clean wire shape. |
| F4 | **`handleMbsNewMessage` finds conv by `(uid, threadId)` linear scan.** O(n) per WS event. | P2 — at <500 conversations per workspace this is microseconds. Map index would be premature. |
| F5 | **`handleMbsNewMessage` synthesizes Message with `id: payload.mid`.** If gateway also delivers `new_message` for the same MBS row in the future, the IDs would collide and dedupe wouldn't kick in. | P2 — today gateway only sends `mbs_new_message` for MBS (separate subjects). Documented constraint: removing the frontend bridge if gateway ever fans MBS into `new_message`. |
| F6 | **`handleMbsOutboundStatus` matches by `mbsMid`.** If the inbox-service outbound consumer (chunk 4) hasn't run yet, the local row's `mbsMid` is empty → no match. UI stays in 'pending' until next list refetch. | P2 — accepted as best-effort. The chunk-4 SetMbsMID + next-tick refetch reconciles. ~30s lag worst case. Documented as E3.5-G3. |
| F7 | **Channel filter chips don't expose UNSPECIFIED explicitly.** "All" is the only non-channel option. | Intentional — UI grammar maps 1:1 to user intent. |
| F8 | **Composer media gating: there is no media composer today.** | F5 in plan was speculative; current Inbox composer is text-only. Plan documented but no code change. Accepted as no-op. |
| F9 | **Drawer MBS metadata shows last-8 of threadId.** If the operator needs the full ID for debugging, the slice elides it. | P2 — pragmatic UX; the full ID is logged on backend. Could add a tooltip in F. |
| F10 | **Status query param parsing only knows 3 status values.** If proto adds a 4th, the REST handler silently drops it. | P2 — same fragility as before chunk 5 (preexisting bug F1). Refactor to use the proto-stub's String() lookup is a future cleanup. |
| F11 | **`InboxChannel` value enum exported but Inbox.tsx aliases it as `InboxChan` to avoid collision with the type-only import.** | Defensive — TypeScript ambiguity bites without the alias. |
| F12 | **`ChannelBadge` only renders for MBS; WA gets no badge.** | Intentional — WA is the default, badge would be noise. Sam's preference per E2 review pattern. |
| F13 | **WS bridge fires `inbox.handleMbsNewMessage` even when the Inbox page is not mounted.** Zustand still updates `useInboxStore.conversations` state. | P2 — that's the point. State stays warm so when the user navigates back, the list already reflects recent activity. |
| F14 | **`gen/` regenerated and gitignored.** Anyone running CI without `make proto-gen` will hit "undefined: hermesv1.InboxChannel_INBOX_CHANNEL_*" on this commit. | Same model as chunk 4. CI must run `make proto-gen` (or buf generate) on PRs. Documented. |
| F15 | **`handler_test.go` mock store signature accepts 7 strings (channel position).** Pre-existing chunk-2 mock; no change needed. | Verified by `TestListConversations_ChannelFilter` passing — mock captures the new channelArg correctly. |
| F16 | **`InboxChannel.WA` and `INBOX_CHANNEL_WA` look like distinct values to a tired reader.** They're the same string (`'INBOX_CHANNEL_WA'`) thanks to the as-const enum pattern. | Intentional convention from prior chunks. |

### P3 / FP

| # | Finding | Status |
|---|---|---|
| F17 | "Could a malicious channel value break SQL?" | FP — Store iface accepts string and the chunk-2 SQL uses parameterized query. Even if "; DROP TABLE" arrived, pgx parameterizes. |
| F18 | "What if WS reconnect floods `handleMbsNewMessage` with duplicates?" | FP — Zustand state map is idempotent: same conversation update overwrites. Only a refetch-and-merge bug could double-count unread. |
| F19 | "Channel filter on `status=closed` tab?" | FP — channel filter is orthogonal to status. Both apply server-side. |

---

## Carrying gaps (cumulative)

| # | Gap | Severity | Plan |
|---|---|---|---|
| **E3.5-G1** | Channel filter not URL-persisted. | L | future. |
| **E3.5-G3** | WS outbound status bridge best-effort. | L | future. |
| **E3.4-G1, G2, G3** | (carried) MBS media, NATS publish retry, live DB smoke. |  |  |
| **E3.3-G1, G2, G3** | (carried) ON CONFLICT smoke, multi-workspace, notify rate. |  |  |
| **E3.2-G1** | (carried) FindOrCreateMbsConversation partial-idx race. |  |  |

---

## Gates verified

```
PATH=~/go/bin:$PATH buf generate           ✓ (regens gen/)
go vet ./internal/inbox/... ./internal/gateway/...  ✓
go build ./...                              ✓
go test -race -count=1 ./internal/inbox/... ./internal/gateway/...  ✓ all green
  ok  github.com/hermes-waba/hermes/internal/inbox/handler   1.134s
  ok  github.com/hermes-waba/hermes/internal/gateway/handler 10.190s
  ok  github.com/hermes-waba/hermes/internal/gateway/middleware 1.219s
  ok  github.com/hermes-waba/hermes/internal/gateway/rest    1.256s
  ok  github.com/hermes-waba/hermes/internal/gateway/websocket 1.101s
cd web && npx tsc --noEmit                  ✓
cd web && npm run build                     ✓ (672.79 kB bundle, +1KB from chunk 4)
```

3 new sub-tests on backend; existing tests unchanged.

---

## Verdict

**Ready to commit.** Chunk 5 is the smallest E3 chunk by LOC and the most user-visible. Backend changes are trivially additive (one new enum field, one new helper function, one new test). Frontend changes are concentrated in three places: list filter + badge + drawer header. The two new tiny components are reusable for any future channel surface. The WS bridge is the only piece that needs explanation — it's intentionally best-effort and the inbox-service outbound consumer (chunk 4) does the canonical state reconciliation.

**Stage E3 backend + frontend feature-complete.** All 5 chunks shipped.

— Oracle, 2026-05-29
