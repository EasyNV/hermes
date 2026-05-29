# Stage E1 — Chunk 6 — Step 8 — Hostile-Eyes Audit

**Date:** 2026-05-29
**Auditor:** Oracle (red team against own `cmd/mbs/` wiring)
**Scope:** `cmd/mbs/` (main.go, nats_streams.go, send_consumers.go, reconnect.go) — 5 source files + 4 test files, ~850 LOC of impl + ~580 LOC of tests
**Method:** Walk every code path that touches: signal handling, ctx propagation, goroutine spawn, shared mutable state, NATS lifecycle, gRPC drain, defer-stack ordering, and the boundary between sync setup and async background work.

## Verdict

**1 P1 found and fixed during audit. 2 P2 documented for future hardening. R2 + R3 (the two gaps pre-identified in the plan) were applied at code-write time. Everything else clean.**

The `cmd/mbs` surface is tight. The plan baked in the chunk-5 lessons (process-wide mautrix mutation, sync vs async startup ordering) so the audit found a single new P1 instead of the usual scatter.

## Findings

| # | Sev | Title | File:line | Status |
|---|---|---|---|---|
| F1 | **P1** | `nc.Drain()` is asynchronous — NATS consumer SendMessage calls could outlive `mgr.Drain` / `mgr.Shutdown` | `main.go:252-269` | **FIXED** in this audit pass |
| F2 | P2 | `reconnectPodSessions` goroutine spawned on `rootCtx` — race with `mgr.Shutdown` if SIGTERM lands ≤30s after boot | `main.go:203` + `reconnect.go` | Documented — bounded by `reconnectPerUIDLimit=30s` and `ErrShutdown` sentinel inside Manager |
| F3 | P2 | `defer pool.Close()` runs after explicit shutdown sequence completes — relies on defer-stack ordering matching mental model | `main.go:88` | Verified safe — defers pop LIFO so pool.Close fires AFTER all the explicit `mgr.Drain`/`mgr.Shutdown` calls. Documented. |

Pre-identified gaps from the chunk-6 plan (R2 = diag bind sync, R3 = NATS drain ordering) were applied at code-write time, NOT discovered in this pass:

- **R2 (FIXED at write):** Diagnostic HTTP listener is bound synchronously via `net.Listen` before being served on a goroutine. Port collision surfaces immediately at boot with a clear log.Fatal, not silently 6 hours later at shutdown.
- **R3 (FIXED at write):** Shutdown order is documented + enforced: SetReady(false) → gRPC health NOT_SERVING → nc.Drain → mgr.Drain → grpcSrv.GracefulStop (with timeout fallback to Stop) → mgr.Shutdown → diag stop. NATS draining happens BEFORE manager teardown so consumer SendMessage calls don't race shutdown.

## Audit walkthrough by checkpoint

### Checkpoint 1 — Signal handling: double-SIGTERM doesn't crash

`signal.NotifyContext(context.Background(), SIGINT, SIGTERM)` cancels rootCtx on first signal. A second SIGTERM hits the parent context which is already canceled — Go's NotifyContext absorbs additional signals (it doesn't reset the channel; the registered handler stays bound until cancel is called).

The drain phase uses `drainCtx`, a fresh `context.WithTimeout(context.Background(), ShutdownDrainTimeout)`. It's independent of rootCtx so the second SIGTERM doesn't shorten the drain window. K8s SIGKILL (default grace period 30s) is outside our control — operators tune the grace period to match `MBS_SHUTDOWN_DRAIN_TIMEOUT`.

**Verified.** ✓

### Checkpoint 2 — DB unavailable at boot → fail closed

`db.NewPoolWithOpts` calls `pool.Ping(ctx)` internally before returning. A dead Postgres returns a wrapped pgx error → `log.Fatal()` → exit 1. Smoke-test confirms with port-9999 fake:

```
{"level":"info","message":"DEK loaded"}
{"level":"fatal","error":"pinging database: failed to connect to ...
  dial tcp 127.0.0.1:9999: connect: connection refused",
 "message":"postgres connect failed"}
```

Half-open pgxpool would be a leak — verified by reading pool_opts.go: `NewPoolWithOpts` calls `pool.Close()` if Ping fails before returning. ✓

### Checkpoint 3 — NATS unavailable at boot → fail closed

`hermesnats.NewJetStream` returns err on dial failure → `log.Fatal`. No NATS object constructed; nothing to leak. ✓

### Checkpoint 4 — DEK missing → fail closed with both env var names in error

```
{"level":"fatal","error":"no DEK source configured: set HERMES_MBS_DEK_FILE or HERMES_MBS_DEK_HEX"}
```

