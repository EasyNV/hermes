# Stage E1 — Chunk 7 — Step 7 — Hostile-Eyes Audit

**Date:** 2026-05-29
**Auditor:** Oracle (red team against own refresh ticker)
**Scope:** `internal/mbs/refresh/` — 4 source files + 3 test files, ~1,000 LOC of impl + ~1,200 LOC of tests
**Method:** Walk every code path that touches: context cancel propagation, goroutine spawn, shared mutable state, defer-stack ordering, the persist-vs-Ping race, the partial-merge-with-sentinel case, and the boundary between Ticker.Run lifecycle and cmd/mbs/main drain.

## Verdict

**1 P1 found and fixed during audit pass. 4 P2 documented as acceptable behavior. Everything else clean.**

The P1 (F2) was a silent value-return Latency bug — the deferred mutation never reached the caller because Go evaluates return values before defers when the return isn't named. Caught + fixed + pinned by a dedicated test.

## Findings

| # | Sev | Title | File:line | Status |
|---|---|---|---|---|
| F1 | — | Concurrent attemptRefresh on same row mutates envelope+signal | `attempt.go:147-153` | Not actionable — list returns unique rows per tick |
| **F2** | **P1** | **`attemptRefresh` returns value-copy before defer mutates Latency** | `attempt.go:56-60` | **FIXED in this audit pass** — converted to named return |
| F3 | P2 | tickOnce mid-fanout ctx cancel — `wg.Wait()` blocks on already-spawned goroutines | `ticker.go:218-241` | Acceptable — child ctx propagates cancel, attempts exit quickly |
| F4 | P2 | First `tickOnce(ctx)` before ticker loop runs even when ctx already canceled | `ticker.go:170` | Acceptable — list query fails fast on canceled ctx, single spurious log |
| F5 | P2 | Persist failure mid-merge leaves stale envelope in DB; next tick re-fetches and retries | `attempt.go:166-181` | Acceptable — refresh is idempotent, retry on next tick |
| F6 | — | `Ping` can return BOTH non-nil signal AND sentinel error; we discard partial cookies on burn/suspend | `attempt.go:117` | Verified intentional — partial-checkpoint cookies aren't useful for next refresh |
| F7 | — | `Run` always returns nil today; main logs `log.Error()` only on non-nil | `main.go:230` | Verified — no spurious log on clean shutdown |
| F8 | P2 | `web.New` cookie-validation failure → `errClient.Ping` returns the construction error → classified transient | `ticker.go:317-330` | Acceptable — bad-cookies fall through to operator triage |
| F9 | P2 | tickOnce taking longer than Interval coalesces via time.Ticker buffer cap=1 | `ticker.go:172-182` | Acceptable — next tick fires immediately on completion |

## Audit walkthrough by checkpoint

### Checkpoint 1 — Named return + defer correctness (F2: the P1)

```go
func (t *Ticker) attemptRefresh(ctx context.Context, row *store.SessionRow) attemptResult {  // ← OLD
    start := t.nowFn()
    result := attemptResult{UID: row.UID}
    defer func() {
        result.Latency = t.nowFn().Sub(start)  // ← writes to LOCAL `result`
    }()
    ...
    return result  // ← Go semantics: COPIES result to return slot, THEN runs defers
                   // ← defer mutation lost
}
```

