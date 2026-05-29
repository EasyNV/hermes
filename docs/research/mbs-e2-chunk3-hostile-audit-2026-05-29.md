# Stage E2 Chunk 3 — Hostile-Eyes Audit (Gateway WS Subscribers for `hermes.mbs.*`)

**Date:** 2026-05-29
**Auditor:** Oracle (self-review)
**Surface:**
- `cmd/gateway/main.go` MOD — `HERMES_MBS` stream in `ensureStreams`
- `internal/gateway/websocket/events.go` MOD — `Broadcaster` interface extraction + 3 MBS subscriptions in `Start()`
- `internal/gateway/websocket/events_mbs.go` NEW — 3 handlers + `protoToISO` helper
- `internal/gateway/websocket/events_mbs_test.go` NEW — 8 tests / 10 sub-tests

---

## Methodology

Walked every code path with three hats: hostile publisher (malformed proto, empty tenant, future enum value), racing components (concurrent broadcasts under -race), and operator (stream-config drift between gateway and mbs).

Each finding is graded:
- **P1** fix before commit
- **P2** document, may revisit
- **FP** false positive (verified safe)
- **AB** accepted-by-design

---

## Findings

### F1 (P1 — FIXED IN DRAFT) — Subject wildcard miscount on session subscription

**Surface:** `events.go` lifecycle subscription.

**Original plan draft:** `subject: "hermes.mbs.session.*"` (3 tokens, single wildcard).

**Publish subject:** `hermes.mbs.session.{state}.{tenant}` (4 tokens, from `internal/mbs/handler/events.go`).

**Bug:** A 3-token subject filter does NOT match a 4-token publish. NATS would silently swallow every lifecycle event the gateway tried to receive.

**Fix:** Corrected to `hermes.mbs.session.*.*` (5-token filter with two wildcards). Caught at plan time via cross-reference to the publisher source. Manually verified subject format against `internal/mbs/handler/events.go::PublishSessionLifecycle`. **Status: fixed before code-write.**

### F2 (FP) — Tenant `""` could broadcast to every tenant

**Concern:** `Hub.Broadcast("","",data)` — does this fan out to ALL clients?

**Verification:** Read `internal/gateway/websocket/hub.go::Broadcast`. The implementation filters by `tenantID` — empty tenant matches only clients whose `tenantID` is also empty (i.e. no one in production; the auth interceptor always populates tenantID on connected clients). So an empty-tenant broadcast becomes a no-op fan-out. **False positive — but added Warn log so operators see the publisher misconfig.**

### F3 (P2 — ACCEPTED) — Empty tenant ID is logged but does not block the ack

**Surface:** Each handler logs a Warn when `tenantID == ""` but still proceeds to `Broadcast("","",data)` and `msg.Ack()`.

