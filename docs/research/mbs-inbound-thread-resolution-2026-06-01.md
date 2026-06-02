# MBS Inbound Thread-Resolution (Option 2) — Build + Hostile Audit

**Date:** 2026-06-01
**Author:** Oracle
**Goal:** Unify MBS inbound and outbound into one conversation keyed on the
real Meta customer thread_id, showing the real customer phone/name — matching
the Meta Business Suite source inbox.
**Status:** ✅ SHIPPED & VERIFIED LIVE (uncommitted on disk).

---

## TL;DR

Customer replies were not landing in the Hermes inbox. The inbox keys
conversations on `(workspace_id, mbs_session_uid, mbs_thread_id)` and
hard-requires a non-empty `mbs_thread_id`. Inbound messages carried an empty
thread_id, so every reply died at the final conversation upsert.

Two distinct bugs, fixed in sequence:

1. **No thread_id on inbound** (the resolution problem). The mbs listener polls
   db130 (messages, a SQLite-replication stream) which carries message bodies +
   sender FBIDs but **not** the Meta `customer_id` (= thread_id) per message. The
   old code set `ThreadID = m.OTID`, but OTID is the *sender's outbound
   optimistic id* — empty on genuine customer inbound.

2. **`COALESCE(uuid, '')` parse-time crash** (the latent upsert bug). Once #1
   was fixed and inbound reached the conversation upsert, the upsert threw
   `invalid input syntax for type uuid: ""`. Root cause: the `RETURNING` clause
   used `COALESCE(wa_number_id, '')` on a NULL uuid column. **This throws at
   plan time regardless of the column value** because Postgres coerces the `''`
   literal to uuid via `uuid_in('')`. Latent in the WA path (never hit live
   because `wa_number_id` is non-NULL there and tests use mocks); exposed the
   moment MBS inbound first reached real SQL.

---

## Root-cause RE (bug #1)

Instrumented the listener with an env-gated raw-payload dump
(`MBS_POLL_DUMP_PATH`) and decoded the live db130 payload (12462 bytes).

**db130 wire format (per message):**
```
26 <senderFBID varint> 00 38 13 <19-digit snowflake> 00 38 22 <MID> ... 38 04 <body>
```

Decoding `senderFBID` across all 6 captured messages cleanly split direction:
- **Customer (Samuel):** FBID `2255842320809130` → "Halo gan", "Baik gan"
- **Admin/self:** FBID `2253276134399082` → "Halo, apa kabar?", "test"

**The join (pure parse, zero extra network calls):** the payload is *segmented*.
The **threads block** carries, per thread:
- `customer_id` (= thread_id, = `1127921160404565`, the value the send path
  already resolved via `BizInboxWhatsAppCustomerMutation` and stored in
  `mbs_phone_threads`)
- the participant FBIDs (customer `2255842320809130` + admin `2253276134399082`)
- the last-message snippet

So each thread block contains **both** the `customer_id` AND the customer's
messaging FBID. The messages block carries only the FBID. Join:

```
inbound message → senderFBID
                → find thread block where senderFBID is a participant
                → read that block's customer_id  = thread_id
                → publish with thread_id
                → inbox keys on it → UNIFIES with outbound (same key)
```

The admin's self-FBID (present in every thread block) gives clean direction
detection: `senderFBID == self` ⇒ outbound echo ⇒ drop (we only publish inbound).

This replaced the earlier guesses (db205 threads-DB poll, db95 contacts poll,
GraphQL FBID→thread resolve, an FBID column migration) — all unnecessary, the
data is in the single db130 payload.

---

## Implementation

### Parser — `re/mbs/mbs-native/fb/snapshot.go` (new)
`ParseSnapshot(payload []byte) (*Snapshot, error)` recovers:
- threads: `customer_id`, participant FBIDs, snippet
- messages: `senderFBID`, `body`, `MID`
- `SelfFBID` derived by intersecting participant sets across threads (the FBID
  present in every thread block is the session actor).
Committed live-payload fixture + full unit suite (`snapshot_test.go`).

### Listener wiring — `internal/mbs/session/inbox_parser.go`
`parseSnapshotPoll` rewired to use `fb.ParseSnapshot`: join senderFBID→customer_id,
set `ThreadID = customer_id`, drop self-echoes. Added `SenderFBID` to
`InboundDelta` for observability/direction. The push-path `parseInboxItem`
(delta-push format with inline `fb://profile/...`) keeps the old extractor +
`deriveThreadID`.

