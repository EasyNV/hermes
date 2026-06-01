# Hostile Audit — Inbound Fix + Parity (Unit A)

**Date:** 2026-06-01
**Auditor:** Oracle (self-review, adversarial)
**Scope:** I1, I2, I3, G2, G3 (mbs + campaign, no proto)
**Plan:** `.hermes/plans/2026-06-01_inbound-fix-and-parity.md`
**Build state:** `go build ./...` clean; `go vet` clean (1 pre-existing atomic-copy
warning in listener_hook_test.go, out of blast radius); full mbs+campaign test
sweep green incl. 6 new/extended tests.

Method: try to break each change. Each vector is either CLOSED (handled) or
CARRIED (documented, out of scope).

---

## I1 — OnDelta wired to PublishInboundMessage

**V1.1 — Hook blocks the listener.** `OnDelta` runs inline in `listener.emit`
before broadcast. A slow hook stalls the 10s poll loop and back-pressures every
subscriber. → CLOSED: `PublishInboundMessage` is a non-blocking JetStream
publish (async by default in nats.go; no synchronous ack wait in the publisher).
No DB call, no network round-trip we await. Documented in the call-site comment.

**V1.2 — Hook panics, kills the listener goroutine.** → CLOSED: `listener.
fireHook` wraps `onDelta` in a `recover()` guard (pre-existing). A panicking
publish logs + continues; the listener survives.

**V1.3 — Receipt/presence deltas published as fake messages.** `InboundDelta`
carries non-message events (empty `Text`). → CLOSED: the closure gates on
`d.Text == ""` and returns early. Only real messages publish. Verified by the
existing listener tests that push mixed deltas.

**V1.4 — Duplicate publish per delta (N subscribers → N publishes).** → CLOSED:
by design the listener fires `onDelta` EXACTLY ONCE per delta before broadcast,
independent of subscriber count (chunk-4 invariant, asserted in
`TestListener_OnDeltaFiresOncePerDelta_RegardlessOfSubscribers`).

**V1.5 — At-least-once delta delivery → duplicate inbound rows.** SnapshotPoll
re-reads the messages DB every 10s; the same message can surface twice. → CARRIED
(INB-G2): de-dup is the inbox consumer's job. `processMbsInbound` keys on
`mbs_mid` (FindOrCreateMbsConversation / CreateMbsMessage are mid-keyed per the
chunk-3 contract), so a duplicate delta is an idempotent upsert downstream. Not
re-verified in this chunk; flagged for the live test.

## I2 — PageID / MailboxID stamping

**V2.1 — Empty PageID/MailboxID on the published event.** If `creds.PageID` /
`creds.WECMailboxID` are empty at connect, the inbound event ships blank routing
ids. → PARTIALLY CLOSED: `connect()` denormalizes these from the primary asset
(manager.go:211-213) only when `primary != nil`. A session with no primary asset
would stamp empty. In practice every bridged session has a primary asset (login
requires page selection), but this is a latent gap → CARRIED (INB-G3): add a
connect-time assertion that primary != nil for active sessions. Low risk: inbox
resolves workspace from `uid`, not pageID, so empty pageID doesn't misroute.

**V2.2 — Stamping the wrong session's ids (cross-uid contamination).** → CLOSED:
the listener is per-uid, constructed inside `connect()` with that uid's own
`creds`. `pageID`/`mailboxID` are captured at construction, immutable for the
listener's life. No shared mutable state.

**V2.3 — senderPhone empty → inbox can't attribute the reply.** → CARRIED
(INB-G1, already documented in plan): poll delta has `SenderName`/`SenderURL`
but no phone. Inbox synthesizes `mbs:thread:<id>` so the message LANDS; real WA
number needs thread→phone resolution. Separate enhancement. **This is the most
likely surprise in the live test** — the reply will appear in the inbox but
possibly under a synthetic identity, not the recipient's phone number.

## I3 — Reconnect reclaims orphaned sessions

