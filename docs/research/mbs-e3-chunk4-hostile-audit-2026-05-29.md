# Hostile-eyes audit — Stage E3 chunk 4 (channel-aware SendMessage + MBS outbound consumer)

**Date:** 2026-05-29
**Scope:** All code changes in commit-pending state for chunk 4:
- `proto/hermes/v1/events.proto` + `docs/contracts/proto/events.proto` (+ `gen/` regen)
- `internal/mbs/handler/events.go`, `rpc_send_message.go` (PublishOutbound sig + impl)
- 5 test publishers extended (`events_test.go`, `rpc_session_lifecycle_test.go`, `importer/importer_test.go`, `refresh/attempt_test.go`, `bridge/integration_test.go`)
- `internal/inbox/handler/store.go` (CreateMbsMessage relax + SetMbsMID + MarkOutboundFailedByID)
- `internal/inbox/handler/handler.go` (channel dispatch + sendMessageMbs)
- `internal/inbox/handler/handler_test.go` (mockStore extension)
- `internal/inbox/handler/handler_send_mbs_test.go` (new — 8 tests)
- `cmd/inbox/main.go` (startMbsOutboundConsumer + processMbsOutbound + wire)
- `cmd/inbox/main_mbs_test.go` (fakeStore extension)
- `cmd/inbox/main_mbs_outbound_test.go` (new — 7 tests)

**Reviewer mindset:** chunk-4 is the routing brain. A wrong correlation key means every MBS message stays pending forever. What could go wrong?

---

## Findings

### P0 / P1 — none unresolved

### Resolved at plan stage (would have been P1)

| # | Finding | Resolution |
|---|---|---|
| F0 | **`result.OTID` is mbs-native-generated, not echoed from caller's IdempotencyKey.** Original plan correlated by Otid → would have left every row in pending forever. | Caught at build-time verification (cmd/mbs/send_consumers.go + mbs-native/client/send.go). Resolved by adding `client_dedupe_id` field to `MbsOutboundEvent`, threading through. Chunk now ships proto + 6 file edits to support correlation. |

### P2 — Accepted with documented trade-off

| # | Finding | Severity | Resolution |
|---|---|---|---|
| F1 | **`PublishOutbound` signature grew.** Backward-incompatible for any external implementer of `EventPublisher`. | P2 | Internal interface; all 6 impls updated in same commit. Test impls extended via search-and-replace. |
| F2 | **`client_dedupe_id` as `bytes` not `string`.** MbsSendMessageRequest already uses bytes; consistent. Inbox stamps it as `[]byte(msg.ID)`. | P2 | Intentional — matches existing field type. Round-trip via `string(event.GetClientDedupeId())`. UUIDs are ASCII-safe so byte↔string is lossless. |
| F3 | **`CreateMbsMessage` empty-mid relax.** Inbound path previously rejected; now only rejects when `direction=="inbound"`. | P2 | Required for outbound. Inbound publisher always sets `mid`. |
| F4 | **`SendMessage` channel branch loads conversation TWICE in WA path** (once for dispatch, then the existing code below ALSO did `h.store.GetConversation`). Refactored to share. | P1 (caught self-review) | Fixed inline: `conv, _ := h.store.GetConversation` removed from WA branch since the early load already populated it. |
| F5 | **`sendMessageMbs` rejects non-UNSPECIFIED non-TEXT.** A frontend that doesn't set content_type at all (UNSPECIFIED) still works. | P2 | Intentional — preserves default behavior. |
| F6 | **`getMessageByMbsMID` is called even after `SetMbsMID` returns success.** Two queries per success-path event. | P2 | The transition guard requires the existing status, which `SetMbsMID` doesn't return. Acceptable given the volume (1 query per delivered message). F-stage could fold into a single UPSERT-with-RETURNING. |
| F7 | **`MarkOutboundFailedByID` updates only rows in 'pending'.** If a row is already 'sent' and a late failure arrives, we silently ignore. | P2 | Intentional — forward-only semantics. Late failure after sent is a publisher bug. |
| F8 | **NATS publish failure on send-path leaves a pending row.** Logged loudly; no cron retry. | P2 | E3.4-G2 carries this. F deploys a cron sweep. |
| F9 | **`mockStore.SetMbsMID`/`MarkOutboundFailedByID` default to nil → returns nil.** A test that forgets to wire the hook silently passes. | P2 | Convention — same as existing mockStore methods. Test that asserts the call is mandatory must check capture. |
| F10 | **Tests use a `fullJS` shim that embeds `natsgo.JetStreamContext`.** Calls to unimplemented methods would nil-panic. | P2 | All test paths only call Publish. Defensive but not exercised. |
| F11 | **`processMbsOutbound` doesn't validate event.Uid or thread_id.** Could be garbage. | P2 | We don't write either to the row; only mid + status. Correlation key alone is sufficient. |
| F12 | **`GetMessageByMbsMID` returns ErrNotFound on success path → ACK and drop.** If NATS race makes the event arrive before the inbox-service finished the local INSERT, we silently miss the status update. | P2 | NATS redelivery (5x, 30s ackWait) gives time for the local DB to commit. After 5 retries it's a real bug, not a race. Worst case: row stays in 'pending'; user re-sends. |
| F13 | **Outbound consumer doesn't republish a WS frame.** Gateway already handles that via the parallel subscription on `hermes.mbs.message.outbound.*` (E2-C3). | P2 | By design — single producer, two consumers. |
| F14 | **`sendMessageMbs` uses `strconv.ParseInt`.** Negative MbsSessionUID would compile but the DB's BIGINT could in theory hold it. | P2 | Defensive — MBS uids are always positive Meta-issued. |
| F15 | **`handler_send_mbs_test.go` has unused `stubJS` type kept via `var _ = stubJS{}`** suppressor. | P2 | Vestigial from initial scaffold. Acceptable — won't accumulate. |
| F16 | **`cmd/mbs/send_consumers.go` already converts `IdempotencyKey` (string) → `ClientDedupeId` ([]byte) via `[]byte(...)`.** Our chunk passes string UUID through bytes → string round-trip — lossless for UUID format. | P2 | Verified via test `TestSendMessage_MbsChannel_PublishesToMbsManualSubject`. |