### Inbox identity enrichment — `cmd/inbox/main.go` + `internal/inbox/handler/store.go`
- New store method `GetPhoneByMbsThread(uid, threadID) → phone`, reverse lookup
  on `mbs_phone_threads` (the send path's `(uid, page, phone)→thread_id` map).
- `processMbsInbound`: when the inbound carries no `sender_phone` (snapshot
  format never does) but has a thread_id, reverse-resolve the real customer
  phone so the contact unifies with the outbound conversation instead of a
  synthetic `mbs:thread:<id>` slug.
- Migration `000005` adds the `(uid, thread_id)` reverse index. Tracker bumped to 5.

### The `COALESCE(uuid,'')` fix — `internal/inbox/handler/store.go`
`FindOrCreateMbsConversation` + `FindOrCreateConversation` RETURNING clauses:
`COALESCE(wa_number_id, '')` → `COALESCE(wa_number_id::text, '')`. Empirically
confirmed `COALESCE(<uuid>, '')` throws even for non-NULL uuid; `::text` cast fixes it.

### Defensive guard — `cmd/inbox/main.go`
Before the conversation upsert: if `workspace_id` or `contact_id` is empty,
log the full context and ACK-drop instead of NAK-looping. (Belt-and-suspenders;
the actual bug was the RETURNING clause, but the guard prevents any future
empty-key NAK storm.)

---

## Live verification

After deploy (inbox + mbs rebuilt, migration applied), the 4 previously-stuck
inbound messages redelivered and flowed through. DB ground truth:

**Conversation (one, unified):**
```
channel=mbs  mbs_thread_id=1127921160404565  status=unassigned
contact: +6281290928464  "S A"   ← real phone/name, matches Meta UI
```
**Messages:**
```
inbound  delivered  "Halo gan"  mid.$cAAABfUHQkGCktU2w8GehAhgUORvK
inbound  delivered  "Baik gan"  mid.$cAAABfUHQkGCktRDojmeg8uR6LRRy
```
Admin echoes ("test", "Halo apa kabar") correctly dropped (not in conversation).
No more `invalid input syntax for type uuid` / `failed to find/create` errors.
Remaining log warns are the cold-first-poll empty-thread batch, correctly
ACK-dropped as un-keyable.

---

## Hostile audit

**V1 — `COALESCE(uuid,'')` also latent in the WA path.** CLOSED: fixed both
occurrences (lines ~641 WA, ~710 MBS). WA never threw live because `wa_number_id`
is part of its conflict key (always non-NULL) AND its unit tests use mocks that
never execute real SQL — so the bug was invisible until MBS inbound hit it.
**Lesson (recorded):** mock-only store tests don't catch SQL type errors; new
SQL needs a live/integration smoke.

**V2 — Multi-customer / multi-thread payloads.** The join finds the thread block
whose participant set contains the message's senderFBID. With N threads in one
poll batch, each message resolves to its own thread independently. Validated on
the single live thread; the parser logic is per-thread-block so it generalizes.
CARRIED: live multi-thread capture not yet taken (only one active customer today).

**V3 — Self-FBID derivation fails for a single-thread payload.** SelfFBID =
intersection of participant sets across threads. With exactly one thread the
intersection is that thread's full participant set (both FBIDs), so direction
detection could be ambiguous. CLOSED for the live case: the snapshot carried
enough threads to disambiguate; the message-direction gate also drops messages
whose senderFBID equals the resolved self. CARRIED: harden single-thread
self-FBID via the session's stored actor id if a future capture shows ambiguity.

**V4 — Duplicate inbound on poll re-read.** SnapshotPoll re-reads db130 every
10s; same message can surface twice. CLOSED: `CreateMbsMessage` is `mbs_mid`-keyed
(idempotent upsert), so a re-read is a no-op. Confirmed: redelivery of the 4
stuck messages produced exactly 2 conversation messages, no dupes.

**V5 — Cold-first-poll empty-thread batch.** On reconnect the first poll can fire
before creds/threads are ready, yielding `thread_id=""` `sender_fbid=0`. CLOSED:
the inbox un-keyable guard ACK-drops these (no NAK loop), and the next poll
resolves correctly.

**V6 — Phone normalization mismatch.** Contacts are stored E.164 *with* `+`
(`+6281290928464`); the MBS path strips `+`. CLOSED: `processMbsInbound` retries
`FindContactByPhone("+"+senderPhone)` on miss (WA-parity), which hits the stored
`+`-form. (This was my initial wrong hypothesis for the empty-UUID — the retry
works fine; the real bug was the RETURNING clause.)

---

## Carried gaps
- **TR-G1:** live multi-thread capture to validate the per-thread join at N>1.
- **TR-G2:** harden single-thread SelfFBID derivation against ambiguity.
- Env-gated diagnostic dump (`MBS_POLL_DUMP_PATH`) left in place (off by default)
  — remove or formalize before the next clean release.

---

## Files touched (uncommitted)
- `re/mbs/mbs-native/fb/snapshot.go` (new) + `snapshot_test.go` (new) + fixture
- `internal/mbs/session/inbox_parser.go` (rewire parseSnapshotPoll)
- `internal/mbs/session/*` (`InboundDelta.SenderFBID`)
- `cmd/inbox/main.go` (enrichment + defensive guard)
- `internal/inbox/handler/store.go` (`GetPhoneByMbsThread` + 2× `::text` fix)
- `internal/inbox/handler/store.go` interface + mocks; cmd/inbox fakes
- `migrations/inbox/000005_*` (reverse index)
