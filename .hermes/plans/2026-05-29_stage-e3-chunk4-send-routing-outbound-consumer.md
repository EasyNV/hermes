# Stage E3 chunk 4 — channel-aware `InboxSendMessage` + MBS outbound consumer

**Date:** 2026-05-29
**Author:** Oracle
**Builds on:** E3 chunks 1–3 (`cea5974`, `1aa234d`, `ef10e53`)
**Effort:** ~3h
**Target LOC:** ~300

---

## Goal

Two halves of the same axis:

1. **Outbound send routing.** `InboxSendMessage` reads the conversation's `channel` and routes:
   - `channel='wa'` (or empty, default) → current WA path (`hermes.wa.send.manual.<tenant>` + `ManualSendTask`).
   - `channel='mbs'` → MBS path (`hermes.mbs.send.manual.<tenant>` + `MbsCampaignSendTask` with `campaign_id=""`).
2. **Outbound status reconciliation.** Subscribe `hermes.mbs.message.outbound.*` and update the message row's status by `mbs_mid`. Mirror the existing WA `startOutboundConsumer`.

End-state: the existing inbox composer in the web UI works for MBS conversations once chunk 5 makes them visible. Send → message row written (PENDING) → NATS work item published → `hermes-mbs` does the actual send (already wired) → outbound event lands on `hermes.mbs.message.outbound.<tenant>` with the real `mid` and `ok`/`error` → inbox-service updates the row to SENT or FAILED → gateway broadcasts `mbs_outbound_status` (already wired in E2-C3) → UI reconciles.

---

## Non-goals (deferred to chunk 5)

- Frontend channel surface — `Conversation` TS type extension, channel filter, badge.
- The "merge `mbs_new_message` into inbox new_message" path — chunk 5 frontend bridge.
- Outbound media (attachments). MBS today only supports text via `MbsCampaignSendTask.resolved_body`.

---

## Contracts

### C4-K1 — `InboxSendMessage` channel branch

Single new code path in `Handler.SendMessage`. Read `conv.Channel`. Branch:

```go
switch conv.Channel {
case "", "wa":
    // existing WA path — unchanged
case "mbs":
    h.publishMbsManualSend(ctx, conv, msg, req)
default:
    return nil, status.Errorf(codes.Internal, "unknown channel %q on conv %s", conv.Channel, conv.ID)
}
```

**Important constraint:** the existing WA path creates the message with `CreateMessage(... wa_message_id="")`. For MBS we must call `CreateMbsMessage(conv.ID, "outbound", req.Body, "")` (empty mid — Meta assigns it). BUT the chunk-2/3 `CreateMbsMessage` REQUIRES a non-empty `mbsMID`. **This is broken for the outbound path.** Need to either (a) relax the constraint, or (b) issue the outbound message via `CreateMessage` with `wa_message_id=""` and patch `UpdateMbsMessageStatus` to also UPDATE `mbs_mid` on first sight from the outbound event.

**Adopted:** Path (a). Relax `CreateMbsMessage` to allow empty mid for outbound; the outbound consumer then patches `mbs_mid` on the row via `idempotency_key=msg.ID` lookup. See C4-K3.

### C4-K2 — `MbsCampaignSendTask` synthesis

Per existing MBS receiver design (`cmd/mbs/send_consumers.go:84-88`): manual sends use `MbsCampaignSendTask` with `campaign_id=""`. We synthesize:

```go
task := &hermesv1.MbsCampaignSendTask{
    Meta: &hermesv1.EventMeta{
        EventId:   eventID,
        TenantId:  tenantID, // resolved via GetMbsTenantForConversation (see C4-K5)
        Timestamp: timestamppb.Now(),
        Source:    "hermes-inbox",
    },
    CampaignId:     "",
    ContactId:      conv.ContactID,
    Uid:            uidInt64,           // parsed from conv.MbsSessionUID
    ThreadId:       conv.MbsThreadID,   // pre-resolved, no phone fallback
    RecipientPhone: "",
    ResolvedBody:   req.Body,
    PageIdOverride: conv.MbsPageID,     // multi-page session
    IdempotencyKey: msg.ID,             // local DB row id is the dedupe key
}
```

