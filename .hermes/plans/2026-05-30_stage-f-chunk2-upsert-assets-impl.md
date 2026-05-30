# Stage F follow-up — Chunk 2: UpsertAssets / SetPrimaryAsset / DeleteSession store impls

**Status:** plan + contracts
**Date:** 2026-05-30
**Predecessor:** chunk 1 (tenant metadata key fix)
**Successor:** Stage G chunk 1 (TBD — first Meta-touching path that needs assets)

---

## 1. Problem

`internal/mbs/store/pg.PgStore` returns `ErrNotImplemented` for three methods declared in the `Store` interface and called from production code paths:

| Method | Caller | Failure mode today |
|---|---|---|
| `UpsertAssets(ctx, uid, []*AssetRow) error` | `importer.Run` (L317), `rpc_bridge_login.go` L522 | importer logs warn + continues; bridge login logs warn + continues |
| `SetPrimaryAsset(ctx, uid, pageID) error` | `importer.Run` L323 | importer logs warn + continues |
| `DeleteSession(ctx, uid) error` | not currently called from non-test code | latent — Stage G burn-and-rebridge flow needs it |

**Observable today:** the 61590134170831 import succeeded for the session row (auth credentials, bridge envelope, cookies all in `mbs_sessions`) but the asset map (page_id=1219576644562769, waba_id=1147297338458228, wec_mailbox_id=1153441357849273, wec_phone_number=573508814866, business_id=1655020605549323, wec_account_registered=true) **never landed in `mbs_session_assets`**:

```sql
hermes=# select count(*) from mbs_session_assets;
 0
```

Downstream consequences:
- Cold-compose composer can't auto-route messages to the right page
- Phone resolver cache lookups by `(uid, page_id, phone)` fail with no-page error
- BridgeLogin live flow fills the row but loses asset metadata on every reconnect
- `mbs-import` UX is misleading — log says "imported" with a warning

The DEK-encrypted session row IS the high-value blob (irreplaceable without re-login). Assets are derivable but expensive to re-discover (Stage B GraphQL cookie probes, rate-limited). Persisting them at import time is correct.

## 2. Spec

### 2.1 Schema (already migrated — no DDL change)

```
mbs_session_assets
  uid                       bigint NOT NULL    REFERENCES mbs_sessions(uid) ON DELETE CASCADE
  page_id                   text   NOT NULL
  page_name                 text
  business_presence_node_id text
  business_id               text
  business_name             text
  waba_id                   text
  wec_mailbox_id            text
  wec_phone_number          text
  ig_account_id             text
  is_primary                bool   NOT NULL  DEFAULT false
  wec_account_registered    bool   NOT NULL  DEFAULT false
  discovered_at             timestamptz NOT NULL DEFAULT now()
  PRIMARY KEY (uid, page_id)
  UNIQUE INDEX uniq_mbs_session_assets_one_primary ON (uid) WHERE is_primary
  INDEX idx_mbs_session_assets_primary  ON (uid) WHERE is_primary
  INDEX idx_mbs_session_assets_waba     ON (waba_id) WHERE waba_id IS NOT NULL
```

Two invariants the impl MUST preserve:
- `(uid, page_id)` is the natural key — upsert by ON CONFLICT
- AT MOST one row per uid may have `is_primary=true` (partial unique index enforces it)

### 2.2 UpsertAssets contract

```go
// UpsertAssets persists a session's asset map. Semantics:
//
//   - Per-row upsert by (uid, page_id). Existing rows are UPDATED in
//     place; new rows are INSERTed.
//   - discovered_at is PRESERVED on update (don't bump on every refresh).
//   - is_primary is overwritten by the caller's value — caller is the
//     authority. Two-phase: caller submits assets with the intended
//     IsPrimary distribution, store enforces the at-most-one invariant
//     by failing the txn if violated. SetPrimaryAsset is a separate
//     dedicated path for explicit primary-flip operations.
//   - Assets NOT present in the input are NOT deleted. UpsertAssets is
//     additive/refresh, not replace. Removal goes through DeleteSession
//     (cascade) or a future TrimAssets path.
//   - Empty `assets` slice is a no-op (return nil).
//   - All upserts run inside a single transaction. Any error rolls back.
//
// Foreign-key contract: caller MUST ensure mbs_sessions(uid) exists
// before calling (PgStore returns ErrForeignKey wrapping pgx 23503 on
// violation — importer already enforces this via CreateSession-first
// ordering).
func (s *PgStore) UpsertAssets(ctx context.Context, uid int64, assets []*AssetRow) error
```

SQL skeleton:

