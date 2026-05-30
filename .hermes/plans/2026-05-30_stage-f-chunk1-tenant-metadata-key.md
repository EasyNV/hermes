# Stage F follow-up — Chunk 1: gRPC tenant metadata key alignment

**Status:** plan + contracts
**Date:** 2026-05-30
**Predecessor:** chunk 5 (a117bb2 reverse-proxy + runbooks), chunk 4 (52c1738 readiness probes)
**Successor:** chunk 2 (UpsertAssets store impl)

---

## 1. Problem

The gateway sends gRPC outgoing metadata under keys `tenant-id` and `user-id`. The hermes-mbs server-side interceptor (`internal/mbs/handler/tenant.go`) reads `x-tenant-id` (declared as `TenantMetadataKey = "x-tenant-id"`). **Mismatch.**

Result: every MBS REST route routed through the running prod stack returns:

```
HTTP 401  {"code":"Unauthenticated","message":"missing x-tenant-id metadata header"}
```

despite a valid JWT. The MBS Pages UI page calls `/api/v1/mbs-sessions` on mount → 401 → empty table.

**Why this shipped:** unit tests in `internal/gateway/handler/mbs_test.go` asserted `lastMD.Get("tenant-id")` — the same wrong key the gateway was emitting. Tests passed by validating a contract no server actually honoured. The bug only surfaces under in-process gRPC dispatch (gateway → embedded mbs client), which the in-process unit tests bypassed by mocking the client directly.

## 2. Blast radius

```
$ grep -rn '"tenant-id"\|"user-id"' --include='*.go' | grep -v _test.go
internal/gateway/handler/mbs.go:102:    "tenant-id": tenantID,        ← unary RPC path
internal/gateway/handler/mbs.go:103:    "user-id":   middleware...,
internal/gateway/rest/mbs_bridge_ws.go:156: "tenant-id": claims.TenantID,  ← WS BridgeLogin path
internal/gateway/rest/mbs_bridge_ws.go:157: "user-id":   claims.UserID,
```

Two senders. The unary fix landed uncommitted on the working tree earlier this session — covers `/api/v1/mbs-sessions{,/:uid,/:uid/{assets,burn,resolve-phone,messages}}`. The WS path (`/ws/mbs-bridge-login`) is the second gap. Without fixing it, the BridgeLogin UI dialog completes the WS handshake but every downstream RPC to mbs returns the same 401, the dialog hangs at "Connecting to Meta…".

## 3. Spec

### 3.1 Authority

`internal/mbs/handler/tenant.TenantMetadataKey` is the single source of truth. Constant value `"x-tenant-id"`. All outgoing-metadata producers MUST import this constant — no string literals.

User-id key has no constant (no server-side reader yet). Spec-define it as `"x-user-id"` to follow the convention (any custom metadata key in gRPC SHOULD use `x-` prefix per gRPC-go conventions; bare `user-id` collides with reserved names in some HTTP-to-gRPC bridges).

### 3.2 Changes

| File | Change |
|---|---|
| `internal/gateway/handler/mbs.go` | `withTenantMetadata` writes `mbshandler.TenantMetadataKey` + `"x-user-id"` (uncommitted — already in tree from earlier session) |
| `internal/gateway/handler/mbs_test.go` | Assertions read `mbshandler.TenantMetadataKey` + `"x-user-id"` (uncommitted) |
| `internal/gateway/rest/mbs_bridge_ws.go` L156-157 | Same key alignment as the unary path |
| `internal/gateway/rest/mbs_bridge_ws_test.go` (if asserts the keys) | Update assertions to match |

### 3.3 Contracts

**C1-G1: Outgoing-metadata contract (gateway → mbs)**

```go
// MUST:
md := metadata.New(map[string]string{
    mbshandler.TenantMetadataKey: tenantID,  // "x-tenant-id"
    "x-user-id":                  userID,    // bare "user-id" / "tenant-id" FORBIDDEN
})
```

Any new gateway→mbs path MUST `import mbshandler "github.com/hermes-waba/hermes/internal/mbs/handler"` and use the constant.

**C1-G2: Test contract**

Tests asserting outgoing metadata MUST read the same constant. Forbidden anti-pattern:

```go
// FORBIDDEN — re-literalises the key, hides drift
if got := md.Get("tenant-id"); ...
```

**C1-G3: No display-layer-driven edits**

The Hermes terminal scrubber rewrites password-shaped strings to `***` in display. Do NOT paste displayed text into patches as old/new_string — work from on-disk bytes (`read_file`, `grep`) so `***` stays as a mask in display only, not literal file bytes.

### 3.4 Gates

| Gate | Description | How to verify |
|---|---|---|
| G1 | `go vet ./...` clean | terminal |
| G2 | `go test ./internal/gateway/...` green | terminal |
| G3 | `go test ./internal/mbs/handler/...` green | terminal |
| G4 | `make docker-build-gateway` builds | terminal |
| G5 | Live `curl /api/v1/mbs-sessions` returns 200 with valid JWT (instead of 401) | curl through Caddy |
| G6 | MBS Pages UI route renders the table with both imported sessions | browser |
| G7 | `grep -rn '"tenant-id"\|"user-id"' --include='*.go' | grep -v _test` returns ZERO non-x- hits | terminal |

### 3.5 Anti-goals

- No proto regen (no `.proto` change)
- No new RPC paths
- No frontend changes (the SPA only sees REST shapes; metadata is a wire detail)
- No DB migration

### 3.6 Rollback

`git revert` on the chunk-1 commit + `make docker-build-gateway` + `docker compose up -d --force-recreate gateway`. The chunk is self-contained (single commit, single image).

## 4. Build plan

1. Re-read uncommitted edits to `internal/gateway/handler/mbs.go` + `internal/gateway/handler/mbs_test.go` — confirm shape
2. Patch `internal/gateway/rest/mbs_bridge_ws.go` L156-157
3. Inspect `mbs_bridge_ws_test.go` — update assertions if any read the keys
4. `go vet ./... && go test ./internal/gateway/... ./internal/mbs/...`
5. `make docker-build-gateway` (need `buf` on PATH — `PATH="$HOME/go/bin:$HOME/.hermes/profiles/oracle/home/go/bin:$PATH"`)
6. `docker-compose -f docker-compose.prod.yml --env-file .env.prod up -d --force-recreate gateway`
7. Verify G5/G6 live
8. Commit
9. Write hostile audit doc

## 5. Risk

| Risk | Probability | Mitigation |
|---|---|---|
| Other in-tree consumers also use wrong keys | low | grep covers all .go files; only 2 senders found |
| WS BridgeLogin test asserts the old key | medium | Inspect `mbs_bridge_ws_test.go` before patching |
| Compose env leak (déjà vu) | low | `.env.prod` is canonical; parent shell already clean |
| `buf` PATH miss | known | Memory note has the workaround |
