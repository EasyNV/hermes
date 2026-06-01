# Plan — Close the Loop: MBS Campaign Delivery Tracking

**Date:** 2026-06-01
**Author:** Oracle
**Trigger:** Campaign `2446f5a9-bc94-4fc2-81b0-e17156043031` reported `completed / sent_count=1`
but the recipient got nothing. Root-cause diagnosis found three compounding bugs that make the
MBS send pipeline **open-loop**: the engine fires tasks into NATS and declares success without
ever hearing whether a send actually happened.

**Scope:** MBS channel only. WA shares the open-loop pattern (Bug 2 + Bug 3) but has its own
`WaOutboundStatusEvent` surface and is out of blast radius for this chunk. Documented as follow-up.

**Ship discipline:** one logical chunk, built in ordered steps, each `go build`/`go test`-green,
landed together. Ends with a hostile-audit doc in `docs/research/`.

---

## The three bugs (diagnosis recap)

**Bug 1 — rotator picks burned sessions (root cause of *this* failure).**
`PgStore.GetActiveCampaignMbsSessions` (store.go:585) filters only on
`campaign_senders.status='active'` and never joins `mbs_sessions` to check live `state`.
The sender row stayed `active` after the underlying session got burned, so the rotator served
a banned account (`61590134170831`, OAuthException 190/464).

**Bug 2 — engine marks `sent` fire-and-forget.**
`dispatchMbsLoop` (engine_mbs.go:146-159) publishes the task, then *immediately* writes
`status='sent'` + `sent_count++` with zero feedback from the consumer. DB says "sent" the instant
the task is queued. This is the "lying about success" bug.

**Bug 3 — NAK storm + no terminal state.**
`makeSendHandler` (send_consumers.go:155) does a bare `msg.Nak()` on any `SendMessage` error →
immediate redelivery, 5 rapid retries against a banned account (OPSEC fingerprint), then the
message dies unacked with **no failure recorded anywhere**. (Confirmed live: task stuck at
`HERMES_MBS_SEND` seq 8, `redelivered`, `ack_floor=0`.)

**Net effect:** banned sender, dead recipient, network blip — all report `completed / sent_count=1`
identically.

---

## Design — closed-loop delivery tracking

### Contract reuse (no proto changes)
- **Result event:** `MbsOutboundEvent` (events.proto:368) — already published by
  `internal/mbs/handler/rpc_send_message.go:77` on **both** success and failure.
  - `uid`, `ok`, `error`, `sent_at`, `mid`, `latency_ms`, `client_dedupe_id`.
- **Subject:** `hermes.mbs.message.outbound.{tenant_id}` → `HERMES_MBS` stream
  (`hermes.mbs.message.>`, retention=limits, max_age=7d). Durable, replayable.
- **Correlation:** `client_dedupe_id` = `MbsCampaignSendTask.idempotency_key` =
  `campaignID + ":" + contactID`. Engine parses this to locate the contact row.

### New contact lifecycle
```
pending ──dispatch (publish task)──▶ queued ──result ok=true──▶  sent
                                       │
                                       ├──result ok=false──────▶  failed (error set)
                                       └──reaper timeout───────▶  failed (error="send result timeout")
```
- Dispatch sets `queued` (NOT `sent`). Prevents `GetPendingContacts` from re-pulling (no double-send)
  AND stops the premature-`sent` lie.
- Counters (`campaigns.sent_count/failed_count`, `campaign_senders.sent_count`) move from dispatch
  to the **result consumer**, incremented only on the terminal transition.

### Completion redefined
- Old: campaign completes when `GetPendingContacts` returns 0 (i.e. all *queued*).
- New: completion = **no `pending` AND no `queued`** contacts remain. The dispatch loop drains
  `pending → queued` then exits WITHOUT marking complete. The **result consumer** re-checks
  completion after each terminal write-back and marks `completed` when the campaign fully drains.
- Safety reaper (below) guarantees `queued` always eventually drains, so a campaign can't hang
  in `running` forever.

---

## Steps

### S1 — Migration: add `queued` to contact status (migrations/campaign)
- New migration `000004_add_queued_contact_status.{up,down}.sql`.
- `up`: drop + recreate the CHECK constraint to include `'queued'`:
  `CHECK (status = ANY (ARRAY['pending','queued','sent','delivered','failed','skipped']))`.
