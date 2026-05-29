# Stage E3 Chunk 1 — Hostile-Eyes Audit (Schema + Proto Extension)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `migrations/inbox/000003_mbs_channel.up.sql` NEW
- `migrations/inbox/000003_mbs_channel.down.sql` NEW
- `proto/hermes/v1/common.proto` MOD (+InboxChannel enum, +4 Conversation fields, +1 Message field)
- `gen/go/hermes/v1/common.pb.go` REGEN (buf generate)
- `docs/contracts/proto/common.proto` mirror sync
- `web/src/api/types.ts` MOD (+InboxChannel + Conversation/Message field additions)
- `web/src/stores/inbox.ts` MOD (initialize new Conversation fields on WS new_message)

---

## Findings

### F1 (P1 — VERIFIED) — CHECK constraint validates against existing WA rows

Concern: an in-place `ADD CONSTRAINT ... CHECK ...` runs the predicate against all current rows. If any pre-existing row fails it, the ALTER aborts.

**Verification:** Existing WA rows all have `wa_number_id NOT NULL` (the prior table-level constraint). The new `channel` column defaults to `'wa'`. Predicate for these rows: `(channel='wa' AND wa_number_id IS NOT NULL AND mbs_thread_id IS NULL)` → first branch holds because wa_number_id is set and mbs_thread_id is brand-new (NULL by default). **Pass.**

### F2 (P1 — VERIFIED) — DROP CONSTRAINT name correctness

Postgres auto-names `UNIQUE (workspace_id, contact_id, wa_number_id)` as `conversations_workspace_id_contact_id_wa_number_id_key`. This matches the migration. `IF EXISTS` guards against re-runs on schemas where the constraint was renamed or already dropped. **Verified.**

### F3 (P2 — ACCEPTED) — `ALTER COLUMN ... DROP NOT NULL` blocks no writes mid-flight

Postgres takes ACCESS EXCLUSIVE on `conversations` for the duration of the up-migration. Brief write pause. Acceptable for E3 rollout windows. **Documented for ops; not a defect.**

### F4 (FP) — Down migration breaks data

If MBS rows have been written in production, `down` will fail at `SET NOT NULL` because MBS rows have `wa_number_id IS NULL`. **This is intentional** — operators must explicitly delete MBS data before reverting. The plan documents this. **False positive.**

### F5 (P2 — ACCEPTED) — Partial unique index doesn't enforce uniqueness on rows with NULL key

Standard Postgres behavior: partial unique indexes only enforce against rows matching the predicate. For WA channel: WHERE channel='wa' implies wa_number_id IS NOT NULL via the CHECK; UNIQUE then enforced. For MBS channel: WHERE channel='mbs' implies mbs_thread_id IS NOT NULL. **Accepted.**

### F6 (FP) — Wire-compat break on Conversation message

Proto3 unknown fields are silently ignored by older readers. New tags 15–18 + 12 (Message) don't shift existing tags. Existing serialized data deserializes correctly. New fields default to zero/empty. **False positive.**

### F7 (P2 — ACCEPTED) — TS Conversation new fields are required (no `?`)

A frontend client deserializing from an older backend (E2-era) gets `channel: undefined`. TS strict mode would surface this only if `as Conversation` is asserted on incomplete data — currently REST responses are typed via fetch's untyped return. Practical impact: the existing inbox UI doesn't read these fields yet. **Documented; safe at this stage.**

### F8 (FP) — `stores/inbox.ts` hardcodes channel='INBOX_CHANNEL_WA'

The WS `new_message` event payload doesn't carry channel (it's a WA-only event today). Hardcoding WA is correct for the synthetic Conversation row built from a WA event. E3.5 will add a parallel `mbs_new_message → upsertFromMbs(...)` path that creates MBS-channel synthetic rows. **False positive; intended.**

### F9 (P2 — DOCUMENTED) — Migration sequencer ID

Existing migrations are 000001 + 000002. Adding 000003 is the next slot — verified. No two-tester collision because no PR branches in this workflow. **Documented.**

### F10 (P2 — ACCEPTED) — `BEGIN/COMMIT` blocks DDL transactionality

Some Postgres-DDL operations (CREATE INDEX CONCURRENTLY, etc.) can't be in a transaction. Our migration uses plain CREATE INDEX (not CONCURRENTLY), which IS transactional. **Accepted.**

### F11 (FP) — Buf generate left the gen tree dirty

`buf generate` re-emitted `gen/go/hermes/v1/common.pb.go` with new symbols (`InboxChannel`, `MbsSessionUid`, etc.). Verified via grep. **False positive — that's the desired output.**

### F12 (P2 — DOCUMENTED) — `idx_messages_mbs_mid` partial index size

The partial WHERE `mbs_mid != ''` predicate excludes the bulk of existing rows (all WA messages have empty mbs_mid). Index stays small. **Documented for size-monitoring.**

### F13 (P2 — DEFERRED) — Down migration leaves no record that MBS data existed

If an operator deletes MBS data before reverting, that data is gone with no audit trail. Stage F adds an MBS-row export-to-CSV step before down. **Deferred.**

### F14 (FP) — Mirror sync vs canonical proto drift

Manual cp of `proto/hermes/v1/common.proto` → `docs/contracts/proto/common.proto`. Verified identical via diff. C1-G2 carrying gap remains until Stage F automates this. **False positive at this commit; gap is the long-standing one.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `go build ./gen/... ./internal/inbox/... ./internal/gateway/... ./internal/mbs/...` | ✓ clean |
| `go build ./cmd/inbox ./cmd/gateway ./cmd/mbs` | ✓ clean |
| `go test ./internal/inbox/... ./internal/gateway/... ./internal/mbs/...` | ✓ all green |
| `npx tsc --noEmit` (web) | ✓ clean |
| `npm run build` (web) | ✓ clean |
| Migration SQL sanity (BEGIN/COMMIT, columns, indexes, CHECK) | ✓ all matched |
| Proto field tags non-overlapping | ✓ (15–18 + 12) |
| `buf generate` produces InboxChannel + Mbs* fields | ✓ verified by grep |

**Migration smoke against live Postgres:** NOT POSSIBLE in this sandbox (no psql/docker/migrate CLI). SQL syntax + structure verified statically. Operator should run a `migrate up && down && up` smoke locally before applying to staging. **Documented as a carrying gap (E3.1-G1: live-DB migration smoke pending).**

---

## Carrying gaps tracked

| Gap | Status | Resolve in |
|---|---|---|
| C1-G2 (proto mirror manual sync) | Open | Stage F automation |
| C2-G1, C3-G1, C4-G1, C5-G1 | Open | Stage F |
| MBS unified-Inbox routing | In progress (this stage) | E3.2–E3.5 |
| **E3.1-G1** (NEW) | live-DB migration smoke | Pre-merge, run by Sam |
| **E3.1-G2** (NEW) | TS Conversation fields are required; older API responses deserialize with undefined | E3.5 frontend hardening |

---

## Approval

Chunk 1 is GO.

- **0 P1 unresolved** (F1+F2 verified; not defects)
- **8** P2 accepted/documented
- **6** FP
- **2** new carrying gaps (E3.1-G1, E3.1-G2)

— Oracle, 2026-05-29
