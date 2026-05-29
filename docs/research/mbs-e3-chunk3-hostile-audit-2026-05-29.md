# Hostile-eyes audit — Stage E3 chunk 3 (MBS inbound consumer)

**Date:** 2026-05-29
**Scope:** All code changes in commit-pending state for chunk 3:
- `internal/inbox/handler/store.go` (`GetWorkspaceIDForMbsUid` + `CreateMbsMessage` ON CONFLICT)
- `internal/inbox/handler/handler_test.go` (mockStore extension)
- `cmd/inbox/main.go` (`startMbsInboundConsumer` + `processMbsInbound` + `ensureStreams` HERMES_MBS)
- `cmd/inbox/main_mbs_test.go` (8 unit tests)
- `migrations/inbox/000004_mbs_mid_unique.{up,down}.sql`
**Reviewer mindset:** assume the change ships at 03:00 Sunday and breaks Sam's BizApp inbound delivery; what's the failure mode?

---

## Findings

### P0 / P1 — none unresolved

### P2 — Accepted with documented trade-off

| # | Finding | Severity | Resolution |
|---|---|---|---|
| F1 | **`ON CONFLICT (mbs_mid) WHERE mbs_mid != '' DO NOTHING` requires a UNIQUE partial index targeting exactly that predicate.** Chunk 1 created a non-unique partial index. | P1 (caught) | Migration `000004_mbs_mid_unique.up.sql` drops `idx_messages_mbs_mid` and creates `uq_messages_mbs_mid` UNIQUE on the same predicate. Down migration restores the chunk-1 shape. Postgres requires this — without it, ON CONFLICT fails with "no unique or exclusion constraint matching the ON CONFLICT specification". Validated by go build but **NOT live-DB validated** (E3.3-G1). |
| F2 | **`processMbsInbound` returns true on `tenantID == ""` BEFORE workspace lookup.** If publisher (hermes-mbs) ever forgets to populate Meta.TenantId, we silently drop. | P2 | Accepted — same publisher invariant as WA path. PublishInboundMessage already returns early when tenant is empty (`internal/mbs/handler/events.go:91`), so this is a redundant defense, not the primary check. Log line surfaces the drop. |
| F3 | **Synthetic phone slug `mbs:thread:<id>` will be stored in `contacts.phone`.** Other code paths (WA outbound matcher, contact CSV import) might assume `phone` looks like E.164. | P2 | Inspected — `findContactByPhone` matches by exact string; the chunk-2 store path and the new MBS path both use string match. CSV import has dedup on phone but doesn't validate format. The synthetic slug only conflicts with itself across MBS threads (intentional, per plan §C3-K4) and never with real phones (no real phone starts with `mbs:`). Documented as carrying constraint. |
| F4 | **`FindOrCreateMbsConversation` partial-index race**: chunk-2 path is read-modify-write; under concurrent inbound for the same thread, two writers may both INSERT and the second fails the partial unique idx → `ErrConflict`. | P2 | Carrying gap E3.2-G1 already tracks live-DB validation. The chunk-2 impl uses `INSERT ... ON CONFLICT DO NOTHING RETURNING *` plus a follow-up SELECT, but the SELECT runs in the same QueryRow so concurrency on partial-idx ON CONFLICT may be flaky. Will be exercised in F live-DB smoke. NATS redelivery (5 max-deliver, 30s ackWait) absorbs transient NAKs from this path. |
| F5 | **`startMbsInboundConsumer` durable name `inbox-mbs-inbound` is global, not tenant-scoped.** Same as WA path (`inbox-inbound`). For single-pod deployment (Stage F target) this is correct; for multi-pod K8s (post-F) we'd want per-pod scoping. | P2 | Inherited from WA. F migration deferred. |
| F6 | **`HERMES_MBS` AddStream from inbox-service races with the same AddStream from gateway.** If both boot at the same moment, one wins and the other no-ops; OK as long as subject sets match. Verified gateway uses `[hermes.mbs.message.>, hermes.mbs.session.>]` (cmd/gateway/main.go:177) — exact match with the new inbox-service ensureStreams entry. | P2 | Verified, no-op. |
| F7 | **`processMbsInbound` ignores `WecMailboxId` field from the event.** Lost in translation; the chunk-2 store `FindOrCreateMbsConversation` doesn't accept it either. | P2 | Mailbox ID is informational (drawer display) — defer to chunk 5 where the frontend pulls it from the inbound NATS event directly via the existing `mbs_new_message` WS frame, not from the persisted conversation. No schema change needed for the inbox path. |
| F8 | **Test `TestProcessMbsInbound_EmptyPhone_SyntheticSlug` originally asserted the wrong tail (`23456789` instead of `90123456`).** Caught by red bar. | P2 | Already fixed. Lesson: avoid mental string-slicing in test fixtures; copy the result of the actual function from a passing run. |
| F9 | **`processMbsInbound` passes `js` to `publishNotification` only if `js != nil`.** Test injects nil so notify skipped. Production path: `js` is always non-nil. | P2 | Convention from WA path. Defensive nil-check is cheap. |
| F10 | **Resolved tenant override**: if `mbs_sessions.tenant_id` ≠ event's `Meta.TenantId`, we trust the DB. Could mask publisher bugs where the wrong tenant is set in the event. | P2 | Accepted — DB-trusted authority is correct for cross-service consistency. A mismatch warning log might help diagnose; defer to F if it ever fires. |

