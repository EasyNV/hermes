# Stage F follow-up — Chunks 3 through 6

**Date:** 2026-05-30
**Author:** Oracle
**Predecessors:** Chunk 1 (`667482b`, tenant metadata key), Chunk 2 (`5bfe2e4`, UpsertAssets impl)
**Branch:** `main` (continuing direct on main per project convention)

---

## Why this plan exists

Per Sam's request:

1. Implement `UpdateSessionTokens` so `bin/mbs-import --force` works.
2. Expand the proto so the UI shows `business_id` + `wec_account_registered`.
3. Eyeball the UI in the browser and capture the asset drawer working.
4. Save a skill for the `write_file`-content-rewriting scrubber trap.

Recon turned up **three deviations** from the original scope. They are all small but they change the chunk boundaries — flagging them up-front for approval.

---

## ⚠️ Scope deviations found in recon

### Deviation A — `UpdateSessionCookies` is *also* `ErrNotImplemented`

`internal/mbs/store/pg.go:318-320` — `UpdateSessionCookies` returns `ErrNotImplemented`, same as tokens.

The importer's `--force` path (`internal/mbs/importer/importer.go:280-291`) calls them **back-to-back**:

```go
opts.Store.UpdateSessionTokens(ctx, sf.UID, cols.AccessToken, cols.Secret, cols.SessionKey)
if len(cols.Cookies) > 0 {
    opts.Store.UpdateSessionCookies(ctx, sf.UID, cols.Cookies, now, now)
}
```

If I only fix tokens, `--force` still explodes on cookies (and `cols.Cookies` is non-empty for every real legacy import). The refresh ticker (`internal/mbs/refresh/attempt.go:171-172` and `:224-225`) **also** calls `UpdateSessionCookies` — so any session that lives past its cookie TTL is currently a dead session.

**Decision:** Chunk 3 fixes **both** `UpdateSessionTokens` AND `UpdateSessionCookies`. Same blast radius, same shape, ten extra lines. Refusing to fix cookies just to honour a literal interpretation of "implement UpdateSessionTokens" leaves the system half-broken.

### Deviation B — The TS `MbsSessionAsset` type is fictional

`web/src/api/types.ts:333-339`:

```ts
export interface MbsSessionAsset {
  uid: string
  kind: string             // "page" | "wec_mailbox"
  externalId: string
  displayName: string
  metadata: string         // JSON blob
}
```

None of those fields exist on the wire. The actual JSON response (verified via curl earlier this session) is:

```json
{"assets":[{
  "pageId": "1219576644562769",
  "wabaId": "1147297338458228",
  "wecMailboxId": "1153441357849273",
  "wecPhoneNumber": "573508814866",
  "hasWaba": true
}]}
```

The shape matches proto `MbsAsset`. The hand-rolled TS type was speculative and never matched reality. The UI `MbsSessions.tsx:347-358` then maps over `a.kind`/`a.externalId`/`a.displayName` — **all undefined** — which is why the assets drawer renders empty `<li>` items even when the API returns rows.

**Decision:** Chunk 4 (proto expansion) fixes this as part of the same edit — the proto expansion regenerates `gen/ts/` and rewrites the hand-rolled `types.ts` `MbsSessionAsset` to match. The UI renderer in `MbsSessions.tsx` gets rewritten to use the real proto field names. Without this, "eyeball the UI" (chunk 5) shows an empty drawer no matter what we put in proto, and would surface the bug as a separate fire-drill.

### Deviation C — `gen/ts/` may not be wired into the build

AGENTS.md says proto codegen produces `gen/ts/` for the frontend, but `web/src/api/types.ts` is hand-rolled. Either gen/ts is not actually imported by the web tsconfig, or it's imported but `types.ts` shadows the generated types. Will verify during chunk 4. If gen/ts is used, we regen it; if not, we just fix the hand-rolled file.

---

## Chunk 3 — Implement `UpdateSessionTokens` + `UpdateSessionCookies`

### Surface

- `internal/mbs/store/pg.go:318-324` — both methods stubbed
- Importer `--force` path (`internal/mbs/importer/importer.go:280-294`) unblocked
- Refresh ticker (`internal/mbs/refresh/attempt.go:171,224`) unblocked
- Test path: `internal/mbs/store/pg_assets_test.go` already runs against live PG when `MBS_PGTEST_DSN` is set; add `pg_tokens_test.go` + `pg_cookies_test.go` (or one file `pg_credentials_test.go`)

