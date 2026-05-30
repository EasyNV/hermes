# Hostile audit — Stage F follow-up chunk 3

**Date:** 2026-05-30
**Scope:** `internal/mbs/store/pg.go::UpdateSessionTokens` + `::UpdateSessionCookies`
**Plan:** `.hermes/plans/2026-05-30_stage-f-followup-chunks-3-to-6.md`
**Predecessor commits:** `667482b` (chunk 1), `5bfe2e4` (chunk 2)

## What landed

Replaced two `ErrNotImplemented` stubs at `pg.go:318-324` with concrete
single-statement UPDATEs.

```go
UpdateSessionCookies(ctx, uid, encryptedCookies, lastRefreshedAt, lastValidatedAt)
  → UPDATE mbs_sessions SET cookies=$1, last_refreshed_at=$2,
                            last_validated_at=$3, updated_at=NOW()
    WHERE uid=$4

UpdateSessionTokens(ctx, uid, encAccessToken, encSecret, encSessionKey)
  → UPDATE mbs_sessions SET access_token=$1, secret=$2,
                            session_key=$3, updated_at=NOW()
    WHERE uid=$4
```

Both return `ErrNotFound` on zero rows affected. Both wrap pg errors
with `fmt.Errorf("update session …: %w", err)`.

8 integration tests added at `pg_credentials_test.go`. Gated on
`MBS_PGTEST_DSN`. All 8 PASS against the live stack.

## Threat surface

### T1 — Concurrent writer race on the same uid

**Setup:** Two callers race on the same uid. Importer `--force` thread
and refresh ticker tick on the same session at the same instant.

**Worst case:** Both want to write the access-token triple AND cookies.

**What happens:**
- `UpdateSessionTokens` and `UpdateSessionCookies` touch DISJOINT
  column sets. They cannot corrupt each other's columns.
- Within a method, the single UPDATE is atomic — pg guarantees the
  whole row WHERE-clause + SET reads consistently. No partial-column
  write is possible.
- Two `UpdateSessionTokens` calls in flight: last-writer-wins on the
  three triple columns. Both ciphertexts are valid under the same DEK
  with AAD bound to `uid`, so the surviving row decrypts cleanly.
- One `UpdateSessionTokens` + one `UpdateSessionCookies` interleaved:
  no overlap, both succeed, row stays internally consistent.

**Mitigation:** None needed. The disjoint-column property comes for
free from the explicit column list.

**Open risk:** The importer `--force` path is NOT in a transaction
(see F1 in `docs/research/mbs-importer-hostile-audit-2026-05-29.md`).
A refresh tick between importer's tokens-write and importer's
cookies-write CAN result in:
  - importer wrote new tokens
  - refresh ticker rotated cookies (using OLD tokens — still valid
    because the AAD is uid, not token-bound)
  - importer wrote OLD-archive cookies, overwriting the refresh
    rotation
The system stays internally consistent (every ciphertext decrypts), but
the cookies revert to the imported-archive snapshot. This is the F1
race I knowingly left for a separate chunk. Documented; not
introduced by chunk 3.

### T2 — AAD smuggling across uids

**Question:** Can an attacker who controls the encrypted bytes for uid A
trick the store into persisting them under uid B?

**Analysis:**
- AAD is bound at the encryption layer (`crypto.EncryptAESGCM` with
  `store.BuildAAD(store.AADAccessToken, uid)`) — see
  `internal/mbs/importer/encrypt.go:90-91`.
- The store layer is a thin SQL wrapper. It does NOT inspect or
  re-bind AAD.
- If a caller passes ciphertext encrypted for uid A and a uid argument
  of B, the store writes B's row with A's bytes. Subsequent decrypt
  against uid B's AAD fails — the row becomes undecryptable garbage.
- Importer guards against this via `creds.UserID == uid` check at
  `encrypt.go:72-78`. Refresh ticker passes the uid from the row it
  just read.

**Mitigation:** None at the store layer (correct). The store cannot
verify AAD without DEK. Caller integrity check is the right boundary.

### T3 — DEK rotation in flight

**Question:** During a DEK rollover, can an `UpdateSessionTokens` land a
half-updated row?

**Analysis:**
- DEK rotation is at the envelope layer (DEK envelope re-wrap), not
  the store layer. The store sees opaque bytes.
- A single `UpdateSessionTokens` writes three columns atomically (one
  UPDATE). If the caller pre-encrypted with new-DEK, all three
  columns are new-DEK. If with old-DEK, all three are old-DEK. No
  intra-row mix possible.
- Inter-row mix: if rotation is iterating uids and chunk-3 writes
  land in the middle, some rows are new-DEK and others old-DEK. The
  ticker/handler must hold envelopes for both DEKs until rotation
  completes — that's a DEK rotation design constraint, not a store
  concern.

**Mitigation:** None at the store layer. Documented in the method
docstrings ("AAD-bound encrypt path which produced these bytes").

### T4 — pg connection failure mid-statement

**Question:** Network glitch between client and pg during UPDATE.

**Analysis:**
- pgx returns an error. We wrap with `fmt.Errorf("update session
  tokens: %w", err)`.
- Caller (importer) treats as `outcomeFailed`. Refresh ticker treats
  as transient and retries next tick.
- No partial write: pg transactionality on a single statement means
  the UPDATE either lands or does not.

**Mitigation:** None needed. Standard pg semantics.

### T5 — Zero-byte / nil ciphertext

**Question:** Can a caller corrupt the row by passing `nil` or `[]byte{}`?

**Analysis:**
- pgx encodes `nil` and `[]byte{}` differently:
  `nil` → SQL NULL, `[]byte{}` → empty bytea (`'\x'`).