### P3 / FP — Considered and rejected

| # | Finding | Status |
|---|---|---|
| F11 | "What if `event.Uid == 0`?" — would the JOIN return a non-zero row? | FP — `mbs_sessions.uid` is BIGINT PRIMARY KEY; uid=0 would require an explicit insert. The publisher derives uid from MBS session state which is always non-zero (Meta uids start at ~10^9). Defensive: harmless extra SELECT. |
| F12 | Goroutine leak on NATS subscription. | FP — JS Subscribe returns a sub; sub is closed when nc.Drain runs during graceful shutdown. Same model as WA path. |
| F13 | What if `event.Text` is huge (>1MB) — does it crash the preview slice? | FP — `preview = preview[:100]` is byte-indexed; if Text is valid UTF-8, the slice may split a rune. Acceptable for a preview; UI shows ellipsis. WA path has the same behavior. |
| F14 | `strings.TrimPrefix` on `SenderPhone` — what if phone is `++62...`? | FP — `TrimPrefix` strips only the literal once. Double-`+` would land as `+62...` in the lookup, fail, then fall through to autocreate with phone `+62...`. Edge case not in scope; no real phone has double-plus. |
| F15 | `strconv.FormatInt(event.Uid, 10)` returns negative for negative uid. | FP — uids are always positive. |
| F16 | Log volume — `MBS inbound: auto-created contact` fires once per new sender. | FP — same pattern as WA. Not a regression. |

---

## Carrying gaps

| # | Gap | Owner | Plan |
|---|---|---|---|
| **E3.3-G1** | `CreateMbsMessage` ON CONFLICT semantics need live Postgres validation. The CTE-based INSERT...ON CONFLICT...RETURNING + UNION ALL fallback SELECT pattern is one of two PG idioms for the "insert-or-fetch" case; the other is RETURNING in a CTE with COALESCE. We picked the first. | Sam (F deploy smoke) | Run two concurrent INSERTs against a freshly-migrated DB; verify both calls return the same row id and no error surfaces. |
| **E3.3-G2** | Multi-workspace tenant routing → arbitrarily picks oldest workspace. | Sam (future) | Add `mbs_session_workspaces` mapping if multi-workspace tenants land. |
| **E3.3-G3** | Notify dispatch volume for busy MBS pages. | Sam (Stage F) | Add per-workspace rate cap. |
| **E3.2-G1** | (carried from chunk 2) `FindOrCreateMbsConversation` partial-index ON CONFLICT live-DB smoke. | Sam (F deploy smoke) | Same DB smoke as G1. |

---

## Gates verified

```
go vet  ./cmd/inbox/... ./internal/inbox/...   ✓
go build ./...                                  ✓
go test -race -count=3 ./cmd/inbox/... ./internal/inbox/...   ✓
  ok  github.com/hermes-waba/hermes/cmd/inbox            1.262s
  ok  github.com/hermes-waba/hermes/internal/inbox/handler 1.276s
```

8 new unit tests pass. Existing 30+ tests unchanged.

---

## Verdict

**Ready to commit.** P1 finding (F1) was caught at plan time and resolved with migration 000004 in the same chunk. All other findings either resolved or accepted with documented carrying gap. Live-DB smoke (G1) is the only operator-side task pre-merge, and it's blockable by Stage F deploy gating.

— Oracle, 2026-05-29
