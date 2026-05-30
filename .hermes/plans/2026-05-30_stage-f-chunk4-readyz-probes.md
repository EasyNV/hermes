# Stage F Chunk 4 — Real `/readyz` across all services

**Owner:** Oracle
**Created:** 2026-05-30
**Status:** Plan + contracts written, awaiting build
**Parent:** `.hermes/plans/2026-05-29_stage-f-deploy-hardening-master.md`
**Predecessor:** chunk 3 (`docker-compose.prod.yml` + secret externalisation) — landed at `0e7fb37`

---

## 1. Goal

Replace every backend service's compose `healthcheck` from `nc -z localhost
<port>` ("TCP listener bound — service may still be useless") to a real
`/readyz` HTTP probe served by a dedicated metrics/diag port that returns
200 only when the service has actually completed its boot dance:

- HTTP server bound
- DB pool reachable (`Ping`)
- NATS connected (`nats.Conn.IsConnected()`)
- gRPC server listening
- Service-specific steady-state predicate (e.g. consumers subscribed
  for inbox, WebSocket hub started for gateway)

And add `/livez` — always 200 once the HTTP server is up — so compose
can distinguish "process alive" (livez) from "service ready"
(readyz). Long-term this aligns with the K8s probe contract.

This chunk also lays the foundation chunk 5's fresh-VM smoke test
needs: a single, uniform `wget --spider http://<svc>:<port>/readyz`
healthcheck that works across every container.

---

## 2. Constraints inherited

- mbs already ships this (`internal/mbs/observability/http.go`).
  We **clone the pattern**, not extract a shared package — chunk-4
  scope is each service's own probe, factored when more than one
  service starts depending on the shared abstraction.
  *(See §10 for the "extract to pkg/observability" follow-up.)*
- Compose-first (no K8s manifests).
- Single `wget --spider` healthcheck shape so the busybox `wget` in
  the chunk-2 Alpine runtime keeps working.
- Probe ordering on graceful shutdown: `/readyz` MUST flip to 503
  **before** the gRPC server starts refusing connections. The mbs
  pattern (`SetReady(false)` → `grpcSrv.GracefulStop`) is the
  reference; every service follows it.
- Wire profile preserved. No `replace` directives touched.
- All probes bind to `0.0.0.0:METRICS_PORT` (not `localhost`) to
  avoid the chunk-3 F5 IPv6 trap (web container) recurring inside
  Go services — Go's `net.Listen("tcp", ":<port>")` already binds
  dual-stack, but documenting for safety.

---

## 3. Contracts

### 3.1 Port allocations

Per-service `METRICS_PORT` env var, in the 9100-9107 band already
implied by AGENTS.md (and chunk-3 master plan §4). Defaults match
service ports +0x1000 where the gRPC port lives in 9100-9106 (so
the diag port lives one band above the service port to avoid any
risk of collision):

| Service | gRPC port | METRICS_PORT (default) | env |
|---|--:|--:|---|
| gateway | 8080 (gRPC) + 8081 (REST/WS) | **9100** | `METRICS_PORT` |
| proxy | 9101 | **9111** | `METRICS_PORT` |
| contacts | 9102 | **9112** | `METRICS_PORT` |
| notify | 9103 | **9113** | `METRICS_PORT` |
| wa | 9104 | **9114** | `METRICS_PORT` |
| campaign | 9105 | **9115** | `METRICS_PORT` |
| inbox | 9106 | **9116** | `METRICS_PORT` |
| mbs | 8082 | **9092** (unchanged) | `METRICS_PORT` |

Why not 9101-9107? Because those collide with existing gRPC
service ports (`wa` 9104, etc.). Shifting the diag port to
service-port + 10 keeps the mental model "gRPC port + 10 = diag
port" simple, and gives 9100 to gateway specifically (a memorable
top-of-band slot).

`mbs` keeps `9092` for backward compatibility with the chunk-1
healthcheck — no churn in the running stack.

### 3.2 HTTP probe surface (per service)

Every diag server exposes exactly these routes:

| Path | Status | Body | Semantic |
|---|---|---|---|
| `GET /livez` | 200 always | `ok\n` | process is alive and HTTP server up |
| `GET /readyz` | 200 ready / 503 not | `ready\n` or `not ready: <reason>\n` | service is ready to serve traffic |
| `GET /metrics` | 200 | Prometheus exposition | metrics scrape — used by chunk-5 follow-up Prometheus wiring |

`/readyz` returns 503 when **any** of:

1. Ready flag is false (boot incomplete or shutting down)
2. `ReadinessFn(ctx)` returns non-nil (DB ping fails, NATS dropped,
   service-specific subcheck fails)