Go spec, [Defer Statements](https://go.dev/ref/spec#Defer_statements): "Each time a 'defer' statement executes, the function value and parameters to the call are evaluated as usual and saved anew but the actual function is not invoked." Combined with [Return statements](https://go.dev/ref/spec#Return_statements): "The return value or values may be explicitly listed in the 'return' statement. Each expression must be single-valued and assignable to the corresponding element of the function's result type."

When the function uses **unnamed** returns, `return result` is sugar for "copy result into the implicit return slot, then return." The defer runs AFTER the copy. Mutation of `result` doesn't propagate.

When the function uses **named** returns, the return slot IS the named variable. `return` (bare or with explicit value) commits the current value of the named variable, defers run with access to the same storage, and post-defer the named variable's value is what the caller sees.

**Fix:**

```go
func (t *Ticker) attemptRefresh(ctx context.Context, row *store.SessionRow) (result attemptResult) {
    start := t.nowFn()
    result = attemptResult{UID: row.UID}
    defer func() {
        result.Latency = t.nowFn().Sub(start)
    }()
    ...
    return  // bare return — uses named slot
}
```

Verified by `TestAttempt_LatencyIsPopulated`: injects a NowFn that advances 5s between start and end. Pre-fix: Latency = 0. Post-fix: Latency = 5s.

This bug would have shown up as 100% of refresh attempts logging `latency=0s` in production. Easy to miss because attempts succeed (state changes correctly); only the metric/log is wrong. Caught here, pinned by test.

### Checkpoint 2 — ctx cancel propagates through fan-out

```go
sem := make(chan struct{}, t.concurrency)
for _, row := range rows {
    if ctx.Err() != nil { break }  // outer guard
    select {
    case sem <- struct{}{}:
    case <-ctx.Done(): wg.Wait(); return  // sem-wait guard
    }
    wg.Add(1)
    go func(row *store.SessionRow) {
        defer wg.Done()
        defer func() { <-sem }()
        actx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
        defer cancel()
        r := t.attemptRefresh(actx, row)
        ...
    }(row)
}
wg.Wait()
```

Three cancel paths:

1. **Cancel before any goroutine spawned.** Outer `if ctx.Err() != nil { break }` catches → wg.Wait() returns immediately (no Add calls) → tickOnce returns. ✓
2. **Cancel while waiting for sem slot.** Select picks `ctx.Done()` → drain via wg.Wait → return. Already-running attempts complete naturally. ✓
3. **Cancel mid-attempt.** Each attempt has its own `actx` derived from parent `ctx`. Parent cancel → child cancel → attemptRefresh's `client.Ping(actx)` sees ctx.Err() and returns. classifyRefreshErr → `actionTransientError`, reason `ctx_canceled`. No state change. ✓

Pinned by `TestAttempt_CtxCancelMidPing` and `TestRun_ExitsOnCtxCancel`.

### Checkpoint 3 — semaphore correctness under race

`TestTickOnce_RespectsConcurrencySemaphore` uses a `barrierClient.onPing` that increments a counter and blocks on `release`. Test seeds 10 rows, concurrency=3. Asserts peak active = 3 (never 4+).

Pre-this-audit: passed under `-race -count=2`. Re-run after F2 fix: still passes.

### Checkpoint 4 — Run + ticker.C edge cases

`time.NewTicker(t.interval)` fires immediately at construction... no wait, that's wrong. `time.NewTicker` does NOT fire immediately; the first send on `C` is after `interval`. The first `tickOnce(ctx)` call before the `for` loop is what gives us "first tick immediately after jitter."

If `tickOnce` takes longer than `interval`, the ticker buffer cap=1 absorbs one missed tick. We never get two ticks queued. ✓

If we want overlap detection: not in chunk 7. The summary log surfaces tick duration; operators can compare against interval.

### Checkpoint 5 — F6: partial cookies + sentinel error

Reading `web.Get`:

```go
// In checkpoint case:
return finalURL, body, refresh, sessErr  // ← BOTH non-nil
```

Our `classifyRefreshErr` checks err first → routes to actionSuspend. The refresh signal with partial cookies is **discarded**. Is this correct?

YES. Reasoning:
- Checkpoint/challenge/consent indicate Meta requires user interaction. The redirect-chain Set-Cookies in these flows include cookies that are valid ONLY in the checkpoint context (e.g., a one-shot CSRF nonce for the challenge form). Persisting them as the "current session cookies" would corrupt the next refresh.
- Burning/suspending releases the claim. Operator must re-bridge to get a fresh, post-challenge session.

Verified intentional. ✓

### Checkpoint 6 — F1: persist races against next attemptRefresh

Single attemptRefresh per row per tick (list query returns each uid once). Two ticks back-to-back COULD both target the same row if the first persist hadn't committed before the second list query — but `tickOnce` is serial via the `for {<-ticker.C}` loop. Two ticks never overlap.

Concurrent BurnSession via gRPC handler + refresh attempt on the same uid: both call `store.BurnSession`. Single-statement UPDATE; last write wins. Lifecycle event fires twice. Acceptable (consumers dedupe by uid+timestamp).

### Checkpoint 7 — F8: errClient fallthrough on bad cookies

`defaultClientFactory` wraps `web.New(cookies, opts)`. `web.New` returns `ErrMissingRequired` if cookies don't have c_user/xs/datr (legacy or corrupted row). We fall through to `errClient{err: err}` whose `Ping` always returns the construction error.

`classifyRefreshErr` sees a non-sentinel error → actionTransientError. The session stays active; metric increments. Operator alerts on transient_errors gauge spike.

Why not burn? A row with missing cookies is recoverable by re-bridging. Burning would be premature.

Pinned indirectly by `TestAttempt_NoCookies_LegacyRow` (the EncryptedCookies=nil case takes the same fall-through to transient).

### Checkpoint 8 — F9: tickOnce overrun

`time.Ticker` buffer cap=1. If tickOnce takes 1.5×Interval, we get:
- t=0: first tick fires
- t=Interval: ticker tries to send, buffer was empty → succeeds (cap=1)
- t=1.5Interval: tickOnce finishes; loop reads from C → runs SECOND tick immediately
- t=2.5Interval: ticker tries to send, buffer empty again → succeeds

Net: no missed ticks beyond at most 1 per overrun event, AND no queue-runaway under sustained slowness. ✓

If we wanted dropped-tick detection: compare `t.nowFn().Sub(start)` against `t.interval` and warn-log. **Deferred** — not chunk-7 scope.

### Checkpoint 9 — main.go drain ordering

```
signal -> SetReady(false) -> gRPC health NOT_SERVING ->
  nc.Drain() + wait for CLOSED bounded by drainCtx ->
  WAIT refresh ticker exit (bounded by drainCtx) ->   ← chunk-7 added
  mgr.Drain -> grpc.GracefulStop -> mgr.Shutdown ->
  diag teardown
```

Why wait for refresh BEFORE mgr.Drain? An in-flight refresh attempt calls into `store` and `publisher`. mgr.Drain doesn't affect these — refresh doesn't go through Manager. But we want clean exit semantics: when the drain phase declares "manager draining," no refresh attempt should still be running.

In practice rootCtx is already canceled by the time we enter the drain phase, so the ticker's `Run` loop is already exiting. The select bounded by drainCtx is belt-and-suspenders — caps blast radius if Ping has a stuck retry loop. ✓

### Checkpoint 10 — Metrics nil-safety

Every metric method has `if m == nil { return }`. The Ticker constructor accepts `Metrics: nil` (chunk-7's tests pass nil). Tests verify the no-op path doesn't panic.

`prometheus.MustRegister` is called in `NewMetrics`, NOT in `New`. If a test wires a Metrics value but registers against `nil` Registerer, `NewMetrics` returns nil early. ✓

## Test surface after audit

```
✓ go vet ./internal/mbs/refresh ./cmd/mbs                  clean
✓ go test -race -count=5 -timeout 180s ./internal/mbs/refresh
                                                            1.7s, 27 tests
✓ go test -race -count=2 -timeout 180s ./cmd/mbs/... ./internal/mbs/... ./pkg/...
                                                            ~30s, 347+ tests
✓ make build                                                bin/hermes-mbs (51MB)
```

27 tests broken down:

| File | Tests | Focus |
|---|---|---|
| `classify_test.go` | 8 | Sentinel mapping, ctx override, generic-err transient |
| `attempt_test.go` | 13 | All 5 Stage-D sentinels via attempt, decrypt/envelope failures, no-cookies legacy, ctx cancel, F2 latency pin |
| `ticker_test.go` | 6 | New() validation, defaults, jitter bounds + determinism, perAttemptTimeout bounds, tickOnce fan-out, Run lifecycle, semaphore cap, summarize |

## Verdict

✅ All chunk-7 success criteria met. P1 (F2) caught + fixed + pinned. Remaining P2 findings documented as acceptable behavior. Ready for chunk-7 merge to main.
