# Hostile Audit — Close-the-Loop: MBS Campaign Delivery Tracking

**Date:** 2026-06-01
**Author:** Oracle
**Plan:** `.hermes/plans/2026-06-01_close-the-loop-mbs-campaign-delivery.md`
**Trigger:** Campaign `2446f5a9-bc94-4fc2-81b0-e17156043031` reported `completed / sent_count=1`
with zero delivery to the recipient.

Audit method: adversarial walk of every failure/race path, each with the code-level reason it's
contained. Verdict per vector: **PASS** (contained), **ACCEPTED** (known tradeoff, documented), or
**OPEN** (carrying gap).

---

## Changes under audit

| Step | Change | File |
|---|---|---|
| S1 | Migration: `queued` status + inflight index | `migrations/campaign/000004_*` |
| S2 | Rotator JOINs `mbs_sessions.state='active'` | `handler/store.go:GetActiveCampaignMbsSessions` |
| S3 | No active senders → pause + status event | `engine/engine_mbs.go` |
| S4 | Dispatch writes `queued`, drops eager counters, no eager completion | `engine/engine_mbs.go` + `store.go` |
| S5 | Send consumer classifies Term (permanent) vs Nak (transient) | `cmd/mbs/send_consumers.go` |
| S6 | Result consumer `HandleMbsResult` + completion check | `engine/engine_mbs_result.go`, `cmd/campaign/mbs_result_consumer.go` |
| S7 | Stuck-queued reaper | `engine/engine_mbs_result.go:ReapStuckQueued`, `cmd/campaign` ticker |

---

## Attack vectors

### V1 — Double-send (same contact dispatched twice). **PASS**
Dispatch writes `status='queued'` guarded on `status='pending'`
(`UpdateContactQueuedMbs ... AND status='pending'`). `GetPendingContacts` only pulls
`status='pending'`. Once queued, a contact is invisible to the next dispatch batch. A redelivered
NATS *task* (not result) is deduped server-side by the handler's `(uid, client_dedupe_id)` dedupe
cache. Two layers.

### V2 — Double-count (sent_count/failed_count inflated). **PASS**
Counters now move ONLY on the genuine first terminal transition: `UpdateContactSentFromResult` /
`UpdateContactFailedFromResult` return `RowsAffected`, and the engine bumps a counter only when
`affected == 1`. The SQL is guarded on `status='queued'`, so any duplicate/redelivered result
event hits 0 rows → no bump. Test: `TestHandleMbsResult_DuplicateNoDoubleCount`.

### V3 — Lost result (send happened, event never consumed). **PASS (reaper backstop)**
The handler publishes `MbsOutboundEvent` to `HERMES_MBS` (retention=limits, 7-day age) on every
attempt. If the campaign result consumer is down, JetStream retains + redelivers on reconnect
(durable `campaign-mbs-result`, `DeliverAll`). If the event is *never* produced (e.g. mbs crashed
between send and publish), the contact stays `queued` and the S7 reaper times it out to `failed`
after 5min. No contact can be stranded in `queued` forever.

### V4 — Duplicate result events (NAK storm produced up to 5× ok=false). **PASS**
Idempotent `status='queued'` guard: first event transitions queued→failed (1 row), the rest hit 0
rows and are no-ops. S5 additionally Term()s permanent failures so the storm doesn't happen in the
first place. Belt + suspenders.

### V5 — Stuck `queued` (campaign hangs in `running`). **PASS**
S7 reaper (60s tick, 5min threshold) sweeps `status='queued' AND sent_at < cutoff` → `failed`,
bumps failed_count, re-checks completion per affected campaign. Test:
`TestReapStuckQueued_TimesOutAndCompletes`.

### V6 — Banned sender selected mid/early campaign. **PASS (root-cause fix)**
S2's JOIN on `mbs_sessions.state='active'` means a burned session is structurally excluded from the
rotator candidate set. If ALL senders are burned, S3 pauses the campaign with reason
"no active MBS senders" (operator-visible) rather than silently stranding contacts. Tests:
the engine `fakeMbsStore` exercises the no-senders→pause path
(`TestDispatchMbsLoop_NoActiveSessions`); the live JOIN is validated in S9.

### V7 — Stream not ready at campaign boot. **PASS**
`HERMES_MBS` is created by hermes-mbs, not campaign. `startMbsResultConsumer` retries the
`BindStream("HERMES_MBS")` subscribe 15× with 2s backoff (30s window) before fatal. Campaign can
boot before mbs without crashing.