Probe wait timeout per call: **2 seconds** (matching the mbs
constant `readinessTimeout = 2 * time.Second`). Compose healthcheck
`timeout: 5s` gives 3s headroom for the wget round-trip.

### 3.3 Per-service `ReadinessFunc`

Each service's readiness function probes the dependencies it
actually needs. Reference table:

| Service | ReadinessFn checks |
|---|---|
| gateway | DB `Ping` + NATS `IsConnected()` |
| wa | DB `Ping` + NATS `IsConnected()` + Redis `Ping` |
| campaign | DB `Ping` + NATS `IsConnected()` |
| inbox | DB `Ping` + NATS `IsConnected()` |
| contacts | DB `Ping` + NATS `IsConnected()` |
| proxy | DB `Ping` + NATS `IsConnected()` |
| notify | DB `Ping` + NATS `IsConnected()` |
| mbs | DB `Ping` (unchanged) |

NATS `IsConnected` is a synchronous flag check (no IO), so the
2-second deadline is plenty. DB `Ping` uses the existing pgxpool;
each service's `pkg/db` connection is reused.

Service-specific subchecks (e.g. "WebSocket hub started" for
gateway, "consumers subscribed" for inbox) are **NOT** wired in
chunk 4 — they require per-service atomic flags I'm not introducing
in this chunk. Documented as chunk-4 carry-forward.

### 3.4 Boot ordering contract

Each service's `main.go` follows this sequence:

```
1.  cfg := config.Load()
2.  log := logger.New(cfg)
3.  pool := db.Connect(cfg)                    // fatal-if-fail
4.  nc, js := nats.Connect(cfg)                // fatal-if-fail
5.  diagListener := net.Listen(":METRICS_PORT")  // fatal-if-fail
6.  diagSrv := observability.NewHTTPServer(...)  // ready = false
7.  go diagSrv.Serve(diagListener)             // /livez = 200, /readyz = 503
8.  ensureStreams(js)                          // any NATS streams
9.  startConsumers(...)                        // service-specific
10. lis := net.Listen(":PORT")                 // gRPC
11. diagSrv.SetReady(true)                     // /readyz = 200
12. grpcSrv.Serve(lis)
```

Shutdown sequence (reverse, with explicit ordering for probe race
prevention — chunk-3 master plan R4):

```
signal → diagSrv.SetReady(false)               // /readyz starts 503
      → flip gRPC health to NOT_SERVING
      → consumers Drain (finish in-flight)
      → grpcSrv.GracefulStop (or .Stop on drain timeout)
      → close pgxpool, NATS
      → diagSrv shutdown LAST                   // /livez stays 200 till end
```

The 503-before-NOT_SERVING ordering matters because a load balancer
or compose `depends_on: service_healthy` graph reads `/readyz`
faster than gRPC connections see RST. Mbs already does this; every
service in this chunk adopts the same shape.

### 3.5 `pkg/observability/http.go` — extracted package

Chunk 4 **does** extract the observability HTTP-server shape into
`pkg/observability/` because 7 services need it and copy-paste is
worse than a 153-LOC shared package. The mbs-specific concerns
(`/debug/pprof` toggle) remain optional via the `Options.EnablePprof`
field that already exists in `internal/mbs/observability`.

`internal/mbs/observability/http.go` keeps existing call sites in
mbs but **the body is replaced with a thin wrapper around
`pkg/observability`** so we don't double-maintain the readiness
logic. This is a refactor + extract in one chunk; tests in both
packages stay green.

Alternative considered (and rejected): leave mbs alone, copy-paste
into each of the 7 other services. Rejected because future
patches (a metrics-only path, a pprof toggle, etc.) would have to
land 8 times.

### 3.6 New compose healthcheck shape

```yaml
healthcheck:
  test: ["CMD-SHELL", "wget --spider -q http://127.0.0.1:${METRICS_PORT}/readyz"]
  interval: 10s
  timeout: 5s
  retries: 5
  start_period: 20s     # generous; gives DB+NATS+consumers time
```

Why `127.0.0.1` not `localhost`? Chunk-3 F5: busybox `wget` in
Alpine resolves `localhost` → `::1` first, but Go's `net.Listen`
binds dual-stack by default — `localhost` would *work* for the Go
services, but using `127.0.0.1` is invariant across all containers
(including the web image where nginx is IPv4-only) and removes
mental overhead.

The `${METRICS_PORT}` literal expands at compose-parse time. Per
service the value matches §3.1.

### 3.7 `depends_on` upgrade