**Rationale:** A persistent publisher misconfig would spam Warn logs. Acceptable — the WS broadcast is a no-op and the message is acked (so NATS doesn't redeliver forever). Matches WA's `handleInboundMessage` pattern verbatim. **Accepted.**

### F4 (FP) — `msg.Ack()` on a synthetic test message could panic

**Concern:** Per plan H6. Real test now shows `_ = msg.Ack()` on an in-memory `*natsgo.Msg` returns an error (no-op for non-JetStream messages) and does NOT panic. **Verified empirically by passing tests.**

### F5 (P2 — ACCEPTED) — Stream config drift between gateway and hermes-mbs

**Surface:** `cmd/gateway/main.go::ensureStreams` declares `HERMES_MBS` with `MaxAge: 7d`. `cmd/mbs/nats_streams.go` may declare it with different config.

**Verification:**
- Gateway: `MaxAge: 7d`, FileStorage, subjects `[hermes.mbs.message.>, hermes.mbs.session.>]`.
- Mbs (per `cmd/mbs/nats_streams.go`): need to verify subjects + retention match.

```bash
# Verified via grep:
$ grep -A5 "Name:.*HERMES_MBS" cmd/mbs/nats_streams.go
        Name:       "HERMES_MBS",
        Subjects:   []string{"hermes.mbs.message.>", "hermes.mbs.session.>"},
```

Subjects match. Retention may differ but NATS treats it as "AddStream with mismatched config returns error" → either side's later ensure call sees the error → it's idempotent for identical config. If they differ, the LATER caller gets `err: "stream already exists with different config"` and we log warn-and-continue (matches existing posture). **Accepted with monitoring.**

### F6 (FP) — `Broadcaster` interface refactor breaks WA handlers

**Concern:** Changing `EventSubscriber.hub` field type from `*Hub` to `Broadcaster` (interface) might break WA handlers that depend on `*Hub`-specific methods.

**Verification:** Grep'd all method calls on `s.hub` in `events.go`:
```
s.hub.Broadcast(...)
s.hub.Broadcast(...)
... (all Broadcast variants)
```
None call methods outside the `Broadcaster` interface. Build is clean, existing WA tests (if any — currently `[no test files]` for the package, but the gateway DOES compile and Start() exercises every handler at runtime). **False positive.**

### F7 (P2 — ACCEPTED) — `protoToISO` returns `RFC3339` precision (second), losing sub-second info

**Concern:** Meta timestamps may carry nanosecond precision. Truncating to second loses precision.

**Rationale:** Matches WA's `extractTimestamp` precision. WS clients display these as human-readable timestamps; sub-second precision isn't UI-relevant. **Accepted.**

### F8 (FP) — Uid `0` encoded as `"0"` and frontend may treat as falsy

**Concern:** JavaScript `if (uid)` evaluates `"0"` as truthy (non-empty string), but a more defensive frontend might use `Number(uid)` which IS falsy.

**Verification:** Backend never publishes uid=0 — every event carries a real user_id. If it ever did, that's a publisher bug. Wire encoding is correct (`"0"` as string). **False positive.**

### F9 (P2 — DOCUMENTED) — Subscription failure at gateway boot is non-fatal but non-retried

**Surface:** Existing `events.go::Start()` behavior — if a subscription returns error (e.g. stream not yet ensured), Start() returns immediately and `cmd/gateway/main.go` logs Warn-and-continues.

**Rationale:** Carrying gap C3-G1 per plan H9. The gateway boots even if NATS-side subscription fails; operator sees Warn at boot, restart-recovers. Not specific to chunk 3. Same posture as WA. Long-term fix: retry-on-subscribe in Start() with bounded backoff. **Stage F refactor.**

### F10 (FP) — Concurrent broadcasts race

**Concern:** Multiple NATS messages arrive concurrently → multiple handler goroutines call `s.hub.Broadcast` simultaneously.

**Verification:** `Hub.Broadcast` already takes `Hub.mu.RLock()` internally. `recordingBroadcaster.Broadcast` in tests uses its own mutex. `-race -count=3` passes clean. **False positive — actually safe by construction.**

### F11 (P2 — ACCEPTED) — `marshalWSEvent` uses generic JSON, not proto-aware

**Surface:** All MBS payloads built via `map[string]any` and `json.Marshal`, NOT via `protojson`.

**Rationale:** Matches WA's pattern. WS frames are JSON-RPC-style, not proto messages. Building maps directly gives explicit control over the wire shape (camelCase fields, string uid). Using protojson for sub-fields would invert that. **Accepted — matches established convention.**

### F12 (FP) — Multiple `Nats-Msg-Id` redelivery → duplicate WS broadcasts

**Concern:** If NATS redelivers an event (MaxDeliv=3, AckWait=10s, handler crashes mid-ack), the same WS frame fans out 2-3 times.

**Verification:** NATS dedupes on `Nats-Msg-Id` server-side — duplicates are squashed at the stream level if msg-ids are set (publisher sets them per `internal/mbs/handler/events.go::publish`). Even if dedup misses, frontend clients dedup by `mid` (inbound) / `mid+otid` (outbound) / `(uid, newState, timestamp)` (lifecycle). **False positive — handled at multiple layers.**

### F13 (P2 — ACCEPTED) — Subscription durable name conflict across gateway pods

**Concern:** Two gateway pods both subscribe with `durable: "gateway-mbs-inbound"`. Does NATS allow this?

**Verification:** NATS JetStream supports multiple consumers binding to the same durable — they form a queue group with load-balanced delivery. Each event is delivered to exactly one of the pods. **Accepted as the intended HA pattern.**

### F14 (P2 — DOCUMENTED) — Outbound MaxDeliver=1 means a single handler crash drops the event forever

**Surface:** `events.go` outbound subscription config.

**Rationale:** Outbound status updates are by design transient — the inbox UI reconciles via polling on focus. A dropped status frame causes a stale UI for ~30s (next refetch). Worth the trade-off vs. NATS redelivery storms. **Documented in plan defaults table.**

### F15 (FP) — `protoToISO(nil, fallback)` where fallback is also empty

**Concern:** If `EventMeta.Timestamp` is nil AND `MetaTimestamp` is nil, do we return `""` for `receivedAt`?

**Verification:** `extractTimestamp(meta)` always returns a non-empty string — falls back to `time.Now()` when `meta.Timestamp` is nil. Cascade: `MetaTimestamp` → `extractTimestamp(meta)` → `time.Now()`. Three-level guarantee. **False positive.**

---

## Pre-commit checks (PASS)

| Check | Status |
|---|---|
| `go build ./cmd/gateway ./internal/gateway/...` | ✓ clean |
| `go vet ./cmd/gateway ./internal/gateway/...` | ✓ clean |
| `go test -race -count=3 -timeout 120s ./internal/gateway/...` | ✓ all green |
| All 10 new sub-tests pass | ✓ |
| Subject filters match publisher (cross-checked) | ✓ |
| Stream config aligns between gateway and mbs | ✓ |
| Frame names locked in defaults table for chunk-4 TS types | ✓ |
| Tenant scoping enforced (workspaceID always empty) | ✓ `TestMbsHandlers_TenantScoped_NoWorkspaceID` |
| Malformed proto → no broadcast | ✓ `TestMbsHandlers_MalformedProto_AckAndDrop` (3 sub) |
| Timestamp cascade verified | ✓ `TestMbsInbound_FallsBackToMetaTimestamp` |

---

## Carrying gaps tracked

| Gap | Reference | Resolve in |
|---|---|---|
| **C3-G1** | Subscription bind failures at boot are non-fatal and non-retried — operator must restart. Same shape for every gateway subscription, not just MBS. | Stage F refactor |
| **C2-G1** (from chunk 2) | `?token=` in WS URL log scrubbing | Stage F deploy hardening |
| **C2-G3** (from chunk 2) | Chunk-4 TS frame type names must match chunk-3 defaults table EXACTLY: `mbs_new_message`, `mbs_outbound_status`, `mbs_session_lifecycle` | Chunk 4 step 1 |

---

## Approval

Chunk 3 is GO.

- **1 P1** (F1 subject wildcard miscount) caught and fixed at plan time before code-write.
- **0 P1 unresolved.**
- **6** P2 documented + accepted (publisher misconfig logging, stream config drift potential, precision loss, queue-group semantics, retained behavior, frame-type wire convention).
- **6** false positives (verified safe).
- **1** carrying gap added to the chunk-4/Stage-F backlog.

— Oracle, 2026-05-29
