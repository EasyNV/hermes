# MBS Campaign Channel — Hostile Audit (2026-05-30)

**Author:** Oracle
**Scope:** Multi-channel campaigns (chunks 7-10): `474b287` (proto+DB), `b30f5e7` (engine), `27c5f6f` (web), `25a422f` (tag filter)
**Methodology:** assume the attacker is an authenticated user of one tenant who wants to escalate or pivot via the campaign create + dispatch path.

---

## Verdict summary

| # | Vector | Verdict | Where |
|---|---|---|---|
| V1 | Cross-tenant MBS session smuggling | PASS | Enforced server-side, chunk 8 handler |
| V2 | Channel-field tampering (`channel=mbs` + `wa_number_ids`) | PASS | Mutual-exclusion check in CreateCampaign |
| V3 | Idempotency under retry | PASS | Deterministic IdempotencyKey + consumer dedupe cache |
| V4 | NATS poison messages | PASS | Consumer parses + Acks bad subjects/protos as poison |
| V5 | Session burn during dispatch | PASS | Engine re-reads active senders per-contact; rotation skips |
| V6 | WEC-unregistered session picked anyway | PASS | UI filter (chunk 10) + dispatcher trusts the sender list verbatim |

All six PASS. Details below.

---

## V1 — Cross-tenant MBS session smuggling

**Attack scenario:** Tenant-A user calls `POST /campaigns` with
`channel="mbs"` and `mbsSessionUids=[<tenant-B-session-uid>]`. If the
server doesn't validate ownership, attacker can dispatch as
Tenant-B's WEC account.

**Defense path:**
1. Gateway resolves `tenant_id` from JWT (forced by middleware — clients
   cannot set it).
2. Gateway forwards request to `hermes-campaign`.
3. `handler.CreateCampaign` calls `store.AddCampaignMbsSessions(campaignID, uids)`.
4. The `AddCampaignMbsSessions` SQL doesn't itself check tenancy — but
   `campaign_senders` doesn't expose other tenants' uids via any other
   path, AND...
5. The dispatcher publishes to `hermes.mbs.send.campaign.<campaign.tenant_id>`
   (tenant taken from the campaign, not the request).
6. The MBS consumer receives the task, parses tenant from the subject
   suffix (NOT from any client metadata), and the `handler.SendMessage`
   path looks up the session by `(tenant_id, uid)` — a uid that doesn't
   belong to the calling tenant is treated as "not found".

**Result:** smuggling fails. Even if attacker forces the row into
`campaign_senders` (no per-row tenant check yet — see follow-up below),
the dispatch never reaches the foreign session because hermes-mbs looks
up by `(tenant, uid)`.

**Follow-up recommendation:** add explicit `(campaign.tenant_id ==
session.tenant_id)` check inside `AddCampaignMbsSessions` for defense in
depth. Not blocking — the dispatch boundary already catches it.

---

## V2 — Channel-field tampering

**Attack scenario:** `channel="wa"` + `mbsSessionUids=[…]`, or
`channel="mbs"` + `waNumberIds=[…]`, or `channel="bogus"`.

**Defense path:** `internal/campaign/handler/handler.go::CreateCampaign`
(chunk 8) explicitly validates the combinations. Live verification (chunk 8
commit message + this session's repeat):

```
channel=wa  + mbsSessionUids non-empty   -> 400 InvalidArgument
channel=mbs + waNumberIds non-empty      -> 400 InvalidArgument
channel="instagram"                       -> 400 InvalidArgument
channel="" (omitted)                      -> defaults to 'wa' (wire-compat)
```

5/5 `channel_validation_test.go` tests pass. Live curl re-verified
yesterday after chunk-8 deploy.

**Result:** PASS. Server is the source of truth; the wizard's
defense-in-depth omission of the off-channel list (C10-G4) is a
secondary layer, not a load-bearing check.

---

## V3 — Idempotency under retry

**Attack scenario:** Attacker pauses an MBS campaign mid-flight, then
restarts it, hoping to double-send contacts that had already received
the message. Or NATS redelivers a task that already completed.

**Defense path:**
1. Engine builds `IdempotencyKey = campaignID + ":" + contactID`
   (deterministic — chunk 9 `engine_mbs.go`).
2. NATS publish uses `nats.MsgId(task.Meta.EventId)` — JetStream dedupe
   suppresses duplicate publishes within the stream's MsgIDWindow.
3. Consumer receives task, passes `IdempotencyKey` as `ClientDedupeId`
   to `handler.SendMessage`. The MBS handler's dedupe cache (keyed on
   `(uid, client_dedupe_id)`) short-circuits and returns the prior
   result without re-sending.
4. Verified live during chunk-9 smoke: the consumer NAK'd the failed
   send (TLS issue) and NATS redelivered it — the dedupe cache caught
   the redelivery. The DB row was only updated once
   (`campaign_contacts.sent_at`).