Chunk 3 currently uses `condition: service_started` for the gateway
→ backend dependency edges (because backend `nc -z` healthchecks
flip green too aggressively). Chunk 4 upgrades these to `condition:
service_healthy` since `/readyz` is now a real predicate:

```yaml
gateway:
  depends_on:
    proxy:    { condition: service_healthy }
    contacts: { condition: service_healthy }
    notify:   { condition: service_healthy }
    wa:       { condition: service_healthy }
    campaign: { condition: service_healthy }
    inbox:    { condition: service_healthy }
    mbs:      { condition: service_healthy }
```

This makes a slow-booting backend (e.g. wa waiting on Redis +
proxy + NATS streams) actually delay gateway boot instead of
gateway connecting too early and getting `connection refused`.

---

## 4. Implementation steps

1. **Create `pkg/observability/http.go`** by copying the body of
   `internal/mbs/observability/http.go` (minimal/zero changes —
   only the package declaration and any mbs-specific comment
   adjustments).
2. **Create `pkg/observability/http_test.go`** by copying the body
   of `internal/mbs/observability/http_test.go`. Tests pass
   unchanged.
3. **Refactor `internal/mbs/observability/http.go`** to re-export
   `pkg/observability` types so `cmd/mbs/main.go` keeps compiling
   without any change to its call site. Either:
   - Add `type HTTPServer = observability.HTTPServer` re-exports
     (Go 1.9+ type aliases), or
   - Update `cmd/mbs/main.go` to import `pkg/observability`
     directly and delete `internal/mbs/observability/`.
   **Decision:** delete `internal/mbs/observability/` and update
   cmd/mbs/main.go imports. The package's only consumer is
   cmd/mbs/main.go, and re-export aliases obscure the architecture.
4. **For each of the 7 backend services** (`gateway`, `wa`,
   `campaign`, `inbox`, `contacts`, `proxy`, `notify`):
   a. Add `MetricsPort int` field to `internal/<svc>/config/config.go`
      with `pkgconfig.GetEnvInt("METRICS_PORT", <default per §3.1>)`.
   b. In `cmd/<svc>/main.go`:
      - Pre-bind `diagListener` after DB+NATS connect.
      - Construct `diagSrv` with the service-appropriate
        `ReadinessFn` (DB.Ping + NATS.IsConnected, plus Redis for
        wa).
      - Goroutine `diagSrv.Serve(diagListener)`.
      - Call `diagSrv.SetReady(true)` after gRPC listener bound.
      - Hook signal handler: `SetReady(false)` → gRPC graceful
        stop → diagSrv shutdown last.
   c. If the service has no existing graceful-shutdown signal
      handler, add one (the mbs pattern is the reference). Several
      services do `grpcSrv.Serve(lis)` and rely on container kill —
      chunk 4 elevates them to graceful-shutdown-with-probe-flip
      hygiene.
5. **Update `docker-compose.dev.yml` and `docker-compose.prod.yml`**:
   - Add `METRICS_PORT` env to each service block (per §3.1).
   - Expose the metrics port (host-bind) in dev only.
   - Replace each `healthcheck.test` with the §3.6 shape.
   - In prod, upgrade gateway `depends_on` to `service_healthy`
     for the backend services (§3.7).
6. **Update `pkg/db`** if no `Ping` is exported (verify; spec
   §3.3 assumes it exists).
7. **Update or add tests** in each `cmd/<svc>` for the diag boot
   ordering — at minimum, a test that asserts SetReady(false) is
   called before grpcSrv.GracefulStop in the shutdown path.
8. **Run smoke test**:
   - `make docker-build-all` (rebuild every Go service image).
   - `make deploy-prod-up` (boot the prod stack).
   - For each service: `curl -fsS
     http://127.0.0.1:<port>/readyz` from inside the network →
     200.
   - Trigger an intentional NATS disconnect (kill NATS container)
     → every backend's `/readyz` flips to 503 within
     `interval+timeout` of the probe (~15s).
9. **Write hostile audit**.
10. **Commit.**

---

## 5. Files inventory (anticipated diff shape)