### Behaviour spec

**`UpdateSessionTokens(ctx, uid int64, encAccessToken, encSecret, encSessionKey []byte) error`**

- Single UPDATE setting `encrypted_access_token`, `encrypted_secret`, `encrypted_session_key`, `updated_at = NOW()` where `uid = $4`.
- Returns `ErrNotFound` on zero rows affected.
- Wraps pg errors with `fmt.Errorf("update session tokens: %w", err)`.
- **Does NOT touch cookies, state, pod_id, or any other column.** Caller composes with `UpdateSessionCookies` + `UpdateSessionState` as needed.
- Nil/empty byte slice for any of the three encrypted blobs is **allowed** (sealed nil is a real ciphertext for legacy rows where a field was missing). No length validation here — that belongs in the encryption layer.

**`UpdateSessionCookies(ctx, uid int64, encryptedCookies []byte, lastRefreshedAt, lastValidatedAt time.Time) error`**

- Single UPDATE setting `encrypted_cookies = $1`, `last_refreshed_at = $2`, `last_validated_at = $3`, `updated_at = NOW()` where `uid = $4`.
- Returns `ErrNotFound` on zero rows affected.
- Zero-value timestamps allowed — caller's responsibility to pass meaningful values. If both are zero we still write (caller intent).
- Nil/empty `encryptedCookies` allowed for the same reason as tokens.

### Contracts

- **C3-G1** — Idempotency: calling either method twice with identical inputs is a no-op semantically (the second UPDATE just rewrites the same bytes, `updated_at` advances).
- **C3-G2** — No transaction needed. Each is a single statement; concurrent writers race on `updated_at` but the pgsql write ordering picks a winner and both writers see ErrNotFound or success deterministically.
- **C3-G3** — Importer `--force` end-to-end: after fix, `bin/mbs-import --force --session <legacy.json>` on an existing uid replaces tokens + cookies and resets state to `active` (existing UpdateSessionState call at importer.go:293).
- **C3-G4** — Refresh ticker integration: post-fix, the ticker can persist merged cookies and bumped LastValidatedAt. Will not exercise this in chunk 3 tests (it's a refresh-package concern) but the unblock is the headline win.

### Verification gates

1. `go vet ./...` clean
2. `go test ./internal/mbs/store -run TestUpdateSession -count=1` green with `MBS_PGTEST_DSN`
3. `go test ./internal/mbs/store -run TestUpdateSession -count=1` SKIPS clean without the env var (CI safety)
4. Live verification: `bin/mbs-import --force --session sessions/1674772559.json` against live stack → row tokens + cookies replaced, state reset to active
5. Pre-existing tests stay green: `go test ./internal/mbs/...`

### Hostile audit checklist (writes to `docs/research/mbs-credential-update-hostile-audit-2026-05-30.md`)

- Concurrent writer race on the same uid — what's the worst-case interleave? (Answer: last-writer-wins per column; AAD bound to uid so ciphertext can't be smuggled across uids.)
- ErrNotFound path — does importer treat it correctly? (Yes, surfaces as `outcomeFailed` with error.)
- pgconn 25P02 (in_failed_sql_transaction) during refresh ticker — what happens on the ticker's next attempt? (Pool reconnects, next attempt fresh.)
- DEK rotation in flight — does an UpdateSessionTokens during rotation half-update a row? (No, single statement; transactional rotation must happen at the DEK envelope layer not here.)

---

## Chunk 4 — Proto expansion for asset fields

### Surface

- `proto/hermes/v1/mbs.proto:230-239` — `MbsAsset` message expanded
- `internal/mbs/handler/proto_conv.go:102-116` — `assetRowToProto` populates new fields
- `gen/go/hermes/v1/mbs.pb.go` — regenerated (gitignored, dev/CI runs `make proto-gen`)
- `gen/ts/hermes/v1/mbs_pb.ts` — regenerated if used by frontend, otherwise N/A
- `web/src/api/types.ts:333-339` — `MbsSessionAsset` rewritten to match wire shape
- `web/src/pages/MbsSessions.tsx:347-358` — assets drawer renderer uses real fields

### Proto changes