### V8 — Reaper vs late-result race. **ACCEPTED (documented)**
Reaper marks a contact `failed` at T+5min; a genuine `ok=true` lands at T+5min+ε. The late event's
write-back is guarded on `status='queued'`, but the row is now `failed` → 0 rows, no-op. Contact
stays `failed`. We under-report (recipient may have received it) rather than over-report.
Consistent with the chunk's prime directive: **never lie about success.** 5min threshold is far
beyond the worst-case send path (a few seconds), so this race is rare.

### V9 — Manual sends contaminate campaign tracking. **PASS**
`hermes.mbs.send.manual.*` sends also emit `MbsOutboundEvent`. `parseDedupeKey` requires exactly
`<non-empty>:<non-empty>`; a manual send's dedupe id (if any) won't match a known
`campaignID:contactID`, and `GetCampaign` returns not-found → Ack + ignore. Test:
`TestHandleMbsResult_NonCampaignIgnored` covers nil/empty/no-colon/extra-colon/half-empty.

### V10 — Completion double-fire (duplicate COMPLETED status event). **PASS**
`maybeCompleteCampaign` is idempotent: it only fires when `CountInflightContacts` returns (0,0).
Once completed, no contacts are pending/queued so re-entry is a no-op. (Minor: a redelivered result
for an already-complete campaign re-runs the count, finds (0,0), and re-issues
`UpdateCampaignStatus(completed)` — a harmless idempotent DB write. The status *event* could
theoretically re-publish; downstream WS consumers treat completion as idempotent. Low-risk, noted.)

### V11 — Poison result event (malformed proto). **PASS**
`proto.Unmarshal` failure → `msg.Ack()` (drop poison, no redelivery loop). Logged.

### V12 — Transient DB error during write-back. **PASS (reaper backstop)**
`HandleMbsResult` logs the error and returns `true` (Ack) rather than Nak-looping on a DB blip. The
contact stays `queued`; the reaper is the backstop. Avoids a redelivery storm against a degraded DB.

### V13 — `sent_at` semantic overload. **ACCEPTED (documented)**
`sent_at` is set on the `queued` transition and reused as "dispatched_at" by the reaper. It is later
overwritten with the true send time on the queued→sent transition. Between queue and result,
`sent_at` means "dispatched"; after success it means "sent". Documented in plan S7 + store comments.
Avoids a schema column add. A contact that's `sent` has an accurate `sent_at`; a `queued` one uses
it as a dispatch clock — no consumer reads `queued.sent_at` as a real send time.

---

## Out of scope (carrying gaps)

- **CTL-G1 (WA parity):** WA channel still has the open-loop Bug 2 + Bug 3 (dispatch marks `sent`
  fire-and-forget; no result consumer). WA has its own `WaOutboundStatusEvent` surface. Separate
  chunk. Not regressed by this change — WA path untouched.
- **CTL-G2 (sender auto-burn):** `campaign_senders.status` is not proactively flipped when a session
  burns mid-campaign; S2's JOIN masks it at selection time, but the stale `active` row persists. A
  `hermes.mbs.session.burned.*` consumer in campaign could flip it. Hardening, not correctness.
- **CTL-G3 (completion status-event idempotency):** see V10 — harmless re-publish possible on a
  redelivered terminal result for an already-complete campaign. Could guard on the
  `UpdateCampaignStatus` returning a real RUNNING→COMPLETED transition.

## Test inventory
- `TestDispatchMbsLoop_TaskShapeAndDBUpdates` — dispatch writes queued, no eager sent/count/complete.
- `TestDispatchMbsLoop_NoActiveSessions` — no senders → pause.
- `TestDispatchMbsLoop_CapExhausted` — at-cap sender → no queue.
- `TestHandleMbsResult_SuccessMarksSentAndCompletes` — V2, V10 happy path.
- `TestHandleMbsResult_FailureMarksFailed` — failure write-back + error propagation.
- `TestHandleMbsResult_DuplicateNoDoubleCount` — V2/V4.
- `TestHandleMbsResult_NonCampaignIgnored` — V9, parseDedupeKey edge cases.
- `TestReapStuckQueued_TimesOutAndCompletes` — V5.
- `TestReapStuckQueued_NoopWhenNothingStuck` — reaper no-op.

`go build ./...` clean. `go test ./internal/campaign/... ./cmd/campaign/... ./cmd/mbs/...` green.

## Live verification (S9 — pending)
1. Apply migration 000004.
2. Add healthy session `61590752691262` as sender to a fresh one-contact test campaign (same
   recipient as `2446f5a9`).
3. Watch: dispatch→queued, mbs send (human-cadence), MbsOutboundEvent ok=true, write-back→sent,
   completion. Confirm recipient receives the message.
4. Negative confirmation: the burned `61590134170831` must NOT be selectable (S2).