Subject: `hermes.mbs.send.manual.<tenantID>`. NATS PubOpt: `MsgId(eventID)`.

### C4-K3 — Allow empty MID in `CreateMbsMessage`

Currently:

```go
if mbsMID == "" {
    return nil, fmt.Errorf("CreateMbsMessage: mbsMID required")
}
```

For outbound, the local row is created BEFORE Meta hands back a MID. Relax: empty MID is allowed for `direction="outbound"`. The ON CONFLICT in chunk-3's SQL guards `WHERE mbs_mid != ''` so empty-MID rows can never conflict. **No schema change needed.**

```go
if direction == "inbound" && mbsMID == "" {
    return nil, fmt.Errorf("CreateMbsMessage: mbsMID required for inbound")
}
```

The outbound consumer then patches `mbs_mid` when the real MID arrives — see C4-K4.

### C4-K4 — `Store.SetMbsMID` (new method)

For the outbound consumer to attach the real MID to the row created by `CreateMbsMessage`. Lookup is by `idempotency_key=msg.ID` carried in `MbsOutboundEvent.Otid`. mautrix-meta and our MBS publisher both call client-side identifier `otid`. We piggy-back: `otid = msg.ID`.

```go
// SetMbsMID stamps the Meta-assigned MID on a row identified by its
// local UUID (carried as the otid through the campaign task → outbound
// event round-trip). Idempotent: a re-delivered event setting the same
// MID is a no-op.
SetMbsMID(ctx context.Context, messageID, mbsMID string) error
```

Impl:

```sql
UPDATE messages SET mbs_mid = $2 WHERE id = $1 AND (mbs_mid = '' OR mbs_mid = $2)
```

WHERE clause makes it idempotent and prevents accidental MID overwrites.

### C4-K5 — `Store.GetMbsTenantForUID` (new method)

Needed by `SendMessage` to look up the tenant for an MBS conversation's `mbs_session_uid` BEFORE publishing the manual task. `GetWorkspaceIDForMbsUid` already returns tenant; we add a thinner method that returns only tenant_id to avoid wasting workspace lookup in send-path:

Actually — simpler: just reuse `GetWorkspaceIDForMbsUid`. The discard cost is negligible. **No new method.**

### C4-K6 — Outbound consumer

```go
func startMbsOutboundConsumer(js natsgo.JetStreamContext, store handler.Store, log zerolog.Logger) error
```

Mirrors `startOutboundConsumer` (WA). Subject: `hermes.mbs.message.outbound.*`. Durable: `inbox-mbs-outbound`.

Per message:

1. Unmarshal `MbsOutboundEvent`.
2. If `ev.Mid != ""` and `ev.Otid != ""`: call `store.SetMbsMID(ctx, ev.Otid, ev.Mid)`. (First sight after Meta assigns.) Errors → log + continue.
3. Translate `ok`/`error` → status string:
   - `ok=true` → `"sent"`
   - `ok=false` → `"failed"`
4. If `ev.Mid != ""`:
   - look up message by mid via `GetMessageByMbsMID`
   - apply forward-transition guard via `IsForwardTransition`
   - call `UpdateMbsMessageStatus(ev.Mid, newStatus)`
5. If `ev.Mid == ""` (failure before Meta assigned mid):
   - look up by `ev.Otid` (= local msg.ID) directly via a new helper. Actually simpler: extend `UpdateMbsMessageStatus` to accept either MID or otid. Simpler still: when MID is empty + ok=false, run `UPDATE messages SET status='failed' WHERE id = $otid AND status='pending'`.

**Adopted:** add a focused `MarkOutboundFailedByOtid(ctx, otid)` method. Keeps the two paths cleanly separated.

```go
MarkOutboundFailedByOtid(ctx context.Context, otid string) error
```

Impl:

```sql
UPDATE messages SET status = 'failed'
WHERE id = $1
  AND direction = 'outbound'
  AND status IN ('pending', 'sent')   -- forward-only
```

### C4-K7 — `MbsOutboundEvent.ClientDedupeId` correlation

**[Updated 2026-05-29 after build-time verification of mbs-native send pipeline.]**