- Add partial index for the reaper + completion check:
  `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_campaign_contacts_inflight
   ON campaign_contacts (campaign_id, status) WHERE status IN ('pending','queued');`
  (Note: `CONCURRENTLY` cannot run in a txn — golang-migrate runs each file in a txn by default;
  use a separate migration file or set `-- +migrate NoTransaction` equivalent. For golang-migrate,
  put the index in its own migration without `CONCURRENTLY` since tables are tiny here, OR document
  manual apply. Decision: plain `CREATE INDEX IF NOT EXISTS` — tables are small, lock is sub-ms.)
- Apply via the `migrate` compose service (already wired) or psql-in-container.

### S2 — Bug 1: state-gate the MBS rotator (store.go)
- Rewrite `GetActiveCampaignMbsSessions`:
  ```sql
  SELECT cs.campaign_id, cs.sender_id, cs.status, cs.sent_count, cs.failed_count
  FROM campaign_senders cs
  JOIN mbs_sessions s ON s.uid = cs.sender_id::bigint
  WHERE cs.campaign_id = $1
    AND cs.sender_kind = 'mbs'
    AND cs.status = 'active'
    AND s.state = 'active'
  ```
- This alone stops banned accounts from being selected. Unit test: burned session excluded.

### S3 — Bug 1 follow-through: no-active-senders handling (engine_mbs.go)
- When `rotator.Next` returns `!ok` (all sessions exhausted/none active): instead of silently
  `return` (leaving contacts `pending` forever), mark the campaign `paused` with a status event
  + reason `"no active MBS senders"`. Operator-visible, not a silent hang.
- Pin with a test: zero active sessions → campaign paused, contacts untouched (still `pending`,
  resumable once a sender is added).

### S4 — Bug 2: dispatch writes `queued`, drops eager counters (engine_mbs.go + store.go)
- New store method `UpdateContactQueuedMbs(ctx, campaignID, contactID, uid)`:
  `UPDATE campaign_contacts SET status='queued', mbs_session_uid=$1, wa_number_id=NULL
   WHERE campaign_id=$2 AND contact_id=$3 AND status='pending'` (guard on `pending` for idempotency).
- Dispatch loop: replace `UpdateContactSentMbs` + `IncrementSentCount` +
  `IncrementMbsSessionSentCount` with the single `UpdateContactQueuedMbs`. **Remove** the eager
  counter increments (they move to S6).
- Dispatch loop no longer marks the campaign `completed` when pending drains — it just returns
  ("dispatch done, awaiting results"). Completion ownership moves to S6.

### S5 — Bug 3: classify send errors, Term vs bounded Nak (send_consumers.go)
- Replace bare `msg.Nak()` with error classification:
  - **Permanent** (banned session, OAuthException 190/464, validation, tenant mismatch) →
    `msg.Term()` (no redelivery — the result event with `ok=false` was already published by the
    handler, so the engine still learns of the failure). Stops the 5× ban hammer.
  - **Transient** (network, timeout, 5xx) → `msg.Nak()` (bounded by existing `MaxDeliver=5`).
