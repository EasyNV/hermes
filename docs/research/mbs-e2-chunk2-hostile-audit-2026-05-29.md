# Stage E2 Chunk 2 — Hostile-Eyes Audit (REST + Bridge-Login WS)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `internal/gateway/handler/mbs.go` MOD (C1-G1 metadata-propagation fix)
- `internal/gateway/handler/mbs_test.go` MOD (metadata assertions)
- `internal/gateway/rest/rest.go` MOD (route registration + MbsRouter interface)
- `internal/gateway/rest/handlers_mbs.go` NEW (6 REST handlers)
- `internal/gateway/rest/mbs_bridge_ws.go` NEW (bidirectional WS bridge)
- `internal/gateway/rest/handlers_mbs_test.go` NEW (12 REST sub-tests)
- `internal/gateway/rest/mbs_bridge_ws_test.go` NEW (8 WS scenarios)
- `cmd/gateway/main.go` MOD (rest.New signature update)

---

## Methodology

Walked the WS bridge with three hats: malicious browser (cross-tenant attack via start.payload), racing pumps (concurrent Write/Send/Recv), and abrupt teardown (browser close, gRPC stream EOF, ctx cancel mid-frame). Walked the REST handlers as: path/body mismatch attack, malformed JSON, missing JWT.

Every finding is graded:
- **P1** fix before commit
- **P2** document, may revisit
- **FP** false positive (verified safe)
- **AB** accepted-by-design

---

## Findings

### F1 (FIXED IN DRAFT — was P1) — Race in test fake (`fakeMbsBridgeClient.stream`)

**Surface:** `mbs_bridge_ws_test.go::fakeMbsBridgeClient`

**Behavior:** Without a mutex, test goroutine polled `fake.stream` while the handler goroutine (running inside the httptest server) wrote `fake.stream = newFakeBridgeStream(...)` inside BridgeLogin. Caught by `go test -race`. **Fixed** by wrapping the field with `sync.Mutex` + helper methods (`streamRef`, `waitForStream`, `outgoingMD`).

**Status:** P1 caught + fixed during step 6 → step 8. All tests pass under `-race -count=3`.

### F2 (FP) — `safeWriteFrame` concurrent call from both pumps

**Surface:** `mbs_bridge_ws.go::safeWriteFrame`

**Concern:** `coder/websocket.Conn.Write` is NOT safe for concurrent calls. Pump A (gRPC→WS goroutine) and pump B's `safeSendWSError` (main goroutine, on parse failure) could both write.

**Verification:** Pre-mortem (plan H8) flagged this. Implementation uses a `sync.Mutex` (`writeMu`) passed into both `safeWriteFrame` and `safeSendWSError` for ALL pump-active calls. Race detector confirms no Write conflicts. **Mitigated by design.**

### F3 (P1 — FIXED) — Tenant force-overwrite must happen BEFORE `stream.Send`

**Surface:** `mbs_bridge_ws.go::bridgeLoginWS` step 7

**Test:** `TestBridgeWS_StartTenantForcedFromJWT` sends `start.payload.tenantId = "tenant-EVIL"` and asserts the BACKEND-recorded `BridgeLoginStart.TenantId == "tenant-A"` (the JWT tenant). Asserts outgoing gRPC metadata also carries `tenant-id: tenant-A`. **Status: mitigation verified by test.**

### F4 (FP) — Pump A leak when WS closes before gRPC stream

**Concern:** If the WS client closes mid-flow, will pump A keep running until the gRPC stream finishes on its own?

**Verification:** Pump B's `conn.Read` returns error → main loop breaks → `cancel()` fires on the grpcCtx → pump A's `stream.Recv()` observes cancelled context and returns. The `<-pumpADone` wait bounded by `time.After(3s)` catches the edge case where Recv is mid-frame. Tested implicitly by `TestBridgeWS_CancelFromBrowser` (cancel triggers identical teardown). **False positive.**

### F5 (P2 — DOCUMENTED) — `?token=` in URL leaks in proxy access logs

