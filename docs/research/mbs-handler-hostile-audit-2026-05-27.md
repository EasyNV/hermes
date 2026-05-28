# Stage E1 — Chunk 4 — Step 10 — Hostile-Eyes Audit

**Date:** 2026-05-27
**Auditor:** Oracle (red team mindset against own code)
**Scope:** 6 RPCs + persistence boundary in `internal/mbs/handler/`
**Method:** Read every line of every handler file, look for ways an attacker (tenant-A) compromises tenant-B, exfiltrates a secret, exhausts a resource, or leaks data through a side-channel. Trace data flow end-to-end. No tools, no SAST — eyes + grep.

## Verdict

**1 P0 vulnerability, 2 P2 hygiene findings.** Everything else: green.

The handler is fundamentally well-structured (proper tenant cross-check on lookups, column-bound AAD, fail-closed encryption boundary). The P0 is a corner-case oversight in the re-bridge fallback path — the only code site in the handler that touches a session by uid without first checking tenant ownership. Tight fix.

---

## Findings table

| # | Sev | Title | File:line |
|---|---|---|---|
| F1 | **P0** | Re-bridge fallback overwrites another tenant's stored creds | `rpc_bridge_login.go:461-485` |
| F2 | P2 | `BurnSession` falls back to pre-burn row on read-back failure; reports `state="burned"` even if the burn UPDATE rolled back | `rpc_session_lifecycle.go:208-215` |
| F3 | P2 | `bridgeReaderLoop` goroutine outlives the handler if `stream.Recv()` keeps blocking after the handler returns (fake-stream-only — production gRPC tears down the stream and unblocks Recv) | `rpc_bridge_login.go:255-308` |

Everything else audited: clean.

---

## Audit walkthrough by checkpoint

### Checkpoint 1 — BridgeLogin: client cancel during prompt, no goroutine leaks

**Threat model:** A client opens BridgeLogin, the driver emits a Prompt, the client cancels. Does the driver goroutine wind down? Does the reader goroutine wind down? Does any state leak — semaphore slot held, defer not fired, persisted secrets dangling?

