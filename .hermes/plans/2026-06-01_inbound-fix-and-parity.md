# Inbound Fix + Parity — Plan / Spec / Contracts

**Date:** 2026-06-01
**Author:** Oracle
**Status:** approved (Sam green-lit I1-I3, CTL-G1, CTL-G2, CTL-G3)
**Predecessor:** `2026-06-01_close-the-loop-mbs-campaign-delivery.md` (commit 4e6cb19)

---

## Problem statement

Two live defects surfaced during the close-the-loop re-test:

1. **Inbound replies never reach the inbox.** A recipient replied to an MBS
   campaign message; the reply never appeared in the agent inbox. Root-caused to
   two independent breaks in the MBS inbound pipeline (Bug A + Bug B below).
2. **Three carried gaps (CTL-G1/G2/G3)** documented at the end of the
   close-the-loop chunk, now scheduled.

### Root cause — inbound (verified)

**Bug A — inbound publisher is dead code (nil hook).**
`cmd/mbs/main.go:165` constructs `session.NewManager(...)` **without `OnDelta`**.
The per-uid listener (`internal/mbs/session/listener.go`) polls Meta every 10s
(`SnapshotPoll`), parses deltas, and calls `fireHook(d)` → `onDelta == nil` →
returns. Every inbound delta is dropped. Confirming evidence: `handler.
PublishInboundMessage` (the NATS publish for `hermes.mbs.message.inbound.*`) has
**zero production callers** (grep: only `events_test.go` + the nop stub). The
listener and the publisher were built in separate chunks and never wired
together. Inbound has never worked for any message.

**Bug B — listener not resident (orphan-on-restart).**
The listener only exists while a session is loaded in the manager. At boot,
`reconnectPodSessions` (`cmd/mbs/reconnect.go:43`) reclaims sessions with
`ListSessionsByPod(podID, "active")` → `WHERE pod_id = $1 AND state='active'`.
But graceful shutdown (`manager.Shutdown` → `Disconnect` → `ReleaseSession`)
resets `pod_id` to `''`. After every restart the pod cannot reclaim its own
sessions — reconnect logged `count:0`. The active session `61590752691262` is
`state='active'` but `pod_id=''` (verified in DB). When the reply arrived, no
listener was polling. Even with Bug A fixed, Bug B means nobody was listening.

The **downstream is fully built and healthy**: `inbox-mbs-inbound` consumer and
`gateway-mbs-inbound` WS subscriber have been subscribed since 2026-05-30. The
break is entirely at the source (hop one).

---

## Scope split

| Unit | Items | Surface | Proto? | Services deployed |
|---|---|---|---|---|
| **A** (this build) | I1, I2, I3, G2, G3 | mbs + campaign | No | hermes-mbs, hermes-campaign |
| **B** (follow-up) | G1 | wa + campaign + proto | Yes | hermes-wa, hermes-campaign |

Rationale: keep proto regen + a third-service (`hermes-wa`) bounce off the back
of the urgent inbound hotfix. Both units are fully specced here.

---

# UNIT A — Inbound fix + MBS-side parity

## I1 — Wire `OnDelta` → `PublishInboundMessage`

**File:** `cmd/mbs/main.go`

The manager already has the NATS publisher available (`pub`, line 162). Pass an
`OnDelta` closure into `session.NewManager` that maps an `InboundDelta` to
`pub.PublishInboundMessage`. This single wire kills both the nil-hook drop and
the dead-code problem.

```go
mgr := session.NewManager(session.Opts{
    Store:  st,
    DEK:    dek,
    PodID:  cfg.PodID,
    Logger: log,
    OnDelta: func(d *session.InboundDelta) {
        // Hook MUST NOT block (listener calls it inline before broadcast).
        // PublishInboundMessage is a fire-and-forget NATS publish — fast.
        // Only publish message deltas (Text present); skip receipt/presence
        // deltas that carry no body (Kind-based filter, see I2).
        if d == nil || d.Text == "" {
            return
        }
        pub.PublishInboundMessage(
            d.UID, d.TenantID, d.PageID, d.MailboxID, d.ThreadID,
            d.MID, d.SenderPhone, d.Text, d.MetaTimestamp,
        )
    },
})
```