**Surface:** Both the existing `/ws` hub and the new `/ws/mbs/bridge-login` accept the JWT as a query parameter (browsers can't set Authorization headers on WS handshake).

**Concern:** Reverse-proxy access logs (nginx, cloudflare) typically log the full request URL including query params. JWT tokens leaking into log files is a real exposure.

**Rationale to accept:** Same shape as the existing hub. Operators MUST configure log scrubbing. This is not a new exposure introduced by chunk 2 — the hub already has it. **Documented for Stage F (deploy hardening) — log scrubbing playbook needed there.**

### F6 (P2 — ACCEPTED) — `dialBridgeWS` test helper depends on `coder/websocket.Dial`

**Concern:** Test uses real WS dial → real TCP socket against httptest.Server. CI might be flaky under network load.

**Verification:** httptest.Server is localhost-only. `coder/websocket.Dial` over localhost is microsecond latency. All bridge tests pass under `-race -count=3` consistently. **No real issue.**

### F7 (FP) — `safeSendWSError` could race with another concurrent writer

**Verification:** Already wraps via `safeWriteFrame` with the same `writeMu`. **False positive.**

### F8 (P2 — ACCEPTED) — `safeSendWSError` pre-pump phase passes `nil` mutex

**Surface:** Lines 130-150 of `mbs_bridge_ws.go`, before `writeMu` is declared.

**Concern:** If `safeSendWSError` is called with `nil` mutex, the write is unguarded. Could pump A start before that early call returns?

**Verification:** Pump A is spawned AFTER the early-error paths return. The pre-pump phase is strictly single-goroutine. Doc-comment on `safeWriteFrame` spells this out. **Accepted with documentation.**

### F9 (FP) — Path-vs-body uid mismatch attack via `resolveMbsPhone`/`sendMbsMessage`

**Surface:** `handlers_mbs.go`, both `resolveMbsPhone` and `sendMbsMessage` overwrite `req.Uid` from the path AFTER `readProto`.

**Verification:** Tested by `TestREST_ResolveMbsPhone_PathWinsOverBody` and `TestREST_SendMbsMessage_PathWinsOverBody`. Body's `"uid": 999` is overwritten with path `42`. **Mitigated by test.**

### F10 (P2 — ACCEPTED) — `MbsRouter` interface duplicates the proxy method signatures

**Surface:** `internal/gateway/rest/rest.go::MbsRouter`

**Concern:** If chunk-1's proxy method signatures drift, the REST adapter compile-breaks but the gateway handler doesn't.

**Verification:** Go's compile-time interface check catches this. Adding `var _ MbsRouter = (*handler.Handler)(nil)` would assert at package load. **NOT added now — would create a dependency cycle (`rest` already imports `handler` indirectly via the chunk-1 surface). Stage F refactor.**

### F11 (P2 — ACCEPTED) — `parseStateFilter` silently maps unknown to UNSPECIFIED

**Concern:** A client sending `stateFilter=ACTIVE` (without the proto prefix) gets UNSPECIFIED, which the backend treats as "no filter". Surprising for a careless caller.

**Rationale:** Backend behavior is "UNSPECIFIED = all", matching the chunk-1 contract. The frontend client (chunk 4) generates the prefixed string form from a TS const matching the proto. Operators using curl directly get the documented behavior; not a security concern. **Accepted.**

### F12 (P2 — ACCEPTED) — `writeMbsError` writes JSON via `json.NewEncoder` not the marshaler

**Surface:** `handlers_mbs.go::writeMbsError`

**Concern:** Uses `encoding/json` directly, bypassing the Adapter's protojson marshaler. Drift between error and success shape.

**Verification:** Error shape `{code, message}` is canonical and doesn't need protojson. The Adapter's protojson marshaler is for response proto messages, not free-form error JSON. The existing `Adapter.writeError` uses the same approach. **Convention-consistent.**

### F13 (P1 — RESOLVED) — Carrying gap C1-G1 from chunk 1

**Surface:** Chunk-1 unary proxy methods did NOT set outgoing gRPC metadata, so mbs's tenant interceptor would see anonymous calls for uid-keyed RPCs.

**Resolution in chunk 2 (C2.4):**
- Added `withTenantMetadata(ctx, tenantID)` helper in `mbs.go`.
- All 6 proxy methods now wrap ctx via `withTenantMetadata` before forwarding.
- `stubMbsClient.captureMD` records outgoing metadata.
- `TestHandler_MbsMethods_PropagateTenantMetadata` (6 sub-tests) asserts `tenant-id` + `user-id` flow correctly.
- `TestHandler_MbsMethods_SuperadminMetadataUsesRequestTenant` asserts superadmin-overridden tenant flows.

**Status: C1-G1 CLOSED.**

### F14 (P2 — ACCEPTED) — `bridgeWSIdleTimeout = 5min` may cut off slow 2FA users

**Concern:** A user reading a TOTP off a physical token may take >5min on a sluggish device.

**Rationale:** Mbs `BridgeOverallTimeout` defaults to 180s; the bridge gives 5min headroom. A 5-min idle 2FA flow is genuinely abnormal — the bridge cleanly terminates and the user can retry. Not worth raising. **Accepted.**

### F15 (P2 — ACCEPTED) — `bridgeWSReadLimit = 16KB` larger than necessary

**Concern:** Largest legit frame is the `start` with email+password (~ 200 bytes). 16KB is 80× too generous.

**Rationale:** 16KB is still tiny. Smaller cap would prevent a 1-frame DOS but doesn't measurably help against real attacks (a hostile WS client can spam 100 small frames just as easily). Keep generous. **Accepted.**

### F16 (FP) — `pumpADone` channel goroutine leak on early return

**Concern:** If bridge handler returns BEFORE spawning pump A (e.g., malformed first frame), there's no `<-pumpADone` wait, but no pump A goroutine either, so no leak. **False positive.**

### F17 (P2 — ACCEPTED) — Frame schema lives in two places: `mbs_bridge_ws.go` + chunk-4 TS types

**Concern:** Drift between gateway-emitted frames and frontend-consumed types.

**Rationale:** Chunk-4 (TS types) explicitly defines the frame names matching this file's `wsBridge*` constants. Defaults table in the chunk-2 plan lists them as a single source of truth. Long-term: gen TS from a shared spec. **Accepted as known-deferred.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `go build ./cmd/gateway ./internal/gateway/...` | ✓ clean |
| `go vet ./cmd/gateway ./internal/gateway/...` | ✓ clean |
| `go test -race -count=3 -timeout 120s ./internal/gateway/... ./internal/mbs/handler/...` | ✓ all green |
| REST handler tests | ✓ 12 sub-tests pass |
| WS bridge tests | ✓ 8 scenarios pass |
| Chunk-1 metadata-propagation tests | ✓ 8 new sub-tests pass |
| Cross-tenant rejection (REST) | ✓ inherited from chunk 1 + tested via 401 path |
| Tenant force-overwrite (WS) | ✓ `TestBridgeWS_StartTenantForcedFromJWT` |
| Nil mbsClient → HTTP 503 before WS upgrade | ✓ `TestBridgeWS_NilMbsClient_503BeforeUpgrade` |
| Missing JWT → 401 | ✓ REST + WS both tested |
| Backend errors propagated | ✓ REST 404 mapping; WS error-frame surface |

---

## Carrying gaps for chunk 3+

| Gap | Reference | Resolve in |
|---|---|---|
| **C2-G1** | Log scrubbing playbook for `?token=` query params — both `/ws` hub and `/ws/mbs/bridge-login` carry tokens in URL. | Stage F deploy hardening |
| **C2-G2** | Compile-time assertion `var _ MbsRouter = (*handler.Handler)(nil)` to lock the chunk-1 surface to the chunk-2 interface — currently would create a dependency cycle. | Stage F refactor |
| **C2-G3** | TS types in chunk 4 must match the frame names in `mbs_bridge_ws.go` defaults table. | Chunk 4 step 1 verification |

---

## Approval

Chunk 2 is GO.

- **C1-G1 CLOSED** (metadata propagation verified by 8 new tests).
- **No P1 unresolved.**
- 1 P1 (F1 test race) caught and fixed during step 6.
- 3 carrying gaps explicitly tracked for downstream chunks.

— Oracle, 2026-05-29
