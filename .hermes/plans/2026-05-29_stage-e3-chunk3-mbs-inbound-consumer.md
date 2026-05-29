# Stage E3 chunk 3 — `hermes-inbox` MBS inbound consumer

**Date:** 2026-05-29
**Author:** Oracle
**Builds on:** E3 chunk 1 (`cea5974` schema) + E3 chunk 2 (`1aa234d` store extension)
**Effort:** ~3h
**Target LOC:** ~280 (`cmd/inbox/main.go` + supporting store method + 1 new unit test file)

---

## Goal

Subscribe `hermes-inbox` to `hermes.mbs.message.inbound.*` so that every customer message that arrives via the MQTToT broker lands in `inbox.conversations` / `inbox.messages` with `channel='mbs'`. The existing `/inbox` UI then renders these threads alongside WA conversations once chunk 5 wires the WS bridge.

Concretely, the new consumer:

1. Decodes `MbsInboundMessageEvent` (proto field shape already settled).
2. Resolves `(tenant_id, workspace_id)` from the event's `uid` via a new store helper (`GetWorkspaceIDForMbsUid`) — JOIN on `mbs_sessions`.
3. Finds or auto-creates a `ContactRow` keyed by `senderPhone` (E.164 minus `+`). Empty-phone case (Messenger user without a WA number) creates an `Unknown sender` contact with a synthetic phone slug — see §Contact identity below.
4. (Allowlist parity with the WA consumer is **NOT** applied for MBS — see §Allowlist scope.)
5. Calls `store.FindOrCreateMbsConversation(workspaceID, contactID, sessionUID, threadID, pageID)`.
6. Reopens the conversation if closed (same `StatusAfterInbound` path as WA).
7. Calls `store.CreateMbsMessage(convID, "inbound", text, mid)`.
8. Updates `last_message_at` + preview.
9. If conversation is `unassigned`, publishes a `NotifyDispatchEvent` (mirroring the WA path).
10. ACKs the NATS message. NAKs on transient DB errors.

The HERMES_MBS stream already exists (created by `hermes-gateway` ensureStreams in E2-C3); inbox-service binds a fresh durable consumer (`inbox-mbs-inbound`) — no new stream.

---

## Non-goals (deferred to other chunks)

- Send routing (chunk 4): `InboxSendMessage` switches on `conversation.channel` and publishes to `hermes.mbs.send.manual.<tenant>` for MBS rows.
- Outbound status reconciliation (chunk 4): subscribe `hermes.mbs.message.outbound.*` → `UpdateMbsMessageStatus`.
- Frontend WS bridge / channel badge (chunk 5).

This chunk only writes inbound rows.

---

## Contracts

### C3-K1 — New store method

```go
// internal/inbox/handler/store.go (Store interface, new method)

// GetWorkspaceIDForMbsUid resolves (workspaceID, tenantID) for a given
// MBS session uid by joining mbs_sessions.tenant_id with workspaces.
// Returns the workspace with the smallest created_at — the "default"
// workspace for the tenant. Multi-workspace tenants today have exactly
// one workspace per tenant; this picks the deterministic one even if
// that changes.
//
// Returns (workspaceID, tenantID, error). On miss returns ErrNotFound.
GetWorkspaceIDForMbsUid(ctx context.Context, uid int64) (string, string, error)
```

PgStore implementation:

```sql
SELECT w.id, w.tenant_id
FROM mbs_sessions s
JOIN workspaces  w ON w.tenant_id = s.tenant_id
WHERE s.uid = $1
ORDER BY w.created_at ASC
LIMIT 1
```

`mbs_sessions` and `workspaces` are in the same Postgres DB (single `hermes` DB, per `docker-compose.yml`). The JOIN does not cross a service boundary at the Postgres layer.

### C3-K2 — Consumer signature

```go
func startMbsInboundConsumer(
    js  natsgo.JetStreamContext,
    store handler.Store,
    log zerolog.Logger,
) error
```

Wired from `main()` next to the existing WA inbound consumer.