```proto
message MbsAsset {
  string page_id = 1;
  string page_name = 2;
  string waba_id = 3;
  string wec_mailbox_id = 4;
  string wec_phone_number = 5;
  string business_presence_node_id = 6;
  string ig_account_id = 7;
  bool has_waba = 8;
  // ── Added 2026-05-30 (Stage F follow-up chunk 4) ──
  string business_id = 9;             // Stage B.1 origin
  string business_name = 10;          // Stage B.1 origin
  bool is_primary = 11;               // partial-unique-index gated
  bool wec_account_registered = 12;   // Stage B.2 send-to-phone gate
}
```

Field numbers 9-12 are previously unused. Adding fields is wire-compatible — clients on old generated code ignore unknown fields.

### TS types rewrite

```ts
export interface MbsAsset {
  pageId: string
  pageName: string
  wabaId: string
  wecMailboxId: string
  wecPhoneNumber: string
  businessPresenceNodeId: string
  igAccountId: string
  hasWaba: boolean
  businessId: string
  businessName: string
  isPrimary: boolean
  wecAccountRegistered: boolean
}

export interface MbsListSessionAssetsResponse { assets: MbsAsset[] }
```

`MbsSessionAsset` was speculative and never matched the wire — replaced wholesale with `MbsAsset` matching proto. Any callers of the old name (grep shows only `types.ts` itself + `mbs.ts` + the drawer) get migrated.

### UI renderer rewrite

Replace the broken `a.kind` / `a.externalId` / `a.displayName` render with:

```tsx
{assetsQuery.data.assets.map((a) => (
  <li key={a.pageId || a.wecMailboxId}
      className={`rounded border bg-card px-2 py-1.5 text-xs ${a.isPrimary ? 'ring-1 ring-primary' : ''}`}>
    <div className="font-medium">
      {a.pageName || a.pageId || '—'}
      {a.isPrimary && <span className="ml-1 text-[10px] text-primary">PRIMARY</span>}
    </div>
    <div className="text-[10px] text-muted-foreground space-y-0.5">
      {a.businessName && <div>biz: {a.businessName}</div>}
      {a.wabaId && <div>WABA: {a.wabaId}</div>}
      {a.wecPhoneNumber && (
        <div>
          WEC: {a.wecPhoneNumber}
          {a.wecAccountRegistered ? ' ✓' : ' ✗ unregistered'}
        </div>
      )}
    </div>
  </li>
))}
```

### Contracts