- Classification helper reads the gRPC status code from `h.SendMessage` error
  (handler's `mapSendErr` already routes session vs client errors — reuse its codes).
- Pin with tests: permanent error → Term (no redelivery), transient → Nak.

### S6 — NEW: result consumer in hermes-campaign (cmd/campaign + engine)
- New durable push consumer in `cmd/campaign/main.go`: subscribe
  `hermes.mbs.message.outbound.*` bound to stream `HERMES_MBS`, durable `campaign-mbs-result`,
  `ManualAck`, `AckWait=30s`, `MaxDeliver=5`.
  - **Startup ordering:** `HERMES_MBS` is created by hermes-mbs. Wrap the subscribe in a bounded
    retry (e.g. 10×, 2s backoff) so campaign tolerates booting before mbs creates the stream.
    (Mirrors carrying gap C3-G1 sub-bind retry.)
- New engine method `HandleMbsResult(ctx, *MbsOutboundEvent)`:
  1. Parse `campaignID:contactID` from `client_dedupe_id`. Malformed → Ack (drop poison) + log.
  2. Idempotent terminal write-back (guard on `status='queued'` so duplicate/redelivered events
     and the NAK-storm's multiple `ok=false` events are absorbed):
     - `ok=true`: `UPDATE ... SET status='sent', sent_at=now() WHERE ... AND status='queued'`.
       If `RowsAffected==1` → `IncrementSentCount` + `IncrementMbsSessionSentCount`.
     - `ok=false`: `UPDATE ... SET status='failed', failed_at=now(), error=$ WHERE ... AND status='queued'`.
       If `RowsAffected==1` → `IncrementFailedCount`.
  3. After a terminal transition, re-check completion: if no `pending` and no `queued` remain →
     `UpdateCampaignStatus(completed)` + publish `CAMPAIGN_STATUS_COMPLETED` status event.
  4. Ack.
- New store methods: `UpdateContactSentFromResult`, `UpdateContactFailedFromResult` (both return
  RowsAffected), `CountInflightContacts(campaignID) (pending, queued int)`.

### S7 — Safety reaper: time out stuck `queued` contacts (engine + cmd/campaign)
- Background ticker (60s) in hermes-campaign: for contacts `status='queued'` with
  `now() - sent_at(or queued_at) > 5min`, mark `failed` with `error="send result timeout"` +
  `IncrementFailedCount`, then re-check completion.
  - Needs a `queued_at` timestamp. Reuse `sent_at` column (set it on the `queued` transition in S4),
    semantically "last state change time." Cleaner: add `queued_at`, but reusing `sent_at` avoids a
    column add. **Decision:** set `sent_at=now()` on the `queued` transition and treat it as
    "dispatched_at"; the reaper keys off it. (Document the semantic overload.)
- Guards against poison-Ack'd tasks (no result event ever fires) leaving a campaign hung forever.
- Pin with a test: a `queued` contact older than the threshold → reaped to `failed`.

### S8 — Tests + hostile audit
- Unit tests: S2 (state gate), S3 (no senders → pause), S5 (Term vs Nak), S6 (write-back
  idempotency, completion trigger, poison handling), S7 (reaper).
- `go build ./...` + `go test ./internal/campaign/... ./internal/mbs/... ./cmd/...` green.
- Hostile-audit doc `docs/research/mbs-close-the-loop-hostile-audit-2026-06-01.md` covering:
  double-send, double-count, lost result, duplicate result, stuck queued, banned mid-campaign,
  stream-not-ready-at-boot, reaper-vs-late-result race.

### S9 — Deploy + live re-test (the second ask)
- Rebuild + redeploy `mbs` and `campaign` images (prod compose).
- Apply S1 migration.
- Add healthy account `61590752691262` as a sender to campaign `2446f5a9` (or a fresh test
  campaign — `2446f5a9` is already `completed`; cleaner to clone it to a new test campaign with
  one contact).
- Re-run, watch the full loop: dispatch→queued, mbs send (with the new human-cadence already live),
  result event ok=true, write-back→sent, completion. Confirm the recipient actually receives it.

---

## Race / edge analysis (pre-audit)
- **Duplicate result events** (NAK storm publishes up to 5× `ok=false`): absorbed by the
  `status='queued'` guard — only the first transition counts; rest are no-ops (RowsAffected=0).
- **Result arrives after reaper timeout** (reaper marked `failed`, then a late `ok=true` lands):
  the late event's guard is `status='queued'` but row is now `failed` → no-op. Contact stays
  `failed`. Acceptable (recipient may have gotten it, but we under-report rather than over-report
  — consistent with "never lie about success"). Documented.
- **Stream not ready at campaign boot:** bounded retry on subscribe (S6).
- **Completion double-fire:** completion check is idempotent (`UpdateCampaignStatus` to `completed`
  is a no-op if already completed); guard the status-event publish on the transition.
- **Manual sends** (`hermes.mbs.send.manual.*`): also produce `MbsOutboundEvent`. The result
  consumer filters: `client_dedupe_id` that doesn't parse as `campaignID:contactID` for a known
  campaign → Ack + ignore (inbox owns manual correlation). No interference.

## Files touched
- `migrations/campaign/000004_*.{up,down}.sql` (NEW)
- `internal/campaign/handler/store.go` (S2, S4, S6, S7 store methods)
- `internal/campaign/engine/engine.go` + `engine_mbs.go` (S3, S4, S6, S7)
- `internal/campaign/engine/engineStore` interface (new methods)
- `cmd/campaign/main.go` (S6 consumer wiring + retry, S7 reaper ticker)
- `cmd/mbs/send_consumers.go` (S5)
- Tests across the above.
- `docs/research/mbs-close-the-loop-hostile-audit-2026-06-01.md` (NEW)

## Out of scope (documented follow-ups)
- WA channel shares Bug 2 + Bug 3. Same closed-loop treatment via `WaOutboundStatusEvent` →
  future chunk.
- `campaign_senders.status` auto-sync when a session burns mid-campaign (today the row stays
  `active`; S2 masks it via the JOIN, but a burned-session NATS event could proactively flip the
  sender row). Future hardening.