```sql
INSERT INTO mbs_session_assets (
    uid, page_id, page_name, business_presence_node_id,
    business_id, business_name,
    waba_id, wec_mailbox_id, wec_phone_number,
    ig_account_id, is_primary, wec_account_registered,
    discovered_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
          COALESCE($13, now()))
ON CONFLICT (uid, page_id) DO UPDATE SET
    page_name                 = EXCLUDED.page_name,
    business_presence_node_id = EXCLUDED.business_presence_node_id,
    business_id               = EXCLUDED.business_id,
    business_name             = EXCLUDED.business_name,
    waba_id                   = EXCLUDED.waba_id,
    wec_mailbox_id            = EXCLUDED.wec_mailbox_id,
    wec_phone_number          = EXCLUDED.wec_phone_number,
    ig_account_id             = EXCLUDED.ig_account_id,
    is_primary                = EXCLUDED.is_primary,
    wec_account_registered    = EXCLUDED.wec_account_registered
    -- discovered_at NOT touched — preserved from first discovery
```

### 2.3 SetPrimaryAsset contract

```go
// SetPrimaryAsset flips primary to (uid, pageID), clearing any other
// primary for the same uid. Atomic via single txn.
//
// Returns ErrNotFound if (uid, pageID) has no row.
func (s *PgStore) SetPrimaryAsset(ctx context.Context, uid int64, pageID string) error
```

SQL:

```sql
BEGIN;
  UPDATE mbs_session_assets
     SET is_primary = false
   WHERE uid = $1 AND is_primary = true AND page_id <> $2;

  UPDATE mbs_session_assets
     SET is_primary = true
   WHERE uid = $1 AND page_id = $2
   RETURNING 1;  -- caller checks tag.RowsAffected
COMMIT;
```

If `RowsAffected == 0` on the second UPDATE → `ErrNotFound`.

Race: two concurrent SetPrimaryAsset calls for different pageIDs on the same uid could both clear-then-set. The unique partial index on `(uid) WHERE is_primary` is the backstop — the second commit fails with 23505 unique violation. Wrap in retry-on-conflict at the caller for the rare case (not in scope here).

### 2.4 DeleteSession contract

```go
// DeleteSession removes the mbs_sessions row + all cascading rows
// (mbs_session_assets via FK, mbs_phone_threads if its FK cascades —
// chunk 2 verifies). Returns ErrNotFound if uid had no row.
//
// Burn flow uses UpdateSessionState→BURNED instead — DeleteSession is
// reserved for operator-initiated removal (GDPR / wrong-tenant cleanup).
func (s *PgStore) DeleteSession(ctx context.Context, uid int64) error
```

SQL:

```sql
DELETE FROM mbs_sessions WHERE uid = $1
```

Cascade does the rest (mbs_session_assets has `ON DELETE CASCADE`). Verify mbs_phone_threads FK has the same cascade — if not, document the order-of-deletes requirement and add a deferred check.

### 2.5 Importer behaviour change

Today: `UpsertAssets` returns `ErrNotImplemented` → importer warns + continues, `SetPrimaryAsset` also warns. Net: session row in DB, assets missing.

After chunk 2: `UpsertAssets` succeeds → asset row lands. `SetPrimaryAsset` succeeds (only one row, so trivially primary; UpsertAssets already wrote `is_primary=true`). Net: session + asset both in DB.

The importer's existing `--force` flag re-runs the path for already-existing sessions. Operator can run `mbs-import --force --no-publish` against `/Users/env/.mbs-native/sessions/` to backfill 61590134170831's assets without re-creating the session row (chunk 2 also gates this — re-test with force=true that asset upsert works idempotently).

### 2.6 Contracts

**C2-G1: Importer success post-chunk-2**

`mbs-import --force` for 61590134170831 results in:

```sql
SELECT page_id, page_name, business_id, waba_id, wec_mailbox_id,
       wec_phone_number, is_primary, wec_account_registered
  FROM mbs_session_assets WHERE uid = 61590134170831;

 page_id          | business_id        | waba_id           | wec_mailbox_id    | wec_phone_number | is_primary | wec_account_registered
------------------+--------------------+-------------------+-------------------+------------------+------------+------------------------
 1219576644562769 | 1655020605549323   | 1147297338458228  | 1153441357849273  | 573508814866     | t          | t
```

**C2-G2: API exposure**

`GET /api/v1/mbs-sessions/61590134170831/assets` returns the asset row in JSON shape matching `hermesv1.MbsSessionAsset` proto (already wired in `internal/gateway/handler/mbs.go::ListSessionAssets`).

**C2-G3: At-most-one-primary invariant**

Direct SQL test: insert two rows with `is_primary=true` for same uid → second insert MUST fail with `23505 duplicate key value violates unique constraint`. (PgStore wraps as `ErrConflict`.)

**C2-G4: Cascade on session delete**

```sql
INSERT mbs_sessions ...;
INSERT mbs_session_assets ... (2 rows);
DELETE FROM mbs_sessions WHERE uid = $1;
SELECT count(*) FROM mbs_session_assets WHERE uid = $1;  -- expect 0
```

### 2.7 Gates

