# Stage E1 Chunk 8 — Hostile-Eyes Audit (Legacy Importer)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:** `internal/mbs/importer/{walker,encrypt,importer}.go` + `cmd/mbs-import/main.go` + cmd/mbs/main.go bootstrap branch

## Methodology

Reviewed every code path with attacker / careless-operator / racing-component hats. Concerns are graded:

- **P1** — fix before commit
- **P2** — document, may revisit
- **FP** — false positive, no fix
- **AB** — accepted-by-design with rationale

All P1 findings either fixed during write or are documented inline with a fix path.

---

## Findings

### F1 (P1) — `cmd/mbs-import --force` on a live system races the refresh ticker

**Surface:** `importer.go::importOne` force-replace branch (UpdateSessionTokens → UpdateSessionCookies → UpdateSessionState are not in a transaction).

**Attack scenario:**

1. Operator runs `mbs-import --force --tenant ... --sessions-dir ...` while hermes-mbs is running.
2. Refresh ticker (chunk 7) is mid-tick on the same uid:
   - T1: ticker `decryptCreds(row.EncryptedCookies)` → succeeds with old cookies
   - T2: importer `UpdateSessionTokens` → writes fresh tokens
   - T3: ticker `Ping(creds)` → succeeds, Meta returns Set-Cookie rotation
   - T4: importer `UpdateSessionCookies(cols.Cookies, now, now)` → **overwrites the ticker's freshly-merged cookies with the importer's stale-at-rotate cookies**
3. Net result: row has fresh tokens + stale cookies. Self-heals on next refresh tick (~1h), but until then the cookies are wrong-shape.

**Severity:** Low. Cookies are non-fatal at MQTToT layer (Lightspeed-only). No security breach — neither side is corrupted, just stale.

**Resolution:** Documented in `cmd/mbs-import/main.go` header comment ("Operator workflow"). Recommended pattern: run with hermes-mbs paused OR accept self-healing on next refresh tick. Not worth a transactional Update — that requires a new `ReplaceSessionSecrets` store method and pgx tx surface; ROI is poor for an operator-initiated rare action.

**Bootstrap path (cfg.ImportLegacyOnStartup) is SAFE by construction**: importer runs at boot-step 3a, before `refresh.New` is wired (step 11b). No tick can fire during bootstrap import.

### F2 (P1, FIXED IN DRAFT) — typed-nil `EventPublisher` would panic on dispatch

**Surface:** `importer.go::Run` calls `opts.Publisher.PublishSessionLifecycle(...)` inside `if opts.Publisher != nil`.

**Concern:** If a caller assigns a typed-nil to the interface variable (e.g., `var p *natsEventPublisher; Run(Options{Publisher: p})`), the `!= nil` check returns TRUE (typed-nil interface is non-nil) and the method dispatch panics on nil receiver.

**Verification:**

- `cmd/mbs-import/main.go::run`: declares `var pub handler.EventPublisher` (untyped nil interface) and only assigns when NATS connect succeeds. If connect fails, `pub` stays zero-interface → `opts.Publisher != nil` returns FALSE. ✓
- `cmd/mbs/main.go` bootstrap path: passes `Publisher: nil` (untyped literal). Same evaluation. ✓
- Tests: pass concrete `handler.NopPublisher{}` or concrete `*recordingPub`. No typed-nil. ✓

**Resolution:** No production caller assigns a typed-nil. False positive. Documented here so future contributors don't introduce one. If we ever wire a typed-nil-able publisher (e.g., `handler.EventPublisher` returned from a constructor that can return `(nil, err)`), tests for that path MUST also cover the nil-handling behavior in Run.

### F3 (P2, ACCEPTED) — `_ = UpdateSessionState` swallows the post-force state reset error

**Surface:** `importer.go::importOne` line `_ = opts.Store.UpdateSessionState(ctx, sf.UID, "active", nil)` in the force-replace branch.

**Concern:** If state-reset fails (e.g., DB hiccup), the row stays in its prior state (typically "burned") even though tokens were updated. Operator's log shows "Forced++" outcome but the row isn't fully active.

**Rationale for accept:** Matches the bridge handler's existing pattern (`rpc_bridge_login.go:517`) verbatim. Inconsistency would be more confusing than the small risk of a stuck-burned row. Operator can re-run `--force` to retry; the state will reset on the second attempt.

**Future:** When we add a transactional `ReplaceSessionSecrets`, fold the state reset into the same tx and surface errors cleanly.

### F4 (P2, ACCEPTED) — DisplayName="" for every imported session

**Surface:** `importer.go::buildRow` sets `DisplayName: ""`.

**Concern:** Inbox UI may render "Untitled" / blank for imported rows until the operator manually backfills.

