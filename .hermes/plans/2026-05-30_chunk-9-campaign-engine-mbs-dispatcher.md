# Chunk 9 — Campaign engine MBS dispatcher

**Date:** 2026-05-30
**Author:** Oracle
**Predecessors:** chunk 8 (`474b287`)
**Branch:** `main`

---

## Goal

Fork the campaign engine dispatch path on `campaign.Channel`. WA path stays
byte-identical. New MBS path rotates across `campaign_senders WHERE
sender_kind='mbs'`, builds `MbsCampaignSendTask`, publishes to
`hermes.mbs.send.campaign.{tenant}` (consumer already live), and updates
`campaign_contacts.mbs_session_uid` + `campaign_senders.sent_count` after each
send.

Out of scope: ban-pause analogue for MBS (different semantics), per-campaign
`page_id_override` UI (proto exists; UX iteration later), pre-batch phone
resolution (engine sends `recipient_phone` and lets hermes-mbs resolve per
message, matching how the consumer currently expects).

---

## Surface

| File | Change |
|---|---|
| `migrations/campaign/000003_campaign_contacts_mbs.up.sql` + `.down.sql` | NEW — `ALTER TABLE campaign_contacts ADD COLUMN mbs_session_uid BIGINT NULL;` + supporting index. Up/down/up smoked. |
| `internal/campaign/handler/store.go` | NEW method `UpdateContactSentMbs(ctx, campaignID, contactID, uid)` (mirror of `UpdateContactSent` but writes `mbs_session_uid` and clears `wa_number_id`). Add to `Store` interface. Extend mock store. |
| `internal/campaign/engine/rotation_mbs.go` | NEW — `MbsSessionInfo` struct (`UID int64`, `SentToday int32`, `Status string`) + `MbsRotator` interface + `RoundRobinMbsRotator` + `LeastUsedMbsRotator`. Behaviour mirrors `rotation.go`. |
| `internal/campaign/engine/rotation_mbs_test.go` | NEW — port of `rotation_test.go` for the int64-UID variant. |
| `internal/campaign/engine/engine.go::engineStore` | Add 3 methods: `GetActiveCampaignMbsSessions`, `UpdateContactSentMbs`, `IncrementMbsSessionSentCount`. (Already on `Store` from chunk 8; just extend the engine's narrow interface.) |
| `internal/campaign/engine/engine.go::dispatchLoop` | Branch on `campaign.Channel`. `"wa"` (or empty) → existing body (extracted into `dispatchWaLoop`). `"mbs"` → new `dispatchMbsLoop`. |
| `internal/campaign/engine/engine_mbs.go` | NEW — `dispatchMbsLoop` containing the MBS-specific inner loop: rotation, task build, publish, DB update. |
| `internal/campaign/engine/engine_mbs_test.go` | NEW — at minimum: task-shape test + subject-format test using a fake JetStream interface. |
| `internal/campaign/handler/handler_test.go` | Extend mock store with the new method. |

---

## Behaviour

### Migration `000003_campaign_contacts_mbs`

**Up:**
```sql
ALTER TABLE campaign_contacts
  ADD COLUMN mbs_session_uid BIGINT NULL;

CREATE INDEX IF NOT EXISTS idx_campaign_contacts_mbs
  ON campaign_contacts(campaign_id, mbs_session_uid)
  WHERE mbs_session_uid IS NOT NULL;
```

**Down:**
```sql
DROP INDEX IF EXISTS idx_campaign_contacts_mbs;
ALTER TABLE campaign_contacts
  DROP COLUMN IF EXISTS mbs_session_uid;
```

Additive — no data backfill needed (WA campaigns keep `wa_number_id`, MBS
campaigns populate `mbs_session_uid`). Channel is the discriminator; the row
join key (`(campaign_id, contact_id)`) is unchanged.

### `UpdateContactSentMbs`

```go
func (s *PgStore) UpdateContactSentMbs(ctx context.Context, campaignID, contactID string, uid int64) error {
    _, err := s.pool.Exec(ctx,
        "UPDATE campaign_contacts SET status='sent', mbs_session_uid=$1, wa_number_id=NULL, sent_at=now() "+
            "WHERE campaign_id=$2 AND contact_id=$3",
        uid, campaignID, contactID)
    return err
}
```

Explicit `wa_number_id=NULL` defends against a row that was partially in WA
state from a re-channeled campaign (shouldn't happen — channel is immutable
post-create — but cheap defense).

### `MbsRotator`

Mirror of `Rotator` with `int64 UID` instead of `string WaNumberID`. Reuses
the per-session daily cap check (`SentToday >= dailyCap`) and "active" status
filter (`Status != "active"` skips). Existing `campaign.DailyCapPerNum` field
applies per-session for MBS — same semantic, different sender type. (Future:
per-channel cap if WEC's rate limits diverge enough.)

### `dispatchMbsLoop` (top-level pseudocode)

Reads `campaign.RotationStrategy` (same field, MBS-shaped rotator).

Per pending contact:
1. `GetActiveCampaignMbsSessions(campaignID)` → `[]*CampaignMbsSessionRow`
2. Convert to `[]MbsSessionInfo`
3. `rotator.Next(infos, campaign.DailyCapPerNum)` → `(uid, ok)`. `!ok` → log warn, return (matches WA "all numbers exhausted")
4. Build `vars` (same shape as WA: `name`, `phone`, custom_fields)
5. `resolvedBody = spintax.Resolve(tmpl.Body); spintax.SubstituteVariables(resolvedBody, vars)`
6. Build `MbsCampaignSendTask`:
   - `Meta.{EventId, TenantId, Timestamp, Source="hermes-campaign"}`
   - `CampaignId`, `ContactId`
   - `Uid = uid`
   - `ThreadId = ""` (let hermes-mbs resolve)
   - `RecipientPhone = strings.TrimPrefix(contact.Phone, "+")` (matches consumer expectation per events.proto:431)
   - `ResolvedBody = resolvedBody`
   - `PageIdOverride = ""` (let primary asset rule pick — future hookup)
   - `IdempotencyKey = campaignID + ":" + contact.ContactID` (deterministic, matches WA)
7. Publish to `fmt.Sprintf("hermes.mbs.send.campaign.%s", tenantID)` with `natsgo.MsgId(task.Meta.EventId)`
8. DB updates (best-effort, errors logged but don't halt the loop, mirroring WA):
   - `UpdateContactSentMbs(campaignID, contactID, uid)`
   - `IncrementSentCount(campaignID)`
   - `IncrementMbsSessionSentCount(campaignID, uid)`
9. `dispatched++`; progress event every 10 sends or 5s (existing helper, no change)

**Completion / cancel:** identical to WA — `len(contacts) == 0` → status
event + return.

**Resume-from-checkpoint:** identical to WA — `dispatched = campaign.SentCount`.

### `dispatchLoop` branch

```go
switch campaign.Channel {
case "", "wa":
    e.dispatchWaLoop(ctx, campaign, tmpl, tenantID, workspaceID)
case "mbs":
    e.dispatchMbsLoop(ctx, campaign, tmpl, tenantID, workspaceID)
default:
    e.log.Error().Str("channel", campaign.Channel).Msg("unknown channel, refusing dispatch")
    return
}
```

Empty channel defaults to `"wa"` — backward-compat with rows that predate
chunk-8 migration (the `NOT NULL DEFAULT 'wa'` backfill should cover them,
but defense in depth).

---

## Contracts

- **C9-G1** — MBS dispatch is feature-flagged behind `campaign.Channel`. WA campaigns continue to use the existing `dispatchLoop` body verbatim (extracted into `dispatchWaLoop`, no behaviour change).
- **C9-G2** — `MbsCampaignSendTask.idempotency_key` MUST equal `campaignID + ":" + contactID` so the consumer-side dedupe cache (`internal/mbs/handler/rpc_send_message.go`) suppresses redeliveries on retry.
- **C9-G3** — NATS subject MUST be `hermes.mbs.send.campaign.<tenant_id>` exactly. The hermes-mbs consumer subject filter is `hermes.mbs.send.campaign.*` (cmd/mbs/send_consumers.go:39). Any other shape → message never delivered.
- **C9-G4** — Per-session daily cap honored. `MbsRotator` skips sessions where `SentToday >= DailyCapPerNum`. When all sessions are exhausted or inactive → loop returns. Campaign stays in `running` (operator decides whether to manually pause). Match WA semantics.
- **C9-G5** — On every send, three DB updates happen (best effort): `campaign_contacts.{status='sent', mbs_session_uid=uid, sent_at=now()}`, `campaigns.sent_count++`, `campaign_senders.sent_count++` (via `IncrementMbsSessionSentCount` which already filters `sender_kind='mbs'`).
- **C9-G6** — `recipient_phone` is E.164 minus leading `+` per the proto comment (`events.proto:431`). Contact phones stored as `+62...` get the `+` stripped before publish; raw digits pass through unchanged.
- **C9-G7** — Migration is reversible. Up/down/up cycle leaves WA campaigns' `wa_number_id` column intact; only the new `mbs_session_uid` column is touched.
- **C9-G8** — `engineStore` interface widens by 3 methods. Mock implementations in tests must add stubs. Real `PgStore` already satisfies them (added in chunk 8 except `UpdateContactSentMbs` which this chunk adds).

---

## Verification gates

1. `make proto-gen` clean (no proto changes this chunk, but verify nothing drifted)
2. `go build ./...` green across the monorepo
3. `go test ./internal/campaign/...` green including new `rotation_mbs_test.go` and `engine_mbs_test.go`
4. Migration smoke: `migrate up → down → up` against live PG, schema version lands at 3
5. End-to-end smoke against the running stack:
   - Create MBS campaign (channel=mbs, 1 session uid=61590134170831, 1 contact, simple template)
   - Start the campaign via `POST /api/v1/campaigns/{id}/start` (or whichever the gateway exposes)
   - `nats sub 'hermes.mbs.send.campaign.>'` shows the task land
   - `mbs_session_outbound_status` event flows back through the WS topic
   - `campaign_contacts.status='sent'` and `campaign_contacts.mbs_session_uid` populated for the test contact
6. Regression: existing WA campaign creation + dispatch unchanged (smoke a WA campaign in the stack)
7. Failure path: MBS campaign with one session at daily cap → engine exits dispatch (warn logged), campaign stays in `running` until operator pauses

---

## Decisions already locked

- **D2=9-α** (Sam-approved): `campaign_contacts` gets additive `mbs_session_uid BIGINT NULL` column. Don't rename `wa_number_id`. (Doc: plan-chunks-7-to-11 lines 112-115.)
- **Phone format on the wire:** E.164 minus leading `+`. Consumer doc says so (`events.proto:431`); engine strips it before publish.
- **Dispatch failure when all senders exhausted:** keep campaign in `running`, log warn, return from loop. Same as WA. Sam didn't ask for ban-pause analogue, so we don't add one.

---

## Files-modified summary (anticipated)

```
migrations/campaign/000003_campaign_contacts_mbs.up.sql       NEW
migrations/campaign/000003_campaign_contacts_mbs.down.sql     NEW
internal/campaign/handler/store.go                            +UpdateContactSentMbs, +Store iface
internal/campaign/handler/handler_test.go                     +mock stub
internal/campaign/engine/engine.go                            dispatchLoop split, engineStore widens
internal/campaign/engine/engine_mbs.go                        NEW dispatchMbsLoop
internal/campaign/engine/rotation_mbs.go                      NEW MbsRotator + impls
internal/campaign/engine/rotation_mbs_test.go                 NEW rotation tests
internal/campaign/engine/engine_mbs_test.go                   NEW task-shape + subject tests
```

---

## Commit message (target)

```
feat(campaign): chunk 9 — engine MBS dispatcher

dispatchLoop now branches on campaign.channel. WA path extracted into
dispatchWaLoop verbatim (no behaviour change). New dispatchMbsLoop:

  - Loads campaign_senders WHERE sender_kind='mbs'
  - Rotates via MbsRotator (round-robin or least-used over UIDs)
  - Builds MbsCampaignSendTask with deterministic idempotency_key
  - Publishes to hermes.mbs.send.campaign.<tenant> (consumer already live)
  - Updates campaign_contacts.mbs_session_uid + sent_count

Migration 000003_campaign_contacts_mbs: additive BIGINT column
+ partial index. Up/down/up smoked.

Tests: rotation_mbs_test (port of rotation_test) + engine_mbs_test
(task shape, subject format, fake JetStream). All packages green.

Plan: .hermes/plans/2026-05-30_chunk-9-campaign-engine-mbs-dispatcher.md
```