### P3 / FP

| # | Finding | Status |
|---|---|---|
| F17 | "Could ClientDedupeId be > UUID length?" | FP — set by inbox-service to `msg.ID` (UUID, 36 chars). Bound by Postgres TEXT id. |
| F18 | "Goroutine leak on NATS subscribe." | FP — same model as WA outbound consumer. nc.Drain handles shutdown. |
| F19 | "Could `event.Ok=false` + `event.Mid != ""` happen?" | FP — mbs-native send pipeline only emits ok=true when result.MID is set. ok=false with mid set would only happen if Meta delivered then failed asynchronously — rare but the code handles it. |

---

## Carrying gaps (cumulative)

| # | Gap | Severity | Plan |
|---|---|---|---|
| **E3.4-G1** | MBS outbound media not supported. | M | F or later — extend `MbsCampaignSendTask` with media URL, wire mbs-native send. |
| **E3.4-G2** | NATS publish failure on send-path = stuck pending row. | L | F: cron sweep for pending > 60s. |
| **E3.4-G3** | otid round-trip integrity is verified by test; no live-DB smoke yet. | L | F deploy gate. |
| **E3.3-G1** | (carried) `CreateMbsMessage` ON CONFLICT live-DB smoke. | M | F deploy gate. |
| **E3.3-G2** | (carried) multi-workspace tenant routing picks oldest. | L | future. |
| **E3.2-G1** | (carried) `FindOrCreateMbsConversation` partial-index race smoke. | M | F deploy gate. |

---

## Gates verified

```
go vet  ./cmd/inbox/... ./internal/inbox/... ./internal/mbs/...  ✓ (pre-existing listener_hook_test.go finding unchanged)
go build ./...                                                    ✓
go test  -race -count=2 ./cmd/inbox/... ./internal/inbox/...     ✓ all green
go test  -race -count=1 ./internal/mbs/... ./internal/gateway/... ✓ all green
```

8 new handler tests + 7 new outbound consumer tests pass. Existing 30+ tests unchanged.

---

## Verdict

**Ready to commit.** The chunk's hardest discovery (otid → client_dedupe_id pivot) was caught at plan-stage verification and resolved within the same chunk — exactly the value of the plan-first rule. Single P1 self-review finding (F4 — double GetConversation) caught and fixed during build. All other findings either resolved or documented as carrying gaps.

— Oracle, 2026-05-29