The original plan was to piggy-back on `MbsOutboundEvent.Otid`. **Won't work** —
`result.OTID` is generated inside `mbs-native/client/send.go::generateOTID()`, not
echoed from the caller's `IdempotencyKey`. The mbs-native client mints its own
OTID per send (it goes into the wire frame for Meta-side delta correlation).

**Adopted:** add `client_dedupe_id` field to `MbsOutboundEvent`. The mbs send
handler (`internal/mbs/handler/rpc_send_message.go`) already receives
`req.ClientDedupeId` (bytes); we just need to thread it into the existing
`PublishOutbound` call and surface it on the event.

Changes required outside `internal/inbox`:

1. `proto/hermes/v1/events.proto` — add `bytes client_dedupe_id = 10;` to
   `MbsOutboundEvent`. Append-only — preserves wire compat.
2. `docs/contracts/proto/events.proto` — mirror.
3. `internal/mbs/handler/events.go` — `PublishOutbound` adds a `clientDedupeID []byte` param;
   the natsEventPublisher includes it in the marshalled event.
4. `internal/mbs/handler/rpc_send_message.go` — pass `req.ClientDedupeId` into
   both `PublishOutbound` call sites (success + failure).
5. Test publishers in `bridge/integration_test.go`, `refresh/attempt_test.go`,
   `importer/importer_test.go`, `rpc_session_lifecycle_test.go` — extend `PublishOutbound`
   signature.
6. Regenerate proto via `make proto-gen` (or hand-write in `gen/` since it's
   gitignored).

`inbox-service` uses `event.GetClientDedupeId()` as the correlation key.

Plan revisions:

- `Store.SetMbsMID(messageID, mbsMID)` keys on `messageID = string(client_dedupe_id)`.
  inbox-service publishes the task with `IdempotencyKey = msg.ID`; the mbs handler
  passes that through as `ClientDedupeId`; the outbound publisher includes it; we
  decode it back to a UUID string when correlating.
- `Store.MarkOutboundFailedByOtid` is renamed to `MarkOutboundFailedByID` — same
  semantics, but accepts `messageID = client_dedupe_id` directly. (Name simplifies
  reasoning: we never use the actual otid.)

This expands chunk 4 scope by ~6 small edits across mbs + proto. Decision: **fold
into chunk 4** rather than splitting because the correlation is fundamental to the
status reconciliation feature. Without it, the outbound consumer cannot find rows
to update.

The original "C4-K7 — `MbsOutboundEvent.Otid` provenance" assumption was wrong.
Caught during build-time verification (see C4-P2 resolved below).

### C4-K8 — Error semantics