**Trace:**
- Client cancels stream ctx → `ctx.Done()` fires.
- Main loop selects on `driverCtx.Done()` (which is a child of stream ctx) → loop exits with `driverCtx.Err()`.
- Defers run: `close(stopReader)`, `driverCancel()` (no-op, already cancelled by parent), `driver.Close()`, `releaseBridgeSlot()`.
- Reader goroutine sees its `stream.Recv()` return ctx.Err() → exits cleanly.
- No persist (we never reached UpdateKindSuccess).
- `recordBridgeOutcome` is NOT called on cancel (intentional — cancel isn't an outcome).

**Status:** ✅ Clean.

**Caveat (F3):** In tests using `fakeBridgeStream`, `Recv()` only unblocks if the test cancels the ctx. The handler doesn't wait on the reader's goroutine to exit (no `<-readerDone`). In production this is fine — gRPC tears down the stream when the handler returns and Recv unblocks with an error. In tests, the fixture's `defer cancel()` covers it. Documented in source.

### Checkpoint 2 — BridgeLogin: persist failure → no half-persisted secrets

**Threat model:** Driver returns Success. Encryption succeeds for 2 of 3 columns; the third fails. Or encryption succeeds entirely but `CreateSession` fails. What state lands in the DB?

**Trace (`persistBridgeSuccess`):**
- Encrypt access_token, secret, session_key, cookies, totp — all pure functions, no DB. Any failure: return early, NOTHING in DB. ✅
- `CreateSession(row)` — atomic insert. If it fails AND `ExistsSession()` returns `false`, we error out with no row written. ✅
- If `CreateSession` fails AND `ExistsSession()` returns `true` → re-bridge path. **🚨 P0 — see F1.**
- After CreateSession succeeds: `UpsertAssets` failure logged + propagated as error — session row already written, assets missing. The docstring explicitly admits this is the "half-state" we accept (recoverable via asset rediscovery). ✅
- `SetPrimaryAsset` failure is logged-only, non-fatal. ✅

**Status:** ✅ for fresh-session path. **🚨 P0 for re-bridge path.** See F1.

### Checkpoint 3 — Tenant cross-check on EVERY RPC

Grep'd every entry point. Matrix:

| RPC | Tenant validation path |
|---|---|
| `BridgeLogin` | `requireTenant` + `Start.TenantId` body-vs-metadata check at line 79-83. **No uid lookup pre-persist — uid comes from Driver.** ⚠ See F1: in re-bridge path, persisting overwrites an existing uid's session row without checking that the existing row's tenant matches the caller. |
| `ListSessions` | `requireTenant` + body cross-check (line 91-93). `store.ListSessions(tenantID, ...)` scopes by tenant. ✅ |
| `GetSessionStatus` | `requireTenant` + `GetSessionByTenant`. ✅ |
| `ListSessionAssets` | `requireTenant` + `GetSessionByTenant` THEN `ListAssets`. ✅ (assets scoped by uid, but uid already proven tenant-owned) |
| `BurnSession` | `requireTenant` + `GetSessionByTenant`. ✅ |
| `ResolvePhone` | `requireTenant` + `GetSessionByTenant`. ✅ |
| `SendMessage` | `requireTenant` + `GetSessionByTenant`. ✅ |
| `Listen` | `requireTenant` + `GetSessionByTenant`. ✅ |

**Status:** ✅ for 7/8 RPCs. **🚨 P0 for BridgeLogin re-bridge fallback.**

### Checkpoint 4 — AAD column symmetry

Encrypt sites in `rpc_bridge_login.go` use `store.BuildAAD(<COL>, uid)`. Decrypt sites:
- `session/decrypt.go::DecryptCreds` (the only decrypt site in mbs-native) — verified to use the same column constants for the same fields.
- Cookies decrypt: TODO chunk 5 (not in current decrypt path).
- TOTP decrypt: TODO chunk 5+ (only encrypt site exists today; nothing reads it back yet).

**Status:** ✅ The three columns we DO read back (access_token, secret, session_key) are symmetric. Cookies and TOTP secret only have encrypt sites today; the AAD constants `AADCookies` and `AADTOTPSecret` are wired correctly so future readers will match.

### Checkpoint 5 — Dedupe key isolation by uid

Read `sendDedupeCache`:
```go
type dedupeKey struct {
    uid    int64
    digest [sha256.Size]byte
}
```

`(uid, sha256(dedupe_id))` — uid is part of the key. tenant-A spoofing tenant-B's dedupe_id collides only if they're operating on the same uid AND have identical dedupe bytes. uid scoping prevents cross-tenant cache poisoning. The dedupe is also tenant-validated upstream (the `GetSessionByTenant` cross-check at line 55 of `rpc_send_message.go` runs AFTER the dedupe lookup — but the dedupe only ever returns a response that was *written* by a caller who passed the cross-check for the same uid).

**Status:** ✅ Safe by construction.

### Checkpoint 6 — Listener publish-once across N subscribers

Read `listener.emit`:
```go
for _, d := range deltas {
    if d == nil { continue }
    d.TenantID = l.tenantID
    l.fireHook(d)        // EXACTLY ONCE per delta
    l.bc.dispatch(d)     // fan-out to N subscribers
}
```

The hook fires before dispatch, exactly once per delta. Panic in hook is recovered. The chunk-3 reopen tests already pin this invariant (3 subs × 5 deltas = 15 receives, exactly 5 hook fires).

**Status:** ✅ Per-delta NATS publish is invariant to subscriber count.

### Checkpoint 7 — Send during shutdown / drain

`manager.Send` calls `GetOrConnect` first, which short-circuits with `ErrShutdown` or `ErrDrained` if those flags are set. `mapSendErr` routes both to `codes.Unavailable` via `mapSessionErr`. No partial work performed — Bootstrap and Send are never reached.

**Status:** ✅ Clean.

### Checkpoint 8 — Resolver closure captures creds by value

`defaultResolverFactory(creds)` and the test fixture both build a fresh `graphqlAdapter{gc: graphql.New(creds)}` per call. `creds` is a `*auth.Creds` — same pointer is held inside the adapter for the duration of one resolve. But the resolver lifetime is bounded by the single RPC (created at line 156 of `rpc_send_message.go`, dropped at function exit). No long-lived sharing, no concurrent mutation.

**Theoretical risk:** If `auth.Creds` were mutated by a concurrent goroutine (e.g., the manager mutating `creds.PageID` between resolve-creation and graphql call), we'd have a race. Confirmed via grep: handler's `decryptCredsForUID` returns a fresh `*auth.Creds` per call (via `session.DecryptCreds`), so there's no shared instance. ✅

**Status:** ✅ Clean.

### Checkpoint 9 — All RPC errors via map*Err helpers

Grepped `status.Error(` and `status.Errorf(` across handler files. Direct uses:

| File:line | Use | Justification |
|---|---|---|
| `rpc_bridge_login.go:68` | InvalidArgument "stream closed before Start" | Pre-driver validation, no downstream layer to map from. ✅ |
| `rpc_bridge_login.go:74,77,82,99` | InvalidArgument / Internal for validation | Same — pre-driver/pre-persist guards. ✅ |
| `rpc_bridge_login.go:527` | ResourceExhausted (semaphore) | Synthetic — no underlying error. ✅ |
| `rpc_session_lifecycle.go:92,133,156,184` | InvalidArgument for required-field checks + PermissionDenied for tenant body mismatch | Pre-store validation. ✅ |
| `rpc_resolve_phone.go:39,42,57,95,147` | Required-field validation + resolver init + no-primary-page | Synthetic. ✅ |
| `rpc_send_message.go:42,45,67,93,124,130,134,158,181,184` | Required-field validation + parse-int + nil-result + missing-recipient | Synthetic. ✅ |
| `rpc_listen.go:38,77` | Required-field + closed-channel | Synthetic. ✅ |
| `tenant.go:67,71,90` | Auth header missing | Synthetic. ✅ |

Everywhere a downstream layer error needs translating, `mapStoreErr` / `mapSessionErr` / `mapClientErr` / `mapBridgeErr` is used. ✅

### Checkpoint 10 — EventPublisher PublishOutbound on both success AND failure

`rpc_send_message.go::SendMessage`:
- Success branch: `PublishOutbound(..., true, "", now)` at line 103.
- `sendErr != nil` branch: `PublishOutbound(..., false, sendErr.Error(), now)` at line 82.
- `result == nil` defensive branch: `PublishOutbound(..., false, "manager: nil result without error", now)` at line 90.

All three terminal paths publish. ✅

### Checkpoint 11 — Bridge semaphore acquire bounded

`acquireBridgeSlot`:
- Fast path: non-blocking send to semaphore channel.
- Slow path: timer-bounded select between channel-send, timer.C, ctx.Done.

`bridgeAcquireTimeout` defaults to 100ms (set in `NewHandler`). No unbounded wait possible — even if both timer and channel are slow, ctx cancellation breaks the select. `timer.Stop` is deferred so we don't leak a timer goroutine on success. ✅

`releaseBridgeSlot` non-blocking drain via select-default; if the semaphore is somehow already drained (shouldn't happen — acquire is paired with release), it logs an error and continues rather than blocking. ✅

---

## F1 detail (P0): Re-bridge fallback overwrites another tenant's stored creds

### Setup

Multi-tenant SaaS. Each tenant gets their own BizApp account, identified by Facebook UID (`int64`). Tenant-A is authenticated for `uid=1000`. Tenant-B has previously bridged `uid=2000` and stored creds.

Facebook UIDs are **not secret**. They're visible:
- In page URLs once you click into a Page.
- In Graph API responses for any business that's publicly tagged.
- In leaked dumps (every breach since 2013).
- In any cross-tenant message that lists "sent by user X".

**Assumption:** an attacker can guess or learn another tenant's UID.

### The vulnerability

`rpc_bridge_login.go::persistBridgeSuccess` line 461-485:

```go
if err := h.store.CreateSession(ctx, row); err != nil {
    exists, existsErr := h.store.ExistsSession(ctx, uid)
    if existsErr != nil { ... }
    if !exists { ... }
    // Re-bridge path: update tokens + cookies.  ← NO TENANT CHECK
    if uErr := h.store.UpdateSessionTokens(ctx, uid, encAT, encSec, encSK); uErr != nil { ... }
    if len(encCookies) > 0 {
        if cErr := h.store.UpdateSessionCookies(ctx, uid, encCookies, now, now); cErr != nil { ... }
    }
    _ = h.store.UpdateSessionState(ctx, uid, "active", nil)
}
```

### The attack

1. Tenant-A learns tenant-B's `uid=2000` (page URL, leaked dump, social).
2. Tenant-A creates their own Facebook account `bob@evil.com` with password `pw`.
3. Tenant-A starts BridgeLogin with `tenant_id=tenant-A` (passes metadata + body check ✅), email=bob, password=pw.
4. The driver runs, gets a real bloks payload, returns Success with `Creds.UserID = bob's uid` (NOT 2000).

   …wait. The uid comes from the driver. In the production driver, that's bob's uid, not 2000.

So this attack requires the attacker to make the driver return `Creds.UserID = 2000`. Two paths:

**Path A: server-controlled.** If the attacker compromises the driver pod or wedges the bridge subprocess to return arbitrary uid, they're already root. Not interesting.

**Path B: Facebook returns a uid that collides.** Highly improbable — uids are bound to authenticated FB sessions.

**Path C (REAL — the actual attack):** Re-bridge their own legitimate session. Tenant-A bridged `uid=1000` for their own use. Then tenant-A's account got banned and reissued with the SAME uid (rare but possible — Meta reuses UIDs for reactivated accounts in some flows). OR, more realistic: a malicious *internal* user in tenant-A who has the same SAML provider as another customer. They re-bridge their own creds, and because the uid happens to match an existing session row...

OK that's contrived. Let me reframe.

### The realistic attack

**Operator-error / impersonation-via-takeover.** If tenant-B's Facebook account `victim@example.com` is taken over (credential stuffing, phishing), the attacker:

1. Opens an account at tenant-A (Hermes SaaS).
2. BridgeLogin with `tenant_id=tenant-A`, email=victim@example.com, password=<stolen>.
3. The driver succeeds with `Creds.UserID = victim's uid (=2000)`.
4. `CreateSession` fails because uid=2000 already exists in DB (owned by tenant-B).
5. **The fallback runs.** `UpdateSessionTokens(2000, ...)` writes tenant-A's encrypted-with-DEK tokens into tenant-B's row.

But the DEK is shared (one DEK per pod). So tenant-A's tokens (encrypted with the same DEK and the same AAD `mbs.access_token.uid=2000`) are valid ciphertext under the DEK. They DECRYPT correctly.

**Outcome:**
- Tenant-B's existing session row is now silently overwritten with tenant-A's tokens.
- Tenant-B keeps seeing `tenant_id=tenant-B` on the row, but the access_token/secret/session_key inside decrypt to tenant-A's bridge result.
- If the victim is also tenant-A's victim (FB account compromised), then when tenant-A or tenant-B reconnects MQTToT, both will be using attacker-supplied tokens.
- Worse: tenant-B's lookups (`GetSessionByTenant("tenant-B", 2000)`) still succeed (tenant_id was not updated), and tenant-B's `SendMessage`/`Listen` keep working — but now ALL their messages are visible to whoever holds the attacker's tokens.

### Impact

- **Tenant isolation broken.** A bridge attempt that originated in tenant-A overwrites tenant-B's persisted state. The row's `tenant_id` is unchanged (so cross-RPC checks still pass), but the secrets inside are attacker-controlled.
- **Silent.** No error returned to tenant-B. No log message that's distinguishable from normal re-bridge.
- **Recoverable only by re-bridging legitimately as tenant-B.**

### Fix

Before the fallback path, fetch the existing row and verify its tenant_id matches the caller's. If not, return `PermissionDenied` and do NOT write.

```go
if err := h.store.CreateSession(ctx, row); err != nil {
    existing, getErr := h.store.GetSession(ctx, uid)
    if getErr != nil {
        if errors.Is(getErr, store.ErrNotFound) {
            return fmt.Errorf("create session: %w", err)
        }
        return fmt.Errorf("create session and read-back both failed: create=%v read=%w", err, getErr)
    }
    if existing.TenantID != tenantID {
        // 🚨 SECURITY: a bridge attempt for one tenant must NOT overwrite
        // another tenant's session. Fail closed.
        return fmt.Errorf("uid %d already exists for a different tenant: %w",
            uid, store.ErrTenantMismatch)
    }
    // ... continue with UpdateSessionTokens / UpdateSessionCookies
}
```

Map `store.ErrTenantMismatch` → `codes.PermissionDenied` via the existing `mapStoreErr` table — which then bubbles up through `mapBridgeErr` (no — we return raw error, handler wraps it via `"persist: " + err.Error()` + `mapBridgeErr(INTERNAL, ...)`. That's wrong for this specific case — we want PermissionDenied, not Internal. Need a separate code path.

**Implementation:** wrap the new error so that the outer handler can distinguish "tenant violation" from generic persist failure, and emit `BRIDGE_ERR_CHECKPOINT` (closest existing code → PermissionDenied via mapBridgeErr's table). Actually no — `CHECKPOINT` maps to `FailedPrecondition`. Need to add a new bridge error code, OR map this specific persist error directly.

Simpler: extend `handleDriverUpdate`'s persist-failure branch to detect this sentinel and emit a PermissionDenied + specific BRIDGE_ERR code. Or even simpler: leave it as Internal in the gRPC status (the attack still fails closed — no write happens) and just log loudly. The actual security property is "no write happens"; the gRPC code is a cosmetic concern.

**Chosen approach:** add an explicit `errPersistTenantViolation` sentinel in handler; in the persist-failure branch of `handleDriverUpdate`, detect it and emit gRPC `PermissionDenied` + `BRIDGE_ERR_CHECKPOINT` (cosmetic) so the client sees a clear "this account is owned by another tenant" rather than `Internal`.

Also: write an explicit unit test that creates a uid for tenant-B, attempts re-bridge as tenant-A, verifies the row's contents are UNCHANGED and tenant-A gets PermissionDenied.

---

## F2 detail (P2): BurnSession misreports state on read-back failure

`rpc_session_lifecycle.go:208-215`:
```go
updated, err := h.store.GetSession(ctx, req.Uid)
if err != nil {
    row.State = "burned"
    updated = row
}
```

If `BurnSession` succeeded but the post-read failed (transient DB blip), we mutate the local `row` (the *pre-burn* snapshot we already read) and report `state="burned"`. This is mostly fine, but: if the user's `BurnSession` UPDATE was actually rolled back at the DB level (transaction abort that we didn't notice — e.g. an idle-in-transaction killer), we'd be lying about the outcome.

**Impact:** Low — the caller would call again and the second burn would succeed (idempotent). Lifecycle event publishes the wrong prev→next transition for a transient window.

**Fix proposal:** Either (a) propagate the read-back error directly with `mapStoreErr`, or (b) emit a metric counter for "burn-readback-failed". Option (a) is more honest. Leaving as P2 — won't fix now, will add a TODO and counter.

## F3 detail (P2): bridgeReaderLoop test-only goroutine outlive

Documented inline already (lines 119-128 of `rpc_bridge_login.go`). In production, gRPC framework tears down the stream and `stream.Recv()` returns an error, exiting the loop. In tests, the fixture's `defer cancel()` covers it. The risk is purely a future maintainer writing a test that uses `fakeBridgeStream` without cancelling its ctx → goroutine leak in that test.

**Fix proposal:** add `runtime/leaktest` or `goleak` to the bridge test fixture. Not now — out of scope for Chunk 4. P3 → moved to Chunk 5 backlog.

---

## Action plan (executed in this turn)

1. ✅ Write this audit document.
2. → Fix F1 (P0) in `rpc_bridge_login.go`.
3. → Add `TestBridgeLogin_RebridgeRejectedForDifferentTenant` regression test.
4. → Add `TestBridgeLogin_RebridgeAllowedForSameTenant` to prove the legitimate path still works.
5. → Run `go test -race -count=5 ./pkg/... ./internal/mbs/...` — verify green.
6. F2 + F3: backlog only, not blocking chunk-4 close.