```
NEW:
  pkg/observability/http.go                                          ~155 LOC
  pkg/observability/http_test.go                                     ~150 LOC
  .hermes/plans/2026-05-30_stage-f-chunk4-readyz-probes.md           [this file]
  docs/research/mbs-f-chunk4-hostile-audit-2026-05-30.md             [post-build]

MODIFIED:
  internal/mbs/observability/http.go                                 → DELETED
  internal/mbs/observability/http_test.go                            → DELETED (covered by pkg/)
  internal/mbs/observability/metrics.go                              ~ minor (path adjust if needed)
  cmd/mbs/main.go                                                    ~ import path: internal→pkg
  internal/gateway/config/config.go                                  +1 field (MetricsPort)
  internal/wa/config/config.go                                       +1 field
  internal/campaign/config/config.go                                 +1 field
  internal/inbox/config/config.go                                    +1 field
  internal/contacts/config/config.go                                 +1 field
  internal/proxy/config/config.go                                    +1 field
  internal/notify/config/config.go                                   +1 field
  cmd/gateway/main.go                                                ~+40 LOC (diag server + shutdown)
  cmd/wa/main.go                                                     ~+40 LOC
  cmd/campaign/main.go                                               ~+40 LOC
  cmd/inbox/main.go                                                  ~+40 LOC
  cmd/contacts/main.go                                               ~+40 LOC
  cmd/proxy/main.go                                                  ~+40 LOC
  cmd/notify/main.go                                                 ~+40 LOC (already has ensureStream)
  docker-compose.dev.yml                                             ~+30 LOC (env + healthchecks + port maps)
  docker-compose.prod.yml                                            ~+25 LOC (env + healthchecks + depends_on healthy)
```

Total new code: ~300 LOC test + ~280 LOC main + 155 LOC pkg = ~735
LOC, with 153 LOC deleted from `internal/mbs/observability`. Net
add ≈ 580 LOC. Plan estimate was "4-6 hours" — realistic.

---

## 6. Acceptance gates

| # | Gate | Command | Pass |
|---|---|---|---|
| 1 | All services build clean | `make docker-build-all` | exit 0 |
| 2 | Prod compose boots | `make deploy-prod-up` | 12/12 healthy |
| 3 | Every service's `/livez` returns 200 | `for p in 9100 9111 9112 9113 9114 9115 9116 9092; do docker compose exec <svc> wget -qO- http://127.0.0.1:$p/livez; done` | `ok` × 8 |
| 4 | Every service's `/readyz` returns 200 once healthy | same loop with `/readyz` | `ready` × 8 |
| 5 | Kill NATS → all `/readyz` flip to 503 | `docker kill hermes-nats-1; sleep 5; curl /readyz` per svc | 503 within ~15s |
| 6 | Restart NATS → `/readyz` recovers to 200 | bring NATS back, wait ~15s | 200 across the board |
| 7 | `depends_on: service_healthy` works | `docker compose stop nats; docker compose up gateway` | gateway waits, doesn't fatal |
| 8 | Probe ordering on shutdown | `docker kill --signal=SIGTERM <svc>; observe /readyz response during gRPC drain window` | 503 fires BEFORE NOT_SERVING (logged) |
| 9 | go test still green | `go test -count=1 ./...` | ok |
| 10 | Dev compose still parses + boots | `docker-compose -f docker-compose.dev.yml config && make infra && docker compose -f docker-compose.dev.yml up -d` | exit 0, healthy |
| 11 | Chunk-3 prod compose unaffected | `docker-compose -f docker-compose.prod.yml config` | exit 0 |
| 12 | OrbStack uid/gid warning unchanged | warn message still present, secret behaviour unchanged | cosmetic |

---

## 7. Hostile-audit categories

- **Probe-flip race:** Does `/readyz` actually flip to 503 *before*
  gRPC starts refusing new conns? Test: open a long-lived gRPC
  call, send SIGTERM, observe timeline. mbs already does this; the
  pattern carries over.
- **Slow probe DoS:** A malicious `/readyz` caller spamming
  requests can't tie up gRPC because the diag server is on a
  separate port and goroutine. Verify with `hey -c 100 -z 30s
  http://.../readyz` while gRPC is under load.
- **Probe deadline tuning:** is 2s enough for DB.Ping under load?
  Document; consider 5s for prod if DB sees spike-load.
- **NATS reconnect window:** when NATS bounces, `nc.IsConnected()`
  goes false → `/readyz` goes 503. Verify the nats.go client
  reconnects without restart-of-svc needed (it does by default
  via the reconnect handler, but our code must not be holding the
  old conn).
- **Goroutine leak on shutdown:** Does `diagSrv.Shutdown` always
  return? Bounded by `shutdownTimeout = 5s` in the package.
- **Port collision in compose:** With the new METRICS_PORT
  defaults, do any host-port exposures collide on the typical
  dev host (5173, 8080, 8081, 5433, 6380, 4222, 8222, 5432)? No
  — 9100-9116 is a clean band.