Smoke verified. The error mentions BOTH knobs so the operator knows the configuration surface without grepping source. Pinned by `TestLoadDEK_FailsClosedWhenBothMissing` (asserts both strings appear in err.Error()). ✓

### Checkpoint 5 — DEK file bad → wrapped error with path

`crypto.LoadDEKFromFile` returns wrapped ENOENT/invalid-hex errors. `loadDEK` further wraps with the file path so triage doesn't need to read code. Pinned by `TestLoadDEK_FileMissingReturnsWrappedError`. ✓

### Checkpoint 6 — Diag server port collision

**R2 from plan.** Pre-fix code did `diagSrv.Start(ctx)` on a goroutine, hiding bind errors until the channel was read at shutdown. Fix-at-write: `net.Listen("tcp", ":9092")` runs synchronously BEFORE the goroutine spawn:

```go
diagListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.MetricsPort))
if err != nil {
    log.Fatal().Err(err)...
}
// THEN: go serveDiag(ctx, srv, diagListener)
```

Port-collision: `log.Fatal` → operator immediately knows. ✓

### Checkpoint 7 — F1 (P1): nc.Drain() is asynchronous

**Audit discovery — fixed in this pass.**

Reading `/Users/env/go/pkg/mod/github.com/nats-io/nats.go@v1.50.0/nats.go`:

```go
func (nc *Conn) Drain() error {
    ...
    nc.changeConnStatus(DRAINING_SUBS)
    go nc.drainConnection()  // ← async
    nc.mu.Unlock()
    return nil  // ← returns BEFORE drain completes
}
```

Pre-fix shutdown sequence called `nc.Drain()` then immediately `mgr.Drain(drainCtx)` then `grpcSrv.GracefulStop`. The NATS consumer goroutine could still be mid-SendMessage when mgr.Drain flips the flag — SendMessage's call into `mgr.GetOrConnect` would observe `ErrDrained` and fail the send. Worse: if the consumer was mid-flight on a successful SendMessage, the mgr.Shutdown could disconnect the underlying client while the send was in progress.

**Fix:** Synchronously wait for `nc.StatusChanged(natsgo.CLOSED)` after calling Drain, bounded by drainCtx:

```go
if err := nc.Drain(); err != nil {
    log.Warn()...
} else {
    statusCh := nc.StatusChanged(natsgo.CLOSED)
    select {
    case <-statusCh:
        log.Info().Msg("NATS connection drained")
    case <-drainCtx.Done():
        log.Warn().Msg("NATS drain wait exceeded drainCtx; continuing shutdown")
    }
}
```

Now NATS consumer SendMessage calls finish (or hit drainCtx timeout) before the manager starts tearing down. The outer `drainCtx` bounds total drain time at `ShutdownDrainTimeout`. ✓ FIXED.

### Checkpoint 8 — F2 (P2): reconnectPodSessions race with mgr.Shutdown

`reconnectPodSessions` spawns up to 10 concurrent GetOrConnect goroutines with per-uid 30s timeout. If SIGTERM lands ≤30s after boot, these are still in-flight when the drain phase begins.

Defense in depth (existing):

1. Reconnect goroutines watch `ctx` — `rootCtx` cancels on SIGTERM → goroutines see ctx.Err() and abort their GetOrConnect.
2. Manager's `GetOrConnect` checks `m.shutdown.Load()` and `m.drained.Load()` → returns ErrShutdown/ErrDrained.
3. Per-uid 30s timeout on the reconnect call → worst case the reconnect goroutine holds `ms.mu` for 30s.

The Disconnect call from `mgr.Shutdown` will then wait for that mutex. If the drainCtx (30s) expires while mgr.Shutdown is iterating, Shutdown returns ctx.Err() and the remaining mutexes are dropped naturally as the process exits. Operator sees a "drain timeout exceeded" warning.

**Verdict:** acceptable — bounded blast radius via the existing timeout + ErrShutdown sentinels. Documented for chunk-7 (refresh ticker) which has the same shape. ✓

### Checkpoint 9 — F3 (P2): defer pool.Close ordering

The main function registers `defer pool.Close()` early (line ~88). Defers pop LIFO at function return. By the time `main()` returns:

1. Explicit shutdown sequence already ran: nc.Drain → mgr.Drain → grpcSrv.GracefulStop → mgr.Shutdown → diag stop
2. Then defers fire in reverse registration order: drainCancel → diagDoneCancel → mgr (none — no defer) → nc.Close → pool.Close → rootCancel

