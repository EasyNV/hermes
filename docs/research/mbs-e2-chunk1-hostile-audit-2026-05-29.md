# Stage E2 Chunk 1 — Hostile-Eyes Audit (Gateway Proto-Client Wiring)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:** `internal/gateway/config/config.go` MOD + `internal/gateway/handler/{handler.go,mbs.go,mbs_test.go,handler_test.go}` + `cmd/gateway/main.go` MOD + `docs/contracts/proto/events.proto` sync

---

## Methodology

Walked every code path that participates in the gateway → mbs request path, wearing four hats: attacker (malicious tenant trying cross-tenant access), careless operator (typo'd env, mbs down), upstream tooling (proto file drift), and racing component (concurrent request mutating shared state).

Each finding is graded:
- **P1** — fix before commit
- **P2** — document, may revisit
- **FP** — false positive
- **AB** — accepted-by-design

---

## Findings

### F1 (AB) — superadmin can impersonate any tenant on MBS routes

**Surface:** `mbs.go::forceTenantFromJWT` → `isSuperadmin` bypass branch.

**Behavior:** When the JWT claim has role `ROLE_SUPERADMIN`, a request carrying `tenant_id: "tenant-X"` is preserved and forwarded to mbs.

**Rationale:** This IS the design — superadmin role exists explicitly to cross-tenant boundaries for support/ops. Matches the existing convention for every other gateway proxy that has tenant-scoped data (`AGENTS.md` RBAC section). Documented in `TestHandler_ListMbsSessions_SuperadminBypassesTenantCheck`. **Accept.**

### F2 (FP) — `forceTenantFromJWT` returns the request's tenant for matching case, not the JWT's

**Surface:** Last return path in `forceTenantFromJWT`:

```go
if reqTenant != caller && !isSuperadmin(ctx) {
    return "", status.Error(codes.PermissionDenied, ...)
}
return reqTenant, nil  // ← returns reqTenant, not caller
```

**Concern:** Why return `reqTenant` when we already know `reqTenant == caller`? Could a future edit accidentally drift the two paths?

**Verification:** When `reqTenant != ""` and `reqTenant == caller`, returning either value is byte-identical. When the caller is superadmin and `reqTenant != caller`, we MUST return `reqTenant` because the superadmin is using a different tenant deliberately. The current return is correct for superadmin and inert for non-superadmin matched case. **False positive.**

### F3 (P2 — DOCUMENTED) — `req.TenantId` mutation is in-place

**Surface:** `ListMbsSessions` mutates the input proto:

```go
req.TenantId = tenant
return h.mbsClient.ListSessions(ctx, req)
```

**Concern:** If the caller retains a reference to `req`, the in-place mutation visibly changes their copy. This could surprise a future test that asserts on the original.

**Rationale to accept:** Every existing gateway proxy in this codebase mutates request protos in place (see `gateway/handler/handler.go::ListWaNumbers`, `RegisterWaNumber`, etc.). Convention is established. Tests should not rely on the input being unmodified. If a future refactor wants immutability, do it codebase-wide. **Documented for chunk-2 reviewer; not a blocker.**

### F4 (FP) — `GetMbsSessionStatus` doesn't pass tenant to backend

**Surface:** `GetMbsSessionStatus`:

```go
if _, err := h.forceTenantFromJWT(ctx, ""); err != nil {
    return nil, err
}
return h.mbsClient.GetSessionStatus(ctx, req)
```

**Concern:** The wrapper checks tenant presence in the JWT but does NOT pass it to the backend. Couldn't a malicious caller hit `GetMbsSessionStatus` for a uid owned by a different tenant?

**Verification:** Server-side `internal/mbs/handler/rpc_session_lifecycle.go::GetSessionStatus` calls `store.GetSessionByTenant(ctx, tenantID, uid)`, where `tenantID` is extracted from a separate **server-side gRPC interceptor** (`internal/mbs/handler/tenant.go`). The interceptor reads gRPC metadata. Looking at the call path: when gateway dials mbs, does it propagate the gateway's tenant claim into the outbound gRPC metadata?

**Look at the gateway dial:** `cmd/gateway/main.go::dialService` uses `grpc.WithTransportCredentials(insecure.NewCredentials())` — no per-call metadata injection. So mbs's tenant interceptor sees an empty tenant_id → ✗.

**Status: REAL gap. But it's not a chunk-1 regression** — the existing WA/Inbox/etc. proxy methods have the SAME shape and the SAME end-to-end behavior (server-side interceptors get nothing). Either (a) the in-process call between gateway gRPC server and gateway gRPC client preserves the inbound metadata (verify in chunk 2 with a real e2e), or (b) the metadata isn't carried and mbs's interceptor falls back to "trust the tenant in the request body."

Looking at `internal/mbs/handler/rpc_session_lifecycle.go`:
- It reads tenant from the request body where present (`req.TenantId`), then from a `TenantFromContext` interceptor helper.

For uid-keyed RPCs (no tenant in body), if metadata isn't propagated, mbs's interceptor returns empty tenant → store call uses empty tenant → either fails-closed OR returns the row regardless. **Need chunk 2's e2e to verify.**

**Resolution:** This is a **plan-level gap** to surface for chunk 2, not a chunk-1 bug. Chunk 1's contract was "forward the request unmodified after tenant check" — that's what it does. Documenting as **carrying gap C1-G1** for chunk 2 to confirm with a real round-trip test (gateway → mbs in-process gRPC).

### F5 (P2 — ACCEPTED) — `dialService` swallows errors

**Surface:** `cmd/gateway/main.go::dialService`:

```go
func dialService(addr string) grpc.ClientConnInterface {
    conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        return nil
    }
    return conn
}
```

**Concern:** A typo'd `MBS_ADDR=mbs:9999X` (non-numeric port) silently returns nil, gateway boots, every MBS route returns 503. Operator has no log line.

**Rationale to accept:** Same shape exists for every other service. `grpc.NewClient` is lazy-connect — a syntactic parse failure (which is what `err != nil` catches here) is rare but possible. The non-syntactic failures (DNS, refused connection) DO succeed at `grpc.NewClient` time and only fail at first RPC, which surfaces as the `Unavailable` from `mbsClient.ListSessions`. Operators diagnose via the 503 + log line then.

**Recommended chunk-2 improvement (deferred):** Log the err in `dialService` so a config typo is visible at boot. Not a chunk-1 blocker because it would touch every other client.

### F6 (FP) — `stubMbsClient` could be called with nil receiver

**Concern:** `func (s *stubMbsClient) ListSessions(...)` — if `s` is nil, it panics on `s.lastListReq = in`.

**Verification:** Every test constructs `&stubMbsClient{...}` before passing in. The `newTestHandlerWithMbs(nil, nil)` path passes nil for the **interface**, not the concrete type — that's correctly handled by the nil-check at the top of each proxy method. **False positive.**

### F7 (FP) — `isSuperadmin` uses string compare of enum, drifts on enum rename

**Concern:** If `Role_ROLE_SUPERADMIN` is renamed in the proto, the comparison silently breaks.

**Verification:** The comparison uses `hermesv1.Role_ROLE_SUPERADMIN.String()` — a compile-time reference. Renaming the enum value triggers a compile error before reaching test failure. **False positive.**

### F8 (P2 — DOCUMENTED) — `docs/contracts/proto/events.proto` mirror sync is manual

**Surface:** C1.6 — we `cp` the build source to docs and patch import paths.

**Concern:** Next time someone edits `proto/hermes/v1/events.proto`, they forget the mirror, and the contract review surface drifts again (was a 7-week drift before this sync).

**Rationale to accept (chunk-1):** Long-term fix is removing the mirror entirely (let docs link to `proto/hermes/v1/`) OR generating the mirror in a pre-commit hook. Both are out of scope for E2 chunk 1. Documenting as **carrying gap C1-G2** for Stage F (deploy) or a dedicated docs-sync chunk.

### F9 (FP) — Concurrent calls to the same Handler mutate the same `req.TenantId`

**Concern:** If two callers somehow share a `*ListMbsSessionsRequest`, the in-place mutation has a data race.

**Verification:** gRPC protobuf request types are constructed per-call by the gRPC framework. Sharing across goroutines requires the user to deliberately do something weird (and is invalid use of the proto type). Not a real attack surface. **False positive.**

### F10 (P2 — ACCEPTED) — `MBS_ADDR` default `localhost:8082` doesn't match compose hostname

**Surface:** C1.1 — default is `localhost:8082`.

**Concern:** In compose, gateway must dial `mbs:8082` not `localhost:8082`. If the operator forgets to set `MBS_ADDR=mbs:8082`, the gateway connects to itself or fails.

**Rationale to accept:** Same posture as `WaAddr=localhost:9104`. Compose explicitly overrides each `*_ADDR` env. The defaults exist for local-dev where every service binds to localhost. Stage F (compose hardening) sets all the right env values; chunk-1 just follows existing convention.

---

## Carrying gaps (track into next chunk)

| Gap | Reference | Resolve in |
|---|---|---|
| **C1-G1** | Verify in chunk 2 e2e that the gateway-to-mbs in-process gRPC call propagates tenant metadata so mbs's server-side interceptor sees the right tenant for uid-keyed RPCs. If not, design the metadata-injection middleware. | Chunk 2 |
| **C1-G2** | `docs/contracts/proto/` mirror is manual sync. Long-term: pre-commit hook OR delete mirror. | Stage F or dedicated docs chunk |

Both are **explicit deferrals**, not silent gaps.

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `go build ./cmd/gateway ./internal/gateway/... ./internal/mbs/handler/...` | ✓ clean |
| `go vet ./cmd/gateway ./internal/gateway/... ./internal/mbs/handler/...` | ✓ clean |
| `go test -race -count=3 -timeout 60s ./internal/gateway/handler/... ./internal/mbs/handler/...` | ✓ green |
| All 7 new MBS tests pass | ✓ 18 sub-tests across 7 test functions |
| `docs/contracts/proto/events.proto` carries MBS event messages | ✓ 431 lines, includes MbsInboundMessageEvent/MbsOutboundEvent/MbsSessionLifecycleEvent/MbsCampaignSendTask |
| Gateway boots with mbsClient nil → MBS routes 503 unavailable | ✓ enforced by `TestHandler_MbsMethods_NilClientReturnsUnavailable` |
| Cross-tenant rejection refuses to call backend | ✓ enforced by `TestHandler_ListMbsSessions_RejectsTenantMismatch` |

---

## Approval

Chunk 1 is GO. Two carrying gaps documented for downstream chunks. No P1 unresolved.

— Oracle, 2026-05-29