- **Compose dev hot-reload:** does adding a goroutine for
  diagSrv.Serve break the Dockerfile.dev hot-reload behaviour?
  No — Dockerfile.dev does `go run` on the binary, which is the
  same shape.
- **Test isolation:** `pkg/observability/http_test.go` binds to
  ports — must use `:0` ephemeral ports or `httptest.NewServer`.
  Reference: existing `internal/mbs/observability/http_test.go`
  uses ephemeral.
- **`prometheus.DefaultGatherer` collision:** every service
  registers its own metrics on the default gatherer. Since each
  service runs in its own process, no cross-service collision.
  Verify in single-process tests by using a per-test gatherer.
- **gRPC health-check parity:** chunk 4 leaves `grpc_health_v1`
  alone — it's a separate, gRPC-native protocol. Compose
  healthcheck reads HTTP `/readyz`; gateway-to-backend gRPC
  client uses gRPC health. Both must be consistent
  (gateway's gRPC client sees NOT_SERVING when backend
  shuts down; compose sees 503 from /readyz). Mbs already
  does the dual flip; copy that pattern.

---

## 8. Risks

- **R1 — Shared package extraction breaks mbs:** moving
  `internal/mbs/observability` to `pkg/observability` and deleting
  the old path could miss an import. Mitigation: `go build
  ./...` after the move; CI/test suite catches any miss.
- **R2 — Service `main.go` files diverge in shape:** 7 services
  have 7 different boot dances. Forcing them all into the mbs
  shape is a refactor. Mitigation: do one service at a time, run
  tests after each, commit in a single chunk-4 commit only after
  all 7 work.
- **R3 — `depends_on: service_healthy` lock-up:** if any backend
  service can't reach DB or NATS at boot, gateway will hang
  waiting. Compose default deadline is "wait indefinitely." The
  prod compose already accepts this risk (chunk 3 R4). Document
  the symptom in `compose-deploy.md`.
- **R4 — Metrics port now exposed on host in dev:** new attack
  surface. Mitigate: don't expose in prod (only `127.0.0.1` bind
  via `${HERMES_METRICS_BIND_HOST:-127.0.0.1}` pattern).
  Documented in §3.6.
- **R5 — NATS reconnect false-negatives:** the nats.go client may
  briefly report IsConnected=false during a reconnect window even
  if the connection is healthy. This could cause flappy `/readyz`.
  Mitigation: chunk 4 logs the transition (every flip to 503 is
  noted with reason); chunk 5 follow-up could add a "ready
  hysteresis" buffer.

---

## 9. Out of scope (deferred)

- **Service-specific subchecks** (WebSocket hub started, MBS
  bridge driver pool healthy, etc.) — atomic flags TBD. Carry
  forward to a chunk 4.1 if/when needed.
- **Prometheus scrape config** — the `/metrics` endpoint is
  exposed, but no Prometheus container in compose to scrape it.
  Stage G or follow-up.
- **gRPC health-checking protocol consistency** — gateway-to-mbs
  already does this (chunk E1); chunk 4 doesn't expand it.
- **Probe response time histograms** — would be nice to graph
  `/readyz` latency, but no scrape stack yet.
- **Per-tenant readiness** — not a thing in this codebase; ignore.

---

## 10. Future "pkg/observability" enhancements

Documented for completeness — not chunk 4:

- Pluggable readiness sub-probes (registry pattern).
- Health-check API client for gRPC peers (mutually-reciprocal
  readiness).
- Built-in slowness-detection alarm via a slow-probe histogram.

---

## 11. Rollback

```sh
git revert <chunk-4-sha>
# Compose files revert; cmd/*/main.go reverts; pkg/observability
# files are deleted; internal/mbs/observability restored.
make deploy-prod-up   # runs against revert; everything back to nc -z healthchecks.
```

Chunk 4 is purely additive (new package, new env, new main.go
shape) with one delete (internal/mbs/observability) that is fully
recoverable from git history. Rollback risk: low.

---

## 12. Open questions

- **Should the metrics port be exposed on host in prod?** Default:
  no, only inside `hermes-net`. Chunk 5's reverse proxy can
  optionally expose a `/metrics` endpoint with auth. Defer
  decision to chunk 5.
- **Should `/readyz` cache its result for N seconds to avoid
  hammering DB.Ping?** Default: no. The mbs pattern doesn't cache
  and it's been fine. Reconsider if production sees probe load
  cause DB pool exhaustion.
- **What's the boot-deadline for `depends_on: service_healthy`?**
  Compose default is unbounded. Chunk 5 runbook should call out
  manual intervention if a backend's DB/NATS deps are stuck.