- The `access_token` column is `BYTEA NOT NULL` (per
  `migrations/mbs/000001_init.up.sql`). NULL fails the constraint
  with `not_null_violation` (23502); wrapped into our error.
- Empty bytea succeeds but is non-decryptable. The encryption layer
  guards against this at `encrypt.go:79-87` ("missing access_token")
  before bytes reach us.

**Mitigation:** Caller is responsible. Tested with empty bytes — the
write succeeds and produces a row that the decrypt path rejects with
a clear error.

### T6 — Importer leaks PG password into argv / env logs

**Question:** Does the importer's `--force` path leak the DATABASE_URL
into structured logs?

**Analysis:**
- `cmd/mbs-import/main.go` log lines: "postgres connect failed" with
  the wrapped pg error which sometimes contains the host:port (no
  password in pgx error strings since v5).
- During this session: the terminal display scrubber correctly
  matched `user:password@host` and masked to `user:***@host`. This
  is a DISPLAY-ONLY behaviour for the terminal — the bytes in logs
  to disk are NOT scrubbed (separate concern from chunk 6's skill).

**Mitigation:** Not introduced by chunk 3. Pre-existing importer
practice. Worth a follow-up to scrub at the logger boundary.

### T7 — `updated_at` clock skew on multi-pod writes

**Question:** Multi-pod deployment (future K8s) where two pods write
the same uid: `NOW()` is per-pod-clock; if clocks skew, monotonic
ordering breaks.

**Analysis:**
- pg's `NOW()` uses the SERVER clock, not the client pod's clock.
  All writes funnel through the same pg primary, so `updated_at` is
  monotonic per-row.
- Replication lag to read replicas might surface temporarily
  out-of-order timestamps to read-only consumers. Acceptable.

**Mitigation:** None needed. pg server clock is the source of truth.

## Behaviour-equivalence with the mock store

The mock (`internal/mbs/store/mock/mock.go`) implements both methods
in chunk 1's earlier audit fix. Spot-checked:

- Mock `UpdateSessionTokens` clones the byte slices into a defensive
  copy. PgStore writes through pgx which makes its own internal
  copy during the wire encode.
- Mock returns `ErrNotFound` on missing uid. PgStore returns the same
  via `RowsAffected() == 0`.
- Mock `UpdateSessionCookies` overwrites the cookies blob and stamps
  both timestamps. PgStore SQL matches.

Behavioural drift: zero noted.

## Live verification

```
$ docker exec hermes-postgres-1 psql -U hermes -d hermes -c \
    "SELECT … FROM mbs_sessions WHERE uid = 61590134170831"

BEFORE:  at_head=b03bc8ad151e0c24  cookies_len=1055
         last_refreshed_at=2026-05-30 09:49:52
         last_validated_at=2026-05-30 09:49:52
         updated_at=2026-05-30 09:49:52

AFTER --force (chunk 3 path):
         at_head=43b15444ee3d97df  cookies_len=1055
         last_refreshed_at=2026-05-30 12:34:41   ← UpdateSessionCookies fired
         last_validated_at=2026-05-30 12:34:41   ← UpdateSessionCookies fired
         updated_at=2026-05-30 12:34:41          ← UpdateSessionTokens fired

ASSETS UNTOUCHED:
         page_id=1219576644562769  business_id=1655020605549323
         waba_id=1147297338458228  wec_phone=573508814866
         is_primary=t  wec_account_registered=t
```

The asset row (chunk 2's responsibility) was correctly preserved
through the `--force` write — there is no UpsertAssets call on the
force-replace path because legacy archives don't carry asset data
(per the comment at `importer.go:296-299`).

## Test gate results

```
$ MBS_PGTEST_DSN="…" go test ./internal/mbs/store -run TestPgStore_Update -count=1 -v
PASS: TestPgStore_UpdateSessionTokens_HappyPath              (0.06s)
PASS: TestPgStore_UpdateSessionTokens_NotFound               (0.00s)
PASS: TestPgStore_UpdateSessionTokens_EmptyBytesAllowed      (0.01s)
PASS: TestPgStore_UpdateSessionTokens_UpdatedAtAdvances      (0.02s)
PASS: TestPgStore_UpdateSessionCookies_HappyPath             (0.01s)
PASS: TestPgStore_UpdateSessionCookies_NotFound              (0.00s)
PASS: TestPgStore_UpdateSessionCookies_RefreshThenValidate   (0.01s)
PASS: TestPgStore_UpdateSessionCookies_ZeroTimestampsAllowed (0.01s)
PASS  github.com/hermes-waba/hermes/internal/mbs/store  0.142s

$ go test ./internal/mbs/... -count=1   (no DSN)
ok  internal/mbs/bridge       ok  internal/mbs/config
ok  internal/mbs/handler      ok  internal/mbs/importer
ok  internal/mbs/refresh      ok  internal/mbs/session
ok  internal/mbs/store        ok  internal/mbs/store/mock
```

## Open items not addressed in chunk 3

1. F1 importer/refresh race (`docs/research/mbs-importer-hostile-audit-2026-05-29.md`).
   Real bug; out of scope here; needs a transactional importer wrap or
   a refresh-pause flag.
2. PG password leakage into structured logs (T6 above). Pre-existing.
3. `UpdateSessionTokens` does NOT bump `updated_at` AND `last_validated_at`
   in the same statement. Rationale: validation timestamps belong to
   the cookie freshness path, not the token rotation path. Callers
   that want both call both methods (importer does, refresh ticker
   handles its own).

## Verdict

Chunk 3 is safe to ship. Single-statement writes with explicit column
lists, AAD-bound semantics enforced at the encrypt layer (not here),
no new race introduced. The F1 importer race it exposes was already
present and is documented in a prior audit.