**Spec decisions:**
- **Text-only gate.** `InboundDelta` covers receipts/presence (empty `Text`).
  The inbound *message* pipeline only wants messages. Filter on `Text != ""`.
  (Receipt→read-status for MBS is out of scope; documented gap.)
- **Non-blocking.** The listener invokes `onDelta` inline before broadcast
  (`listener.emit`). `PublishInboundMessage` is a bounded NATS publish; acceptable.
  Panics are already recovered by `fireHook`.

## I2 — Complete the delta payload (`PageID`, `MailboxID`)

**Files:** `internal/mbs/session/session.go` (struct), `internal/mbs/session/manager.go`
(connect → listener construction), `internal/mbs/session/listener.go` (stamp).

`InboundDelta` currently lacks `PageID` / `MailboxID`. `PublishInboundMessage`'s
signature requires `pageID, mailboxID`. The listener already stamps `TenantID`
from the session row in `emit`; extend the same pattern: the listener gets
`pageID`/`mailboxID` from the connected session's primary asset (`creds.PageID`,
`creds.WECMailboxID`) at construction time and stamps them onto each delta in
`emit`, exactly like `TenantID`.

```go
// session.go — InboundDelta
type InboundDelta struct {
    UID       int64
    TenantID  string
    PageID    string   // NEW: stamped by listener from session creds
    MailboxID string   // NEW: stamped by listener from session creds
    ThreadID  string
    ...
}
```

```go
// listener.go — listener gets pageID/mailboxID, stamps in emit()
type listener struct {
    uid       int64
    tenantID  string
    pageID    string   // NEW
    mailboxID string   // NEW
    ...
}

func (l *listener) emit(deltas []*InboundDelta) {
    for _, d := range deltas {
        if d == nil { continue }
        d.TenantID = l.tenantID
        d.PageID = l.pageID        // NEW
        d.MailboxID = l.mailboxID  // NEW
        l.fireHook(d)
        l.bc.dispatch(d)
    }
}
```

```go
// manager.go connect() — pass creds-derived IDs into newListener
go newListener(uid, row.TenantID, creds.PageID, creds.WECMailboxID,
    c, ms.bc, m.onDelta, m.log).run(lctx)
```

**Carried gap (documented, not fixed here):** `senderPhone` resolution. The poll
delta carries `SenderName`/`SenderURL` but `SenderPhone` is empty (chunk-3 left
it "filled by handler" — a handler this direct path bypasses). The inbox
synthesizes `mbs:thread:<id>` when phone is empty, so the message *lands*; the
real WA number won't show until thread→phone resolution is added. Separate
enhancement (tracked as INB-G1).

## I3 — Fix reconnect to reclaim orphaned sessions

**Files:** `internal/mbs/store/pg.go` (new query), `cmd/mbs/reconnect.go` (call site).

Reconnect must also reclaim sessions whose `pod_id` was released to `''` on a
prior graceful shutdown. `ClaimSession`'s CTE already refuses to steal a session
owned by a *different live* pod (`WHERE pod_id='' OR pod_id=$1`), so widening the
reconnect candidate set to `pod_id IN ('', self)` is safe in multi-pod and
correct in single-pod.

```go
// pg.go — new method (sibling to ListSessionsByPod)
func (s *PgStore) ListReconnectableSessions(ctx context.Context, podID string) ([]*SessionRow, error) {
    q := `SELECT ` + sessionCols + ` FROM mbs_sessions
          WHERE state = 'active' AND (pod_id = '' OR pod_id = $1)
          ORDER BY uid`
    rows, err := s.pool.Query(ctx, q, podID)
    ...
}
```

`reconnect.go` switches from `ListSessionsByPod(podID, "active")` to
`ListReconnectableSessions(podID)`. `GetOrConnect` → `connect` → `ClaimSession`
does the actual atomic claim; any session that another live pod grabbed first
fails the claim with `ErrClaimConflict` and is logged + skipped (existing
behaviour). No double-ownership risk.

**Why not fix shutdown to keep pod_id?** Releasing on shutdown is correct for
multi-pod failover (lets another pod claim immediately). The bug is reconnect's
candidate filter, not the release. Fix the filter.