**V3.1 — Two live pods both reclaim the same orphan → double ownership.** The
widened candidate set (`pod_id IN ('', self)`) means two pods could both SELECT
the same `pod_id=''` row. → CLOSED: candidate-selection is not ownership.
`ClaimSession`'s atomic CTE (`UPDATE ... WHERE pod_id='' OR pod_id=$self
RETURNING`) is single-winner: the first pod's claim flips `pod_id`, the second
pod's `GetOrConnect` → `connect` → `ClaimSession` sees `pod_id != ''` and !=
itself, returns `ErrClaimConflict`, and reconnect logs + skips it (existing
path). Verified the conflict branch exists at manager.go:163-165.

**V3.2 — Reclaiming a session legitimately owned by another LIVE pod.** Could we
steal a session another pod is actively serving? → CLOSED: same CTE. A live
pod's session has `pod_id = thatpod != ''`, so it's excluded from our candidate
WHERE (`pod_id='' OR pod_id=self`) entirely. We only ever see orphans + our own.
Test `TestListReconnectableSessions` explicitly asserts uid owned by
`hermes-mbs-prod-1` is NOT selected.

**V3.3 — Burned/disconnected sessions reconnected.** → CLOSED: query filters
`state='active'`. Burned rows excluded. Test covers a burned+orphan row → not
selected.

**V3.4 — Thundering herd on cold boot (many orphans).** Widening the set could
mean a big fleet reconnects many sessions at once. → CLOSED: reconnect already
bounds concurrency (`reconnectConcurrency=10` semaphore) and per-uid timeout
(30s). Unchanged.

## G2 — Burned-session consumer

**V4.1 — `disconnected` sessions wrongly disabled.** A disconnected session
auto-reconnects (I3) and must NOT be removed as a sender. → CLOSED: the consumer
subscribes ONLY to `hermes.mbs.session.burned.*`, not the wildcard lifecycle
subject. `disconnected`/`refreshed`/`connected` events never reach this handler.

**V4.2 — Redelivered burn event double-processes.** → CLOSED: idempotent at the
SQL layer — `MarkMbsSenderBurned` updates only `WHERE status='active'`, so the
second delivery affects 0 rows. Handler Acks regardless. Test
`TestHandleSessionBurned_DisablesSender` covers the call; the SQL guard is the
idempotency mechanism (covered by the PgStore method's WHERE clause, exercised
live).

**V4.3 — Transient DB error blocks the consumer / NAK-storms.** → CLOSED:
`HandleSessionBurned` logs the error and returns true (Ack). The state-gated
selection JOIN (`GetActiveCampaignMbsSessions ... AND cs.status='active' AND
ms.state='active'`) is the backstop — even if the campaign_senders row is never
flipped, a burned session (ms.state='burned') is excluded at pick time. Defence
in depth: the consumer is an optimization, the JOIN is the guarantee.

**V4.4 — uid string-encoding mismatch.** `sender_id` is stored as a decimal
string; the burn event carries an int64 uid. → CLOSED: `MarkMbsSenderBurned`
encodes via `fmt.Sprintf("%d", uid)`, byte-identical to how senders are inserted
(`UpdateCampaignMbsSessionStatus`, `IncrementMbsSessionSentCount` use the same).

**V4.5 — Stream-bind race on cold boot.** HERMES_MBS owned by hermes-mbs; if
campaign boots first the subscribe fails. → CLOSED: 15-attempt bind retry with
2s backoff (mirrors the result consumer). Logged per attempt.

**V4.6 — Zero/nil event.** → CLOSED: nil → Ack; uid==0 → log + Ack (poison
drop). Tests `TestHandleSessionBurned_ZeroUidDropped` + `_NilEvent`.

## G3 — Idempotent completion

**V5.1 — Duplicate completion event on redelivered terminal result.** The
original bug. → CLOSED: `CompleteCampaignIfRunning` is `UPDATE ... WHERE
status<>'completed'`; `RowsAffected()==1` only for the genuine transition.
`maybeCompleteCampaign` publishes the status event iff `transitioned`. Test
`TestMaybeComplete_Idempotent_NoDoublePublish`: 2 drained results → 2 completion
checks, exactly 1 'completed' transition.

**V5.2 — Result/reaper race both completing.** Both `HandleMbsResult` and
`ReapStuckQueued` call `maybeCompleteCampaign`. → CLOSED: same conditional
UPDATE. Whichever commits first wins (`RowsAffected==1`); the loser gets
`transitioned=false`, no publish. DB row-lock serializes the two UPDATEs.

**V5.3 — Completion fires while contacts still in flight.** → CLOSED:
`CountInflightContacts` guard precedes the flip; `pending>0 || queued>0` returns
early. Test `TestMaybeComplete_NotDrained_NoTransition`: queued=1 → 0 completion
attempts.

**V5.4 — `UpdateCampaignStatus` still used elsewhere — did we break it?** →
CLOSED: the WA dispatch path (engine.go:184), the MBS pause path
(engine_mbs.go:105), and other callers keep the old unconditional method
unchanged. We ADDED a method, didn't modify the existing one. Full suite green
confirms no caller regressed.

---

## Cross-cutting

**X1 — No migration in Unit A.** All changes are code + new SQL methods against
existing schema (`campaign_senders.status`, `campaigns.status` already exist).
`campaign_contacts_status_check` already includes `queued` (migration 000004,
prior chunk). Rollback = redeploy prior image tags, no down-migration.

**X2 — Interface surface grew (engineStore, handler.Store, store.Store).** Every
fake updated (mockStore, fakeMbsStore, mbs store mock); test-compile is the
guarantee — `go vet` passing means all implementers satisfy the new interfaces.

**X3 — Inbound has NEVER worked → no historical data to migrate.** This is a
first-activation, not a behavior change. No backfill of missed replies (they
were dropped at the listener and never persisted anywhere). The recipient's
earlier reply is gone; only NEW replies post-deploy will flow.

---

## Carried gaps (tracked, not fixed in Unit A)
- **INB-G1** — inbound senderPhone resolution (thread→phone). Reply lands under
  synthetic `mbs:thread:<id>` until added. **Watch for this in the live test.**
- **INB-G2** — inbound de-dup relies on inbox mid-keying; not re-verified here.
- **INB-G3** — connect-time assertion that active sessions have a primary asset
  (else empty pageID/mailboxID stamped). Low risk (workspace resolves from uid).
- **CTL-G1** — WA close-the-loop (Unit B, proto-touching, separate commit).

## Verdict
Unit A is sound. The inbound P0 (Bug A + Bug B) is structurally fixed: the
listener now publishes, and the pod re-adopts its orphaned sessions so a listener
is actually resident to publish FROM. G2/G3 are defence-in-depth hardenings with
the selection JOIN / conditional UPDATE as the real guarantees. Ready for deploy
+ live re-test. Primary live-test risk is cosmetic (INB-G1 synthetic identity),
not delivery.