NATS subscription:

| Field | Value |
|---|---|
| Subject | `hermes.mbs.message.inbound.*` |
| Stream | `HERMES_MBS` (created by gateway in E2-C3) |
| Durable | `inbox-mbs-inbound` |
| Ack | manual, 30s ack wait, 5 max-deliver |
| Concurrency | default (1 in-flight; same as WA inbound) |

### C3-K3 — Notification publish parity

`publishNotification` is reused as-is. The WA path passes `contact.Phone` as the title; MBS uses the same path with the resolved (or synthetic) contact.

### C3-K4 — Contact identity (MBS-specific)

| Input | Behaviour |
|---|---|
| `senderPhone != ""` | Try exact + `+`-prefixed; fall back to `AutoCreateContact(tenantID, senderPhone, "")`. |
| `senderPhone == ""` | Use synthetic phone `mbs:thread:<threadID>`. Try lookup; auto-create with name `MBS thread <threadID-tail-8>` if missing. **Guarantees a stable contact_id for the same MBS thread.** |

Auto-create errors are NAKed (consistent with WA path). The synthetic-phone slug is recorded in `contacts.phone`. (Long-term, we'd add a `kind` discriminator to `contacts`; today the phone column accepts arbitrary strings so this is the path of least disruption.)

### C3-K5 — Allowlist scope

**MBS does NOT consult the contact allowlist.** Rationale:

1. WA allowlist exists because outbound spam from a campaign-driven WA number is a primary ban risk; pre-flighting unknown senders prevents accidental bot-style replies. For MBS, the human BizApp operator initiates threads (cold-compose) or the customer DM's the page directly — there is no spam-from-Hermès attack surface for inbound. Inbound to a page is implicit consent.
2. The allowlist is workspace-scoped on phone. MBS threads can predate the customer ever having a WA number on file. Filtering them out would silently drop legitimate page DMs.
3. We can revisit if Sam wants MBS-side allowlist in F.

Documented as an intentional difference, not an omission.

### C3-K6 — Reopen behaviour

Same as WA: if conversation status is `closed`, call `ReopenConversation`. `StatusAfterInbound` is channel-agnostic (it only reads the current status). No new helper needed.

### C3-K7 — Unassigned notify

Same as WA: if conversation is `unassigned`, publish to `hermes.notify.dispatch.<tenant>` with category `NEW_MESSAGE`.

### C3-K8 — Error semantics

| Error | Action |
|---|---|
| Proto unmarshal fails | log + ACK (poison pill) |
| `tenant_id` missing in event meta | log warning + ACK (publisher bug, drop) |
| Workspace lookup miss (session burned/missing) | log warning + ACK (no row to write) |
| Contact lookup AND auto-create both fail | NAK |
| `FindOrCreateMbsConversation` returns error | NAK |
| `CreateMbsMessage` returns error | NAK |
| Duplicate mbs_mid (rare — Meta retransmit) | swallowed inside `CreateMbsMessage` via `ON CONFLICT DO NOTHING` returning the existing row; consumer treats as no-op success |

Last point is **NOT YET WIRED** — chunk 2 `CreateMbsMessage` does not have an `ON CONFLICT` clause. **See open-issue G1 below.**

---

## File-level plan

| # | File | Change | LOC |
|---|---|---|---|
| 1 | `internal/inbox/handler/store.go` | Add `GetWorkspaceIDForMbsUid` to `Store` iface + `PgStore` impl | ~20 |
| 2 | `internal/inbox/handler/handler_test.go` | Add `getWorkspaceIDForMbsUidFn` to `mockStore` + impl + closer assertions | ~15 |
| 3 | `cmd/inbox/main.go` | Add `startMbsInboundConsumer`; wire from `main()` | ~190 |
| 4 | `cmd/inbox/main_mbs_test.go` (new) | Unit test consumer with fake JS + store mocks; covers ack/nak/synthetic-phone/closed-reopen paths | ~280 |

`cmd/inbox/main_test.go` already exists but mocks WA paths only — new file keeps the MBS-specific test setup colocated.

---

## Implementation steps

### Step 1 — `store.GetWorkspaceIDForMbsUid`

Add to `Store` interface (after `GetWorkspaceIDForWaNumber`). PgStore method below the existing helper.

### Step 2 — `mockStore` extension

Add field + impl + update existing nil checks (none should break — additive).

### Step 3 — `startMbsInboundConsumer`

Structure mirrors `startInboundConsumer` line-for-line so the diff is reviewable side-by-side. Differences:

- decodes `MbsInboundMessageEvent` not `WaInboundMessageEvent`
- tenant comes from `event.Meta.GetTenantId()` (not derived from wa_number)
- workspace from `GetWorkspaceIDForMbsUid(event.Uid)`
- no allowlist check
- `FindOrCreateMbsConversation` not `FindOrCreateConversation`
- `CreateMbsMessage` not `CreateMessage`
- skip body=="" → mediaPtr handling (Meta sends text only in MVP per E1-C5 publisher contract)
- log keys use `mbs_thread_id` + `mid` instead of `wa_message_id`

### Step 4 — Wire from `main()`

```go
if err := startMbsInboundConsumer(js, store, log); err != nil {
    log.Fatal().Err(err).Msg("failed to start MBS inbound consumer")
}
```

### Step 5 — Test file

Five scenarios:

1. Happy path: real phone → AutoCreateContact succeeds → conversation created → message stored → ack.
2. Workspace lookup miss → ack with no DB write.
3. Empty senderPhone → synthetic `mbs:thread:<id>` phone → contact created.
4. Closed conversation reopens.
5. CreateMbsMessage transient error → nak.

Each scenario uses a `mockJS` + an in-memory `mockStore` (extending the existing fake). The test verifies ack/nak counts and side-effect captures.

---

## Verification gates

```
go vet  ./internal/inbox/... ./cmd/inbox
go build ./internal/inbox/... ./cmd/inbox
go test -race -count=3 ./internal/inbox/... ./cmd/inbox
```

All four must pass before commit. Existing WA-side tests must remain green.

---

## Hostile-eyes pre-audit (caught at plan stage)

| # | Issue | Resolution |
|---|---|---|
| C3-P1 | **Duplicate Meta MID on retransmit.** Meta retransmits a delta during connection loss/recovery; `CreateMbsMessage` lacks `ON CONFLICT`, so a duplicate MID would surface as a constraint violation. The partial unique index `WHERE mbs_mid != ''` already exists from chunk 1. | **Carrying gap E3.3-G1** — patch `CreateMbsMessage` to `INSERT ... ON CONFLICT (mbs_mid) WHERE mbs_mid != '' DO NOTHING RETURNING *`, then re-SELECT on no-row. This chunk patches `CreateMbsMessage` inline (within the chunk-3 commit, scoped to the impl change). |
| C3-P2 | **Empty-phone slug collides if two MBS threads share an ID across sessions.** Unlikely (MBS thread_id is globally unique in Meta's space) but documented. Slug includes `threadID` only, no session_uid prefix. | Accepted — Meta thread IDs are global. If not, partial-unique on `(workspace, mbs_session_uid, mbs_thread_id)` (chunk 1) prevents conversation collision; only the contact gets shared, which is OK (one synthetic contact per thread). |
| C3-P3 | **`GetWorkspaceIDForMbsUid` returns a deterministic workspace for tenants with multiple workspaces, but the first workspace might not be the "right" one.** | Today (per gateway init), each tenant has 1 workspace seeded in `gateway/000001_init`. Multi-workspace tenants are not in scope for E3. If they arrive, add an explicit `mbs_session_workspaces` mapping table; that's an additive migration. Documented in success criteria. |
| C3-P4 | **Notify dispatch fires on every MBS unassigned message.** WA path already does this; volume risk for MBS could be different (busy pages may have hundreds/day). | Inherited from WA; not a new risk. Add per-workspace notify suppression in Stage F if needed. |
| C3-P5 | **Empty-phone synthetic slug uses `mbs:thread:` prefix that breaks `IsPhoneAllowlisted` regex assumptions if a future MBS allowlist arrives.** | Accepted — out of scope. Future MBS allowlist would key off thread_id, not phone. |
| C3-P6 | **NATS publisher (`hermes-mbs`) currently sets `wec_mailbox_id` but consumer doesn't use it.** | Accepted — chunk 5 may display it in the drawer; field stays in the event payload. |
| C3-P7 | **`StatusAfterInbound` is channel-agnostic, but `ReopenConversation` might reset MBS-specific timestamps unintentionally.** | Inspected — `ReopenConversation` only flips status + sets reopened_at. Safe. |
| C3-P8 | **Concurrency on `FindOrCreateMbsConversation` under racing inbound messages for the same thread.** | The partial unique index handles this — second insert fails the index check, returns "no rows" from upsert, second-pass SELECT returns the row created by the first. PgStore impl from chunk 2 implements the read-modify-write loop; live-DB validation is carrying gap E3.2-G1. |
| C3-P9 | **Inbox-service binds to HERMES_MBS but doesn't ensure the stream first.** Gateway is the canonical creator; if gateway isn't up yet, inbox NACKs with "stream not found." | Production: compose `depends_on: [gateway]`. For test/dev, `ensureStreams` already in `cmd/inbox/main.go` adds HERMES_MBS subjects to be safe. Add to `ensureStreams`. |

Findings C3-P1 (with inline fix) and C3-P9 (with `ensureStreams` extension) get folded into the build step. Others accepted/deferred.

---

## Open issues / carrying gaps

| # | Gap | Severity | Owner | Resolution path |
|---|---|---|---|---|
| **E3.3-G1** | `CreateMbsMessage` `ON CONFLICT` semantics need a live DB verification (Postgres skips empty-MID rows from the partial unique idx — we rely on this). | M | This chunk patches code; live-DB smoke = carrying gap until F. | Validate during F deploy. |
| **E3.3-G2** | Multi-workspace tenant routing → first workspace chosen. | L | Documented constraint. | Add `mbs_session_workspaces` mapping if multi-WS tenants land. |
| **E3.3-G3** | Notify volume cap. | L | Inherited from WA. | F. |

---

## Files touched (final shape)

- `internal/inbox/handler/store.go` — +`GetWorkspaceIDForMbsUid` iface + impl; patch `CreateMbsMessage` to `ON CONFLICT (mbs_mid) DO NOTHING` + re-SELECT.
- `internal/inbox/handler/handler_test.go` — extend `mockStore` with new method.
- `cmd/inbox/main.go` — `startMbsInboundConsumer`, wire from `main`, extend `ensureStreams` with HERMES_MBS subjects.
- `cmd/inbox/main_mbs_test.go` — new test file (5 scenarios).

---

## Commit message

```
inbox(consumer): MBS inbound message consumer (Stage E3 chunk 3)

Subscribes hermes-inbox to hermes.mbs.message.inbound.* and persists
each delta as a (channel='mbs') conversation + message via the chunk-2
store extension. Reuses the WA path's reopen-on-inbound and notify-on-
unassigned semantics; skips the allowlist (intentional — see chunk-3
plan §C3-K5). Synthetic contact for empty-phone MBS threads.

Adds Store.GetWorkspaceIDForMbsUid to resolve (workspace, tenant) from
the inbound event's session uid via a JOIN on mbs_sessions ↔ workspaces.
Both tables live in the same hermes DB so this is in-process.

Patches CreateMbsMessage to ON CONFLICT (mbs_mid) DO NOTHING so a Meta
retransmit doesn't crash the consumer.

E3.3-G1: live-DB ON CONFLICT smoke deferred to Stage F.
E3.3-G2: multi-workspace tenant routing picks the oldest workspace.

Refs: .hermes/plans/2026-05-29_stage-e3-chunk3-mbs-inbound-consumer.md
```

— Oracle, 2026-05-29