## G2 — Auto-disable burned senders mid-campaign

**Files:** `cmd/campaign/` (new consumer), `internal/campaign/engine/` (handler),
`internal/campaign/handler/store.go` (new method).

When an MBS session burns mid-campaign, the JOIN in `GetActiveCampaignMbsSessions`
(state-gated, shipped in the close-the-loop chunk) already masks it at *selection*
time — a burned sender won't be picked. G2 makes the cleanup *proactive*: consume
`hermes.mbs.session.burned.*` and flip `campaign_senders.status` to `'burned'`
for that uid across all campaigns, so dashboards/queries reflect reality and the
`idx_campaign_senders_active` partial index stays accurate.

**Input event:** `MbsSessionLifecycleEvent` (subject `hermes.mbs.session.burned.{tenant}`),
fields used: `uid`, `new_state`, `reason`.

**New store method** (global, all campaigns — a burn is session-wide):
```go
func (s *PgStore) MarkMbsSenderBurned(ctx context.Context, uid int64) (int64, error) {
    tag, err := s.pool.Exec(ctx,
        `UPDATE campaign_senders SET status='burned'
         WHERE sender_kind='mbs' AND sender_id=$1 AND status='active'`,
        strconv.FormatInt(uid, 10))
    if err != nil { return 0, fmt.Errorf("mark mbs sender burned: %w", err) }
    return tag.RowsAffected(), nil
}
```
`sender_id` is stored as the decimal-string uid (matches how MBS senders are
added). Guard on `status='active'` so the write is idempotent — a redelivered
burn event affects 0 rows the second time.

**New consumer** `cmd/campaign/mbs_session_consumer.go`: binds `HERMES_MBS`
stream, subject `hermes.mbs.session.burned.*`, durable `campaign-mbs-burned`,
ManualAck. On message: unmarshal, call `engine.HandleSessionBurned(uid)` →
`store.MarkMbsSenderBurned`. Always Ack (idempotent; a transient DB error logs +
Acks, the selection-time JOIN is the backstop). Wired from `cmd/campaign/main.go`
after the existing result consumer.

**Note:** only `burned` is consumed, not all lifecycle states. `disconnected`
sessions auto-reconnect (I3) and shouldn't be disabled; only a true burn is
terminal.

## G3 — Idempotent completion (no duplicate completion event)

**File:** `internal/campaign/engine/engine_mbs_result.go`.

`maybeCompleteCampaign` documents (lines 115-118) that it skips the completion
event when the campaign was already terminal — but the code calls
`publishStatusEvent` **unconditionally**. `UpdateCampaignStatus` is an
unconditional `UPDATE ... RETURNING` (store.go:449) with no already-terminal
signal, so a redelivered terminal result re-publishes the completion event.

**Fix:** make the status flip conditional on the campaign *not already being
completed*, and only publish when this call performed the transition.

```go
// store.go — new method: conditional flip, reports whether WE transitioned it
func (s *PgStore) CompleteCampaignIfRunning(ctx context.Context, id string) (transitioned bool, err error) {
    tag, err := s.pool.Exec(ctx,
        `UPDATE campaigns SET status='completed', completed_at=now()
         WHERE id=$1 AND status <> 'completed'`, id)
    if err != nil { return false, fmt.Errorf("complete campaign if running: %w", err) }
    return tag.RowsAffected() == 1, nil
}
```

```go
// engine_mbs_result.go — maybeCompleteCampaign
transitioned, err := e.store.CompleteCampaignIfRunning(ctx, campaignID)
if err != nil { ...log...; return }
if !transitioned {
    return // already completed — no duplicate event
}
e.publishStatusEvent(tenantID, workspaceID, campaignID,
    hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING,
    hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED, "completed")
e.log.Info()...Msg("mbs: campaign completed (all results in)")
```

Both the result path and the reaper path call `maybeCompleteCampaign`, so the
guard covers a result/reaper race too. The old `UpdateCampaignStatus` stays for
the WA dispatch path (unchanged) and other callers.

---