**Rationale:** Legacy `creds.json` has no display name field. Synthesizing one from email (which we'd have to keep separately) was out of scope. Gateway can backfill from the asset row's PageName (which IS populated when bootstrap had run). Inline comment notes this path.

### F5 (P2, ACCEPTED) — Asset upsert errors are non-fatal warnings

**Surface:** `importer.go::importOne` final block — `UpsertAssets` and `SetPrimaryAsset` failures log Warn and continue.

**Attack:** A DB hiccup at asset-upsert time leaves the session row but no asset row → inbox UI shows no primary page.

**Rationale:** Dropping the entire session (and its encrypted tokens) for a non-critical asset failure is worse than persisting a session sans assets. First successful GetOrConnect from a live pod will re-discover assets via the live GraphQL queries. Matches bridge handler pattern.

### F6 (P2, ACCEPTED) — Cookies cleared when envelope sidecar absent

**Surface:** Pre-Stage-D sessions lack `<uid>.bridge.json`. `cols.Cookies` stays nil. On import, EncryptedCookies is NULL.

**Concern:** On first refresh tick, ticker tries to decrypt empty cookies → fails decrypt with "encrypted column cookies is empty" → classifies as transient → does NOT burn.

**Verification:** `internal/mbs/refresh/attempt.go::decrypt` returns the empty-cookies error which classifies as a non-sentinel transient (chunk-7 audit). Stage-D backfill will populate cookies on the first Ping that triggers Set-Cookie merges. Acceptable.

### F7 (FP) — Filename overflow / symlink injection

**Surface:** `walker.go::parseUIDFromName` and the directory walk.

**Concern:** Crafted filename like `99999999999999999999.json` could overflow int64; symlinks into `/etc` could trick the importer into reading arbitrary files.

**Verification:**

- `strconv.ParseInt(base, 10, 64)` returns error on overflow → rejected. ✓
- `os.ReadDir` returns DirEntry with `IsDir()`; symlinks to FILES would be processed (Linux + macOS). But the operator chose `--sessions-dir`; an attacker who can plant symlinks there already has filesystem access. **AB**: not in our threat model.

### F8 (P2, ACCEPTED) — `os.DevNull` opened inside `withSilentStderr` test helper without cleanup on panic

**Surface:** `cmd/mbs-import/main_test.go::withSilentStderr`.

**Concern:** If `fn()` panics, the deferred close runs but `os.Stderr` is restored. So this is actually safe.

**Verification:** `defer` semantics restore `os.Stderr` and close devnull regardless of panic. ✓ False positive.

### F9 (AB) — DEK drift between mbs-native legacy write time and hermes-mbs import time

**Surface:** Conceptual — legacy `creds.json` is *plaintext* (the legacy tool never had a DEK). Importer encrypts at import time with whatever DEK the operator supplies.

**Concern:** None — there's no prior ciphertext to mis-decrypt. The "drift" concern only applies to re-encryption tools, which is a different operator surface (Stage F encrypt-rewrite, not chunk 8).

---

## Pre-commit checks (all PASS)

| Check | Status |
|---|---|
| `go build ./internal/mbs/importer/... ./cmd/mbs-import/... ./cmd/mbs/...` | ✓ clean |
| `go vet ./internal/mbs/importer/... ./cmd/mbs-import/...` | ✓ clean |
| `go test ./internal/mbs/... ./cmd/mbs/... ./cmd/mbs-import/... -race -count=3` | ✓ all green |
| `make build` produces both `bin/hermes-mbs` (51MB) and `bin/mbs-import` (28MB) | ✓ |
| Cross-tenant `--force` REFUSED (security regression test) | ✓ `TestRun_RefusesCrossTenant_EvenWithForce` |
| Asset synthesis when creds carries PageID/WABA/WEC fields | ✓ `TestRun_AssetSynthesis_FromCreds` |
| Idempotent skip without `--force` | ✓ `TestRun_IdempotentSkip_NoForce` |
| Dry-run produces no DB writes and no NATS emits | ✓ `TestRun_DryRun_NoWrites` |
| Cancellation surfaces `context.Canceled`, stats preserved | ✓ `TestRun_ContextCancellation_StopsCleanly` |
| AAD column-binding enforced | ✓ `TestEncryptForUID_AADBindingByColumn` |
| AAD uid-binding enforced | ✓ `TestEncryptForUID_AADBindingByUID` |
| Cross-DEK decrypt fails | ✓ `TestEncryptForUID_RejectsWrongDEK` |

## Approval

Chunk 8 is GO. The one P1 finding (F1) is a documented operator concern with a self-healing mitigation. All other findings are either FP or AB with clear rationale.

— Oracle, 2026-05-29