| Gate | Description | How to verify |
|---|---|---|
| G1 | New unit tests in `internal/mbs/store/pg_assets_test.go` covering: insert / update-by-conflict / preserve-discovered_at / empty slice / FK violation / set-primary happy-path / set-primary not-found / delete-cascade | `go test ./internal/mbs/store -run TestPgStore_Assets` |
| G2 | `go test ./internal/mbs/importer/...` still green (importer now sees nil from UpsertAssets) | terminal |
| G3 | `go test ./internal/mbs/handler/...` still green (bridge-login non-fatal warn path now unreachable but kept defensively) | terminal |
| G4 | `mbs-import --force` for 61590134170831 against the running stack persists the asset row matching C2-G1 | live |
| G5 | `curl /api/v1/mbs-sessions/61590134170831/assets` returns the row through the chunk-1-fixed gateway | live |
| G6 | DB-level invariant probes (C2-G3, C2-G4) | psql |
| G7 | `grep -rn 'ErrNotImplemented' internal/mbs/store/pg.go` reports zero hits in the three methods | terminal |

### 2.8 Anti-goals

- NOT implementing UpsertPhoneThread / GetPhoneThread (Stage G phone-resolver scope)
- NOT implementing ListSessionsNeedingRefresh (Stage G refresh ticker scope)
- NO change to BridgeLogin's asset persistence pathway (it already calls UpsertAssets — the fix lets that call succeed instead of warn)
- No proto / no API shape change

### 2.9 Rollback

`git revert` on the chunk-2 commit. The session row stays (FK cascade only fires on delete, not on revert). Existing imported assets are orphaned-but-readable until the next force-import. No data loss.

## 3. Build plan

1. Read `internal/mbs/store/pg.go` end-to-end — confirm txn helper pattern + error-wrapping conventions used by `CreateSession` / `UpdateCookies` so the new methods stay stylistically consistent
2. Implement `UpsertAssets` — pgxpool txn, batched INSERTs via `pgx.Batch` or per-row Exec (per-row for clarity at this scale — N is 1–5 in practice)
3. Implement `SetPrimaryAsset` — two-statement txn, RETURNING tag for not-found detect
4. Implement `DeleteSession` — single Exec, RowsAffected for not-found, document cascade
5. Write `pg_assets_test.go` with pgxtest harness (per repo convention — check existing pgxtest setup in `internal/mbs/store/pg_test.go`)
6. `go test ./internal/mbs/store -count=1` green
7. `go test ./...` green (sanity)
8. Rebuild mbs image: `make docker-build-mbs` (PATH-prefixed)
9. Recreate hermes-mbs container: `docker-compose ... up -d --force-recreate mbs`
10. Subshell-isolated `mbs-import --force --no-publish` against `/Users/env/.mbs-native/sessions/` (force forces 1674772559 too — that one's asset map is empty so it's a no-op for assets but rewrites the session row; document the side-effect)
11. Verify C2-G1 via psql; C2-G2 via curl; C2-G3+C2-G4 via psql probes
12. Commit
13. Hostile audit doc

## 4. Risk

| Risk | Probability | Mitigation |
|---|---|---|
| pgxtest harness needs DB setup not wired in CI | medium | Inspect `pg_test.go` for existing pattern; reuse |
| `is_primary` unique partial index races on concurrent UpsertAssets | low | Document; caller (importer + bridge-login) is serial per uid |
| `mbs-import --force` rewrites a session we didn't intend to touch | low | Use a UID-filter? No — chunk 2 should NOT extend importer flags; instead, document that --force is whole-dir. Future chunk can add `--only-uid` if needed |
| `mbs_phone_threads` FK doesn't cascade | low | Verify in `\d mbs_phone_threads`; if not cascading, document the manual cleanup order required for DeleteSession |
| Force-import overwrites a re-keyed session and we lose the post-import bridge state | medium | Importer overwrites with the FILE's state which is the same as what's on disk; bridge driver re-reconciles on next reconnect — verified safe |

## 5. Hostile audit checklist (for the post-build doc)

- P0: data loss / cross-tenant leak
  - [ ] FK violation on UID not in mbs_sessions → 23503 wrapped as ErrForeignKey, not silent
  - [ ] No DELETE inside UpsertAssets — additive only
  - [ ] No tenant_id read or compare — assets live or die with the session row
- P1: invariant violations
  - [ ] At-most-one-primary enforced at DB level (verified C2-G3)
  - [ ] discovered_at preserved on update (verified G1 unit)
  - [ ] DeleteSession cascade reaches all FK children (verified C2-G4 + phone_threads probe)
- P2: race conditions
  - [ ] Concurrent UpsertAssets for same uid documented (caller serializes)
  - [ ] Concurrent SetPrimaryAsset documented (DB backstop catches)
- P3: ergonomic
  - [ ] Empty slice = no-op (no useless txn open/close)
  - [ ] Errors wrap pgxconn errors with method context (UpsertAssets / SetPrimaryAsset / DeleteSession in the chain)