# UNIT B — CTL-G1: WA close-the-loop (proto-touching, follow-up)

WA's open loop mirrors the MBS bug pre-fix: `dispatchWaLoop` eagerly writes
`UpdateContactSent` + counters at dispatch (engine.go:274-276) and marks the
campaign `completed` on dispatch-drain (engine.go:184), regardless of actual
send outcome. Unlike MBS, **no existing WA event carries `campaign:contact`**,
so the fix requires a new event surface.

## Contract (proto)

New message in `docs/contracts/proto/events.proto` (+ mirror to `proto/hermes/v1/`):
```proto
// WaCampaignSendResultEvent is published by hermes-wa on every campaign send
// attempt (success and terminal failure) so hermes-campaign can close the loop.
// Mirrors MbsOutboundEvent's correlation contract.
message WaCampaignSendResultEvent {
  EventMeta meta = 1;
  string campaign_id = 2;
  string contact_id = 3;
  string wa_number_id = 4;
  bool ok = 5;
  string error = 6;            // populated iff !ok
  string wa_message_id = 7;    // whatsmeow-assigned id on success
  google.protobuf.Timestamp sent_at = 8;
}
```
Subject: `hermes.wa.message.sendresult.{tenant}` on the `HERMES_WA` stream
(add to `ensureStreams` subjects if not already covered by a wildcard).
Durable consumer (campaign side): `campaign-wa-result`.

Regen: `PATH="$HOME/go/bin:/Users/env/.hermes/profiles/oracle/home/go/bin:$PATH" buf generate`
(gen/ is gitignored — commit proto source only).

## WA side (`cmd/wa/main.go` campaign consumer)

After `snd.SendMessage`:
- success → publish `WaCampaignSendResultEvent{ok:true, wa_message_id, campaign_id, contact_id, wa_number_id}` then Ack.
- terminal failure → publish `{ok:false, error}` then **Term** (don't NAK-storm;
  mirror the MBS send-consumer classifier from the close-the-loop chunk) — but
  keep the existing bounded NAK for transient "client not connected".

## Campaign side

- `dispatchWaLoop`: write `queued` (not `sent`), drop eager counter bumps, stop
  owning completion at dispatch-drain. (Mirror `engine_mbs.go` exactly.)
  `campaign_contacts_status_check` already allows `queued` (migration 000004,
  global constraint) — **no new migration**.
- New `engine_wa_result.go`: `HandleWaResult` — `queued→sent|failed` idempotent
  write-back keyed on `campaign_id:contact_id`, counter bumps on first
  transition, `maybeCompleteCampaign` (reuse G3's guarded version).
- WA reaper: reuse `ReapStuckQueuedMbs` pattern → `ReapStuckQueuedWa` (or
  generalize the reaper to be channel-agnostic since the contact rows are shared).
- New `cmd/campaign/wa_result_consumer.go` + reaper ticker, wired from main.go.

## Carried gaps for Unit B
- WA delivered/read receipt enrichment (`WaOutboundStatusEvent`) stays a separate
  concern — it's status decoration, not loop-closure.

---

## Verification plan

### Unit A
1. `go build ./... && go vet ./...` clean.
2. Unit tests: I2 delta-stamp test; I3 `ListReconnectableSessions` returns
   `pod_id=''` rows; G2 burned-event → `MarkMbsSenderBurned` affects active rows
   only (idempotent on redelivery); G3 second completion call returns
   `transitioned=false` → no second publish. All existing mbs+campaign suites green.
3. Rebuild hermes-mbs + hermes-campaign; redeploy; health green.
4. **Live end-to-end:** confirm session resident (reconnect `count:1` after I3),
   Sam replies from recipient, watch:
   `mbs: listener delta → PublishInboundMessage → inbox-mbs-inbound consume →
   conversation upsert → gateway mbs_new_message WS frame`. Reply visible in inbox.

### Unit B
Own plan/audit/commit; live test = WA campaign send → real result event → loop closes.

## Rollback
Unit A is additive (new query, new consumer, conditional SQL). Revert = redeploy
prior image tags (hermes-mbs prior digest, hermes-campaign prior digest). No
schema migration in Unit A → no down-migration needed.