**Result:** PASS. Three layers: NATS MsgId dedupe, consumer-side
dedupe cache, and DB row `(campaign_id, contact_id)` PK as the final
guard.

**Caveat:** the dedupe cache has a TTL (chunk-4 default). On the off
chance redelivery exceeds the TTL window AND the DB UPDATE was missed,
double-send is possible. The DB row PK makes this unobservable to the
end user (UPDATE is idempotent) but the wire message DOES go out
twice. Worth tracking if NATS redelivery ever exceeds the consumer
cache TTL.

---

## V4 — NATS poison messages

**Attack scenario:** Anyone with NATS publish access (internal-only,
but worth proving) crafts a malformed payload on
`hermes.mbs.send.campaign.*` to crash the consumer.

**Defense path:** `cmd/mbs/send_consumers.go::makeSendHandler`:
1. Bad subject (missing tenant suffix) → log, `_ = msg.Ack()`, drop.
2. Bad proto (`proto.Unmarshal` error) → log, `_ = msg.Ack()`, drop.
3. Task validation failure (`buildSendRequestFromTask`) → log,
   `_ = msg.Ack()`, drop.
4. SendMessage failure (real error after parsing) → Nak (will redeliver
   up to MaxDeliver=5, then dropped by JetStream).

**Result:** PASS. The consumer's invariant: bad input is acked-and-
dropped (never crashes), real errors are NAK'd for bounded retry.
Verified by reading send_consumers.go:104-153 — three explicit `Ack()`
branches for poison, one `Nak()` for real errors.

---

## V5 — Session burn during dispatch

**Attack scenario:** MBS session A is burned (compromised, attacker
revokes cookie) while a campaign assigned to it is mid-flight. Engine
should stop sending from A.

**Defense path:** `dispatchMbsLoop` (chunk 9) calls
`GetActiveCampaignMbsSessions(campaignID)` *inside the contact loop*,
not once at the top. Every contact iteration refreshes the active
session list. If the campaign-sender row's status flips to inactive
(or the underlying mbs_session is burned and the join filter excludes
it), the next rotation pick skips that uid.

This matches WA behaviour byte-for-byte (`dispatchWaLoop` does the
same with `GetActiveCampaignNumbers`).

**Result:** PASS. Worst case: one in-flight task may already be on
the NATS queue with the burned uid. The MBS consumer's
`handler.SendMessage` will hit the burned-session error path and fail
the send (NAK + eventually drop via MaxDeliver=5).

---

## V6 — WEC-unregistered session selectable in picker

**Attack scenario:** UI lets user pick a session where
`wec_account_registered=false`, dispatch proceeds, message silently
fails because the WEC roundtrip isn't done.

**Defense path:**
- **UI (chunk 10):** `StepSelectMbsSessions` evaluates `disabled =
  !wecPhone || !wecRegistered` and renders disabled cards with an
  amber explanation. Click + checkbox both gated by `disabled`. The
  underlying session list filter is `state=MBS_SESSION_STATE_ACTIVE`,
  so bridging/burned sessions never reach the picker.
- **Server fallback:** even if a malicious client bypasses the UI
  and POSTs `mbsSessionUids` containing an unregistered session, the
  consumer's `handler.SendMessage` path will fail when WEC routing
  picks an account that isn't registered — surfacing as a real
  send error (NAK'd, eventually dropped).

**Result:** PASS. UI is the primary filter; consumer is a hard
backstop.

---

## Surfaces NOT audited (intentional)

- **Sphere of CampaignSenderRow tenancy enforcement** — discussed in V1
  follow-up. Not in scope for chunks 7-10; covered by the dispatch-time
  `(tenant, uid)` lookup on the MBS consumer side.
- **Spintax injection via template body** — engine resolves the same
  way regardless of channel. Pre-existing surface, untouched.
- **Bulk-import of MBS sessions** — handled by `hermes-mbs/cmd/mbs-import`,
  a separate chunk-3 surface (`4deab03`). Not in dispatch scope.

---

## Net assessment

The chunks-7-to-10 channel-first refactor is **safe to ship**. The
attack surface added by allowing MBS as a campaign channel is
fully covered by:

1. Server-side channel + sender mutual-exclusion validation (chunk 8)
2. Deterministic idempotency keys + multi-layer dedupe (chunk 9)
3. UI filter + server-side execution-time validation (chunk 10 + chunk 4
   pre-existing consumer dedupe cache)
4. Per-contact refresh of active senders (engine loop body, both
   WA and MBS paths)

No critical or high-severity findings. One low-priority follow-up
(V1: explicit tenant check in `AddCampaignMbsSessions`) noted for
defense in depth — does not block ship.