| Error | Action |
|---|---|
| Unknown channel on conv | `codes.Internal` (publisher data corruption — alert) |
| Tenant lookup miss for MBS conv | `codes.FailedPrecondition` (session burned, can't send) |
| NATS publish failure | log error, return success to caller (message row persists; cron may retry — TODO out of scope) |
| MBS outbound event w/ unknown mid+otid | log + ACK (race: event arrived before SendMessage finished, or for a campaign send not in inbox) |
| `IsForwardTransition` rejection | ACK, no DB write (idempotency) |

---

## Files touched

| # | File | Change | LOC |
|---|---|---|---|
| 1 | `internal/inbox/handler/store.go` | Relax `CreateMbsMessage` empty-MID guard; add `SetMbsMID` + `MarkOutboundFailedByOtid` to iface + PgStore | ~50 |
| 2 | `internal/inbox/handler/handler_test.go` | mockStore extension for 2 new methods | ~20 |
| 3 | `internal/inbox/handler/handler.go` | Channel-aware branch in `SendMessage`; new `publishMbsManualSend` helper | ~80 |
| 4 | `internal/inbox/handler/handler_send_mbs_test.go` (new) | Cover routing, missing tenant, NATS publish capture | ~150 |
| 5 | `cmd/inbox/main.go` | `startMbsOutboundConsumer` + wire from main + extract `processMbsOutbound` for testability | ~140 |
| 6 | `cmd/inbox/main_mbs_outbound_test.go` (new) | 6 scenarios | ~280 |

---

## Implementation steps

### Step 1 — Store layer

`CreateMbsMessage` relax + 2 new methods.

### Step 2 — mockStore + handler.Store iface

Append in deterministic order.

### Step 3 — `SendMessage` channel branch

Read `conv.Channel`, dispatch. Move WA-specific code into `publishWaManualSend` helper for symmetry; refactoring step but keeps the diff readable.

Actually — minimize change surface. Leave WA path inline; add MBS path side-by-side.

### Step 4 — Handler tests (new file)

```
internal/inbox/handler/handler_send_mbs_test.go
```

Five scenarios:

1. MBS conv → publish to `hermes.mbs.send.manual.<tenant>` with `MbsCampaignSendTask{campaign_id="", uid, thread_id, page_id, body, idempotency_key=msg.ID}`.
2. WA conv → publish to `hermes.wa.send.manual.<tenant>` (regression — verify chunk 4 didn't break it).
3. Unknown channel → `codes.Internal`.
4. MBS conv + missing session (no row for uid in mbs_sessions) → `codes.FailedPrecondition`.
5. MBS conv + non-numeric `MbsSessionUID` (should never happen — defensive) → `codes.Internal`.

### Step 5 — Outbound consumer

Extract `processMbsOutbound(ctx, store, log, *MbsOutboundEvent) (ack bool)` per chunk-3 pattern.

### Step 6 — Outbound consumer tests (new file)

```
cmd/inbox/main_mbs_outbound_test.go
```

Six scenarios:

1. First outbound event (ok=true, mid set) → `SetMbsMID` called with (otid, mid), `UpdateMbsMessageStatus(mid, "sent")` called.
2. Failure before Meta assigned mid (ok=false, mid=="") → `MarkOutboundFailedByOtid(otid)` called.
3. Failure after mid assigned (ok=false, mid set) → both calls (SetMbsMID still safe, then status=failed).
4. Duplicate delivery (idempotency) → no-op via row-not-found fallthrough.
5. Empty otid + empty mid → ack with warning, no DB writes.
6. Forward-transition guard: existing status="sent", new=="pending" → no UpdateMbsMessageStatus call.

### Step 7 — Wire from main()

```go
if err := startMbsOutboundConsumer(js, store, log); err != nil {
    log.Fatal().Err(err).Msg("failed to start MBS outbound consumer")
}
```

---

## Verification gates

```
go vet  ./cmd/inbox/... ./internal/inbox/...
go build ./...
go test -race -count=3 ./cmd/inbox/... ./internal/inbox/...
```

All must pass before commit. Existing 30+ tests + 8 chunk-3 tests must remain green.

---

## Hostile-eyes pre-audit (caught at plan stage)

| # | Issue | Resolution |
|---|---|---|
| C4-P1 | **CreateMbsMessage rejects empty MID — outbound path can't create rows.** Caught reading chunk-2 impl. | Relax to `if direction == "inbound" && mid == ""` — fold into chunk-4 store edit. |
| C4-P2 | **otid round-trip through MBS sender not verified — but proven wrong.** Build-time check showed `result.OTID` is mbs-native-generated, not echoed from `IdempotencyKey`. Replanned: add `client_dedupe_id` field to `MbsOutboundEvent`, thread through. | Resolved by expanding chunk scope per C4-K7 (revised). |
| C4-P3 | **NATS PubAck failure on send-path = silent dropped reply.** UI sees the row in pending forever. | Defer cron-retry to F. Log loudly. Acceptable for MVP — operator can re-click. |
| C4-P4 | **`MarkOutboundFailedByOtid` against status='sent' is destructive.** Forward-transition prevents this but the SQL has it in the allowed set. | Intentional: a publisher race could deliver a late "failed" after an early "sent" — but that's actually backwards transition. Tighten the WHERE: `status IN ('pending')`. UPDATE 1 row → success; 0 rows → silently OK (idempotent re-delivery / out-of-order). |
| C4-P5 | **Channel branch in SendMessage = combinatorial blast with content_type.** WA supports text/image/document/audio/video; MBS today is text-only. Non-text MBS sends should error early. | Add validation: `if conv.Channel == "mbs" && req.ContentType != CONTENT_TYPE_TEXT { codes.Unimplemented }`. Documented as carrying gap. |
| C4-P6 | **`processMbsOutbound` runs in same consumer as send. A long DB UPDATE blocks the consumer.** Same constraint as WA path. | Inherited. AckWait 30s. |
| C4-P7 | **`GetWorkspaceIDForMbsUid` runs on every MBS send to resolve tenant.** N queries per N sends. | Acceptable — single connection pool, in-process JOIN, no network. Per-tenant cache deferred to F. |
| C4-P8 | **The `conv.Status == "closed"` reopen path isn't exercised on outbound.** Inbound reopens; outbound currently doesn't. WA path also doesn't. | Inherited. Out of scope. |
| C4-P9 | **Forward-transition guard imported.** We import `internal/inbox/conversation`; need to check that import already exists in this file. | Yes — already imported via `conversation.StatusAfterInbound`. |
| C4-P10 | **Outbound event arrives BEFORE SendMessage commits the row.** Possible if NATS is faster than the local DB write. Then `SetMbsMID` finds no row, `UpdateMbsMessageStatus` finds no row → silent miss. | NATS redelivery + 30s AckWait gives the DB time to catch up. If both methods return ErrNotFound, NAK with `MaxDeliver=5`. After 5 retries it's a real bug, not a race. |

---

## Open issues / carrying gaps

| # | Gap | Severity | Owner | Resolution path |
|---|---|---|---|---|
| **E3.4-G1** | MBS outbound media (image/doc/audio/video) not supported. Send validation rejects non-text. | M | Deferred | F or later. Add `MbsCampaignSendTask.media_url` field; wire mbs-native send path. |
| **E3.4-G2** | NATS publish failure on send-path = silently retained pending row. | L | Deferred | F: cron-driven re-publish for messages stuck in pending > 60s. |
| **E3.4-G3** | otid round-trip integrity. | M | This chunk verifies; failure = stop. | If broken, may require an inbox-service side correlation table mapping eventID → msg.ID. |
| **E3.4-G4** | Per-tenant cache for GetWorkspaceIDForMbsUid lookups. | L | Deferred | F. |

---

## Files touched (final shape)

- `internal/inbox/handler/store.go` — relax `CreateMbsMessage` empty-MID guard for outbound; add `SetMbsMID` and `MarkOutboundFailedByOtid`.
- `internal/inbox/handler/handler_test.go` — mockStore extension.
- `internal/inbox/handler/handler.go` — channel branch in SendMessage; `publishMbsManualSend` helper.
- `internal/inbox/handler/handler_send_mbs_test.go` (new) — 5 routing tests.
- `cmd/inbox/main.go` — `startMbsOutboundConsumer` + `processMbsOutbound` + wire from main.
- `cmd/inbox/main_mbs_outbound_test.go` (new) — 6 outbound scenarios.

---

## Commit message

```
inbox(send+outbound): channel-aware SendMessage + MBS outbound consumer (Stage E3 chunk 4)

SendMessage reads conversation.channel and routes:
- channel='wa'/'' → existing WA path (unchanged)
- channel='mbs'  → publish MbsCampaignSendTask (campaign_id="") to
                    hermes.mbs.send.manual.<tenant> with the local
                    msg.ID as idempotency_key/otid

startMbsOutboundConsumer subscribes hermes.mbs.message.outbound.*:
- Stamps mbs_mid on the row via SetMbsMID(otid, mid) (idempotent
  UPDATE matching empty-or-same).
- Applies forward-transition guard and UpdateMbsMessageStatus(mid,
  "sent"/"failed").
- If failure arrives before Meta assigned mid (mid==""), uses
  MarkOutboundFailedByOtid(otid) to flip the row from pending → failed.

Store layer:
- Relax CreateMbsMessage empty-MID guard (outbound path needs it).
- Add SetMbsMID and MarkOutboundFailedByOtid.

Validation: MBS sends are text-only for now; non-text content type
returns Unimplemented. (E3.4-G1 carries this gap.)

E3.4-G2: NATS publish failure on send-path is logged but not retried.
E3.4-G3: otid round-trip verified at chunk build time.

Refs: .hermes/plans/2026-05-29_stage-e3-chunk4-send-routing-outbound-consumer.md
```

— Oracle, 2026-05-29