`pool.Close()` runs LAST among the resource closes, which is correct — Manager.Shutdown disconnects every client and releases every claim BEFORE the pool closes underneath it. ✓

**Verified safe.** The pattern matches `cmd/wa/main.go`. ✓

### Checkpoint 10 — MautrixDisableTLS warning visibility

When `HERMES_MBS_DISABLE_TLS=true`:

```json
{"level":"warn","process_wide":true,"unrecoverable_until_restart":true,
 "message":"HERMES_MBS_DISABLE_TLS=true — mautrix-meta TLS verification disabled
            process-wide. Do NOT run multi-tenant in this mode. Audit ref: chunk-5 F1."}
```

Fields `process_wide=true` and `unrecoverable_until_restart=true` are structured (zerolog Bool) so they're greppable in log aggregators. The chunk-5 F1 reference points the operator to the prior audit doc. ✓

### Checkpoint 11 — Send consumer poison handling

`makeSendHandler` handles three poison cases:

1. **Bad subject** (no `hermes.mbs.send.<kind>.<tenant>` shape) → log error + `msg.Ack()` (drop, don't loop)
2. **Bad proto** (proto.Unmarshal fails) → log error + Ack
3. **Bad task** (no uid / no body / no recipient) → log error + Ack

Real failures (network, downstream SendMessage error) → `msg.Nak()` for redelivery up to `MaxDeliver=5`. ✓

Pinned by 11 tests in send_consumers_test.go covering tenantFromSubject (7 err cases including dots-in-tenant, empty tokens, missing prefix) and buildSendRequestFromTask (3 err cases).

### Checkpoint 12 — gRPC drain timeout fallback

`grpcSrv.GracefulStop` blocks indefinitely if a `Listen` stream stays open (stream contracts don't get GoAway notifications the way unary RPCs do). Fix-at-write: select on `stopDone` channel vs `drainCtx.Done()`, and call `grpcSrv.Stop()` (force-close) on timeout:

```go
select {
case <-stopDone:
    log.Info().Msg("gRPC GracefulStop complete")
case <-drainCtx.Done():
    log.Warn().Msg("drain timeout exceeded; force-stopping gRPC")
    grpcSrv.Stop()
}
```

`grpcSrv.Stop()` closes the listener and breaks all in-flight RPCs — clients see context.Canceled. K8s SIGTERM grace period bounds total time. ✓

## Test surface after audit

```
✓ go vet ./cmd/mbs ./internal/mbs/... ./pkg/...        clean (1 pre-existing chunk-3 noise)
✓ go test -race -count=5 -timeout 180s ./cmd/mbs/...   1.4s, 22 tests
✓ go test -race -count=5 -timeout 180s ./internal/mbs  ~37s, 320+ tests
✓ make build                                            produces bin/hermes-mbs (51MB)
✓ ./bin/hermes-mbs (no DEK)                             exit 1 with descriptive fatal
✓ ./bin/hermes-mbs (DEK + bad DB)                       exit 1 after "DEK loaded"
```

22 tests in cmd/mbs broken down:

| File | Tests | Focus |
|---|---|---|
| `config_test.go` | 2 (+sub-tests) | Defaults, env-bool parsing |
| `nats_streams_test.go` | 4 | Stream config shape, replica normalization, error propagation |
| `send_consumers_test.go` | 11 | Subject parsing (7 err cases), task→request projection (3 err cases), consumer wiring |
| `main_test.go` | 5 | DEK loader: file preference, fallback, fail-closed, file-missing, bad hex |

## What's intentionally NOT covered by tests

- Full process boot-to-shutdown end-to-end. Requires real DB + NATS — deferred to compose integration testing in Stage F.
- gRPC server graceful-stop timeout fallback. Hard to test deterministically without a real stream client; visual inspection + chunk-5 audit pattern matches.
- NATS reconnection during partial outage. nats.go handles this; our drain path only runs at clean shutdown.
- mautrix-meta package-init log line. Upstream noise; document, don't test.

## Verdict

✅ All chunk-6 success criteria met:

- `make build-mbs` (via the main `build` target) produces `bin/hermes-mbs`
- `go test -race -count=5 ./cmd/mbs/...` passes
- `go test -race -count=5 ./internal/mbs/...` still passes (no chunk-5 regressions)
- `go vet ./cmd/mbs ./internal/mbs/...` clean
- Binary boots, fail-closed on missing DEK, fail-closed on bad DB
- SIGTERM drain ordering correct (R2, R3, F1 all addressed)
- Hostile-eyes audit complete with single P1 finding fixed before merge

Ready for chunk 6 merge to main.
