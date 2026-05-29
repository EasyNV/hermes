# Stage E3 Chunk 2 — Hostile-Eyes Audit (Store Layer MBS Extension)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `internal/inbox/handler/store.go` MOD — Row struct extensions, Store interface (+1 sig change, +4 new methods), 7 SELECT projections extended, 4 new PgStore implementations
- `internal/inbox/handler/handler.go` MOD — row-to-proto extensions + `strToInboxChannel` helper + 1 caller update
- `internal/inbox/handler/handler_test.go` MOD — mockStore signature + 4 new fn fields + 4 new method impls + 4 new unit tests

---

## Findings

### F1 (P1 — VERIFIED) — Partial-index conflict target on FindOrCreateConversation

`FindOrCreateConversation` previously used `ON CONFLICT (workspace_id, contact_id, wa_number_id)`. After chunk 1, the prior unconditional UNIQUE was replaced with a partial unique index `uq_conversations_wa (workspace_id, contact_id, wa_number_id) WHERE channel = 'wa'`. Postgres requires the partial-index WHERE clause to be repeated on the `ON CONFLICT` target for inference.

**Mitigation:** Updated to `ON CONFLICT (workspace_id, contact_id, wa_number_id) WHERE channel = 'wa'`. INSERT explicitly sets `channel='wa'`. Tested via go build + the test suite (mockStore exercises the code path; live DB smoke deferred to E3.1-G1).

### F2 (P1 — VERIFIED) — Same for FindOrCreateMbsConversation

`ON CONFLICT (workspace_id, mbs_session_uid, mbs_thread_id) WHERE channel = 'mbs'` matches `uq_conversations_mbs` partial unique index from chunk 1. INSERT sets `channel='mbs'`. **Verified.**

### F3 (P2 — ACCEPTED) — `ListConversations` signature change

Existing signature gained `channel` arg before `sortOrder`. Risk: any out-of-tree caller breaks. **Verified:** repo grep shows only one call site (handler.go), updated in this chunk. Mock store + tests likewise updated. **Accepted.**

### F4 (FP) — SELECT projection drift between ListConversations and GetConversation

Both now project the same 18-column shape ending in `channel, mbs_session_uid, mbs_thread_id, mbs_page_id`. Scanners aligned. **False positive.**

### F5 (P2 — ACCEPTED) — `COALESCE(wa_number_id, '')` in WA SELECT projections

After chunk 1, `wa_number_id` is nullable. WA rows always have it populated; MBS rows have NULL. Projecting NULL into a `string` field would panic at scan time. All SELECTs that return wa_number_id now COALESCE to empty string. **Accepted; consistent.**

### F6 (P2 — DOCUMENTED) — `mbs_page_id` upsert merge logic in FindOrCreateMbsConversation

ON CONFLICT DO UPDATE for an existing MBS conversation runs:
```sql
mbs_page_id = COALESCE(NULLIF(EXCLUDED.mbs_page_id, ''), conversations.mbs_page_id)
```
This preserves the original `mbs_page_id` if the incoming event has empty page_id (rare but possible for thread-only notifications). **Documented; intentional.**

### F7 (FP) — `isNew` window race with high-throughput inserts

2-second window mirrors the WA convention. Mathematical worst case: two concurrent inserts within 2s of an existing row's `created_at` both return `isNew=true`. Acceptable false-positive rate for notification-dispatch dedup decisions. **False positive; matches existing posture.**

### F8 (P2 — ACCEPTED) — `GetMessageByMbsMID(mbsMID="")` returns ErrNotFound

Defensive guard: empty input returns ErrNotFound directly (skip DB round-trip). Caller code in E3.4 outbound consumer treats `ErrNotFound` as "not an inbox message — probably a campaign send; ignore". Same semantics. **Accepted.**

### F9 (FP) — `mockStore.findOrCreateMbsConversationFn` default fallback creates a real-looking row

When unset, the mock returns a synthetic row with sensible defaults (channel="mbs", isNew=true). This matches the existing `mockStore` patterns. Tests that need specific behavior set the fn explicitly. **False positive.**

### F10 (P2 — DOCUMENTED) — `CreateMbsMessage` initial-status logic for outbound

`pending` for outbound mirrors WA. The chunk-4 outbound consumer transitions to `sent`/`delivered`/`failed` via the `UpdateMbsMessageStatus` path. Status transition guard (`IsForwardTransition`) carries over from `conversation/state.go`. **Documented.**

### F11 (P2 — ACCEPTED) — `UpdateMbsMessageStatus(mbsMID="")` returns ErrNotFound

Same defensive posture as F8. Defensive against publisher misconfig. **Accepted.**

### F12 (FP) — Tests use `time.Now()` not a fixed clock

Reduces flake to zero in practice — both calls happen in the same goroutine, no time-based assertions on the rows. **False positive.**

### F13 (FP) — `conversationRowToProto` panic on nil receiver

The handler code never passes nil. Existing tests don't probe that path; the existing helper has the same untested-nil semantics. **False positive; consistent with prior posture.**

### F14 (P2 — DOCUMENTED) — Existing fuzzy `patch` tool nearly overwrote the not-mocked error

While patching `ListConversations` mock impl, the patch tool's fuzzy matcher initially returned `nil, 0, nil` instead of preserving the existing `fmt.Errorf("ListConversations not mocked")` sentinel. Caught and restored in a follow-up patch. **Process lesson; documented.**

### F15 (FP) — `import "time"` not needed in handler_test.go

`time` was already imported. New tests use `time.Now()` without a new import. **False positive.**

### F16 (P2 — DOCUMENTED) — No live-DB test for partial-index ON CONFLICT semantics

The two `ON CONFLICT … WHERE channel = 'wa' | 'mbs'` constructs are Postgres-specific and unit tests don't exercise them. Live-DB validation deferred to E3.1-G1 (operator-run smoke). **Documented.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `go build ./internal/inbox/...` | ✓ clean |
| `go build ./cmd/inbox ./cmd/gateway ./cmd/mbs` | ✓ clean |
| `go vet ./internal/inbox/...` | ✓ clean |
| `go test -race -count=1 ./internal/inbox/...` | ✓ all green |
| 4 new unit tests pass | ✓ |
| Existing 30+ tests unchanged behaviorally | ✓ |
| Row struct fields scanned by every SELECT path | ✓ verified by enumeration |
| Mock store interface compliance | ✓ go build catches drift |

---

## Carrying gaps tracked

| Gap | Status | Resolve in |
|---|---|---|
| E3.1-G1 (live-DB migration smoke) | Open | Sam pre-merge |
| E3.1-G2 (older TS deserialize undefined) | Open | E3.5 |
| C1-G2, C2-G1, C3-G1, C4-G1, C5-G1 | Open | Stage F |
| **E3.2-G1 (NEW)** | Partial-index ON CONFLICT not unit-testable without live DB; smoke required | Pre-merge |

---

## Approval

Chunk 2 is GO.

- **0 P1 unresolved** (F1+F2 verified)
- **8** P2 accepted/documented
- **6** FP
- **1** new carrying gap (E3.2-G1)

— Oracle, 2026-05-29