- **C4-G1** — Backward compat on the wire: existing clients reading `MbsAsset` continue to work; new fields are zero-valued for old data (acceptable — those rows just don't render the new badges).
- **C4-G2** — `buf generate` requires the PATH hack from MEMORY: `PATH="$HOME/go/bin:$HOME/.hermes/profiles/oracle/home/go/bin:$PATH" make proto-gen` — documented in the commit message.
- **C4-G3** — Verify whether `gen/ts/` is referenced by `web/tsconfig.json` or `web/src/api/*.ts`. If yes, regen; if no, skip and just patch the hand-rolled types.
- **C4-G4** — Frontend type-check + build green: `cd web && pnpm typecheck && pnpm build`.

### Verification gates

1. `make proto-gen` (with PATH) regenerates without diff drift beyond the new fields
2. `go build ./...` clean
3. `go vet ./internal/mbs/handler` clean
4. `pnpm --filter web typecheck` clean
5. `pnpm --filter web build` clean
6. Live curl: `GET /api/v1/mbs-sessions/61590134170831/assets` returns the new fields populated for the existing row
7. Live UI eyeball (chunk 5)

### Hostile audit checklist

- Field-number conflicts with future RFCs (none — checked the proto file end)
- Nullable handling on TS side — proto strings default to "" so renderer must check for empty (handled by `||` guards)
- Old generated TS in `gen/ts` if kept around — could shadow the new types? (Verify in chunk 4 itself; remove if so.)

---

## Chunk 5 — UI eyeball + screenshot capture

### Surface

- Local stack at `https://localhost:8443`
- Login as default tenant admin (creds from `deploy/.env.local` or compose env)
- Navigate to `/mbs-sessions`
- Expand 61590134170831 row, capture assets drawer
- Capture before/after for comparison if useful

### Verification gates

1. Login succeeds (post-chunk-1 metadata fix)
2. `/mbs-sessions` lists both sessions (1674772559 + 61590134170831)
3. Expanding 61590134170831 shows the page/business/WABA/WEC asset card with `business_name`, `wabaId`, `wecPhoneNumber ✓` (registered) and a PRIMARY badge
4. Expanding 1674772559 shows "No assets attached to this session." (correct — that session never had asset discovery)
5. Zero console errors (no React #185 regression, no missing-field crashes)
6. Screenshot saved as `docs/research/assets/mbs-asset-drawer-2026-05-30.png` for the audit doc

### What we do NOT do here

- We do not change any production code in chunk 5. It's pure verification. If we find a UI bug we open a separate chunk-7 patch.

---

## Chunk 6 — Skill: `write_file`-scrubber-credential-rewrite trap

### Surface

- New skill at `~/.hermes/profiles/oracle/skills/security/scrubber-write-file-credential-trap/SKILL.md`
- Category: `security` (because it's about a tooling-layer security/secrecy behaviour)
- ~80-line skill with trigger conditions, repro pattern, workaround, verification commands

### Content outline

1. **Trigger** — Any time you `write_file` or `patch` a shell script / .env / docker-compose snippet that contains a credential-shaped string (`user:password@host`, `admin123`, JWT-shaped tokens, `${PASSWORD}` interpolations adjacent to literal passwords).
2. **What happens** — The Hermes terminal display-scrubber ALSO intercepts `write_file` payloads and rewrites matched patterns to `***` *on disk*, not just in the displayed output. The file you intended to write is silently corrupted.
3. **Failure mode** — Script runs, gets the masked literal `***`, fails with a confusing auth error ("password authentication failed for user ***" or "connection refused").
4. **Workaround patterns**
   - Build credential strings via shell concatenation in a single terminal command: `DSN="postgres://user:$(cat /secure/path/pw)@host/db" ./bin/mbs-import`
   - For Python: read password from an env var or external file inside the script, never as a literal in the source.
   - For Go: same — pass via env, not source.
   - Verify on-disk bytes after writing: `cat -A path | head -c 200` or `python3 -c "print(repr(open(p,'rb').read()[:200]))"` to see the actual bytes.
5. **Common false-positive shapes that trigger it** — bare strings that look like passwords (admin123, password, secret123), `:WORD@` patterns even when WORD is not actually a secret.
6. **What MEMORY already covers** — the display-only behaviour (correct as of today). What's NEW: the write-time behaviour, escalated this session.

### Contracts

- **C6-G1** — Skill loads via `skill_view name=scrubber-write-file-credential-trap`
- **C6-G2** — Skill follows the standard SKILL.md frontmatter format from claude-code-skill-authoring
- **C6-G3** — Add memory entry pointing future sessions at this skill so the trap is caught at write-time, not after the script breaks

### Verification gates

1. `skill_view` returns content
2. Skill listed under `skills_list category=security`
3. MEMORY entry added: "Scrubber rewrites credential-shaped patterns in write_file/patch payloads to *** ON DISK, not just display. See skill scrubber-write-file-credential-trap."

---

## Out of scope (will not touch in this plan)

- Pre-existing importer race documented in `docs/research/mbs-importer-hostile-audit-2026-05-29.md` (F1 — refresh ticker collides with importer `--force`). Worth a separate chunk; not blocking.
- `gen/ts/` build wiring audit beyond chunk 4's quick check.
- ACME on real VPS (Stage F DoD #5). Sam will surface that when ready.
- `mbs_phone_threads` cascade behaviour (audit comment in `DeleteSession` doc). Separate concern.

## Execution order

1. Chunk 3 — store impls + tests + hostile audit + commit
2. Chunk 4 — proto + handler + TS + UI + commit (per-area subcommits OK if buf regen is noisy)
3. Chunk 5 — verify in browser, capture screenshot, write `docs/research/mbs-ui-asset-drawer-verification-2026-05-30.md`
4. Chunk 6 — skill + memory entry

After approval: build straight through 3 → 6, surfacing any blockers immediately rather than at the end.

## Approval needed before I start coding

- **Deviation A** — extend chunk 3 to also implement `UpdateSessionCookies` (recommended; refusing leaves the system half-broken)
- **Deviation B** — extend chunk 4 to rewrite the speculative TS `MbsSessionAsset` + the broken UI renderer (recommended; otherwise chunk 5 has nothing to verify)
- **Deviation C** — `gen/ts/` wiring audit happens inside chunk 4; if it's used we regen, if not we don't

If you say "go" without qualification I take it as approval of all three deviations and the execution order above. If you only approve part of it, name what you want carved out.
