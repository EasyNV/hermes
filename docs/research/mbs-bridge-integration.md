# MBS Bridge — Hermes Integration Guide

**Status:** ready to wire (PoC validated end-to-end on Samuel Adi 2026-05-12)
**Authors:** Oracle + Sam
**Audience:** Whoever drives the Hermes-side implementation tomorrow
**Predecessor docs:**
- `re/mbs/mbs-native/README.md` (CLI usage)
- `.hermes/plans/2026-05-12_persist-bridged-session.md` (today's work plan)
- Skill ref `meta-mbs-re/references/cross-stack-token-bridging.md` (technique)

---

## TL;DR

We can headless-login Meta Business Suite accounts on Linux without an
Android phone, without Play Integrity, without the Android risk wall.
The mechanism: drive mautrix-meta's iOS Messenger Lite CAA login to mint
a Meta access_token, then bridge it into a BizApp-shaped MQTToT session
by way of a two-stage CONNECT handshake.

Hermes needs three things to absorb this:
1. **A new microservice or hermes-wa extension** that owns MBS sessions
   (`hermes-mbs`).
2. **A Postgres schema** for storing `mbs_sessions` (token+device_id+
   machine_id, per uid).
3. **A gateway flow** so the operator can drive the bridge login via the
   web dashboard (email → optional 2FA prompt → "session created").

Most of the heavy lifting is already done. We have two stable binaries
in `re/mbs/`:
- `mbs-bridge-login` — drives mautrix-meta, emits a structured envelope
- `mbs-native` — consumes the envelope, persists session, drives MQTToT

Hermes integration is mostly schema + RPC translation, not new RE work.

---

## Background — why this is possible

### The Android wall (background)

The native Android CAA login path (`mbs-native login --email <addr>
--password <pw>`) has been wire-byte-perfect to the real BizApp v551 cold
install since 2026-05-11. Phase 3 added mobileconfig_warm + 8× SDK
prelogin + deletepregent — all gold-equivalent.

It still fails. The wall is `ar_context` Play Integrity attestation: the
server's homepage response contains a packed risk token; the client is
expected to ask Play Integrity for a signed counter-token; without that
counter-token, login returns `generic_error_dialog`. No way around it
on Linux without a real Android device.

### The bridge (the win)

mautrix-meta runs iOS Messenger Lite CAA login (NOT Android, NOT BizApp).
Different app_id, different ClientDocIDs, different UA family. No Play
Integrity — iOS uses DeviceCheck instead, and mautrix-meta uses already-
captured DeviceCheck attestation blobs from real iOS sessions.

The token mautrix-meta mints is *not* app-bound at the data plane. The
MQTToT broker authenticates `(uid, device_id, token)` from its session
cache, not `(uid, app_id)`. So we can pair an iOS-app token with a
BizApp-shaped UA + Android device_id and the broker accepts it.

### The CONNACK code 19 surprise

When we first tried, the slim Lightspeed CONNECT (CONNECT #2, no token
in the frame) was rejected with `code 19`. We initially read this as an
auth failure. It is not.

Code 19 = `FAILED_CONNECTION_UNKNOWN_CONNECT_HASH`. The Lightspeed
CONNECT validates `field 4.18 = MD5(UA + " " + token + " " + device_id +
" ")` against a session-cache entry the broker maintains, keyed by
`(uid, device_id) → (token, UA)`. That cache entry is populated by **any
prior CONNECT #1 (analytics) on the same uid+device_id** — and CONNECT
#1 carries the token in field 5.

Real BizApp does this on every cold launch: analytics CONNECT to log
telemetry, brief teardown, Lightspeed CONNECT for routing. We were
skipping the writer.

**Fix shipped:** `client.WarmupAnalyticsSession(ctx)` does a throwaway
CONNECT #1; `client.ConnectLightspeed()` calls it automatically unless
`Client.SkipAnalyticsWarmup = true`. Universal — applies to every
ConnectLightspeed call site.

---

## What landed in mbs-native (drop-in pieces for Hermes)

### Binaries

```
re/mbs/
├── mautrix-meta-patched/         # vendored upstream + 3-line additive patch
├── mbs-bridge-login/             # standalone driver, exec'd by Hermes
│   ├── main.go
│   ├── go.mod                    # replace go.mau.fi/mautrix-meta => ../mautrix-meta-patched
│   └── mbs-bridge-login          # built binary (~32MB, gitignored)
└── mbs-native/                   # the MQTToT client, library + CLI
    ├── auth/
    │   ├── sessions.go           # SaveSession/LoadSession/SetActive/ResolveCreds
    │   ├── sessions_test.go      # 12 tests, all green
    │   ├── bridge_envelope.go    # schema-versioned struct (mirrored both sides)
    │   ├── bridge_materialize.go # MaterializeCreds(env, existing, forceNewDevice)
    │   └── bridge_materialize_test.go  # 7 tests, all green
    ├── client/
    │   └── client.go             # WarmupAnalyticsSession + auto-warmup in ConnectLightspeed
    └── cmd/
        ├── mbs-native/
        │   ├── login_bridge.go   # `login --via-bridge` orchestrator
        │   └── ...               # send/listen/daemon all use ResolveCreds
        ├── probe-connack/        # 1-shot diagnostic: CONNECT #1 only
        ├── probe-connack-both/   # 1-shot: CONNECT #1 + CONNECT #2 chained
        └── probe-materialize/    # 1-shot: envelope → creds (no network)
```

### Storage layout (single-tenant, file-based)

```
~/.mbs-native/
├── creds.json                            # legacy single-session (kept for back-compat)
├── active                                # text: uid
└── sessions/
    ├── 1674772559.json                   # full Creds, 0600
    ├── 1674772559.bridge.json            # raw bridge envelope, diagnostic
    └── 1674772559.json.bak               # last good before overwrite
```

This file-based layout is what `mbs-native` uses standalone. For Hermes
this becomes a Postgres table — same fields, different store.

### Wire format between bridge and consumer

```go
// auth/bridge_envelope.go (v1)
type BridgeEnvelope struct {
    Version  int   `json:"version"` // hard-versioned, refuse on mismatch
    IssuedAt int64 `json:"issued_at_unix"`

    // From mautrix-meta BloksLoginActionResponsePayload
    AccessToken        string `json:"access_token"`
    UID                int64  `json:"uid"`
    SessionKey         string `json:"session_key"`
    Secret             string `json:"secret"`
    MachineID          string `json:"machine_id"`
    CredentialType     string `json:"credential_type"`
    IsAccountConfirmed bool   `json:"is_account_confirmed"`

    // From mautrix-meta browser.Bridge post-login
    BridgeDeviceID       string `json:"bridge_device_id"`
    BridgeFamilyDeviceID string `json:"bridge_family_device_id"`

    // Session cookies (c_user, datr, xs, etc.) — for refresh paths
    Cookies map[string]string `json:"cookies"`
}
```

Both sides know this schema. Either side can change it, but `Version`
must bump and the consumer refuses on mismatch — fail-fast over silent
shape drift.

---

## Hermes integration plan

### Architecture decision: new microservice `hermes-mbs`

Mirroring `hermes-wa` (whatsmeow session pool), Hermes should have
`hermes-mbs` (mbs-native session pool). Same shape:

- gRPC handlers for login / send / listen
- Maintains a pool of `*mbsnative.Client` instances (one per active uid)
- Subscribes to NATS for outbound work, publishes inbound deltas
- Exposes Prometheus metrics (sessions_active, mqtt_reconnects, etc.)

Why a new service rather than extending hermes-wa: different library,
different protocol family, different lifecycle. Sharing a process is a
maintenance trap. Same pattern Hermes already uses (one service per
protocol concern).

```
hermes-gateway → gRPC → hermes-mbs
                          ↕
                       Postgres (mbs_sessions)
                          ↕
                       NATS (mbs.* events)
                          ↕
                       MQTToT broker (mqtt-mini.facebook.com)
```

### Postgres schema

```sql
-- Schema in mbs/0001_init.sql

CREATE TABLE mbs_sessions (
    uid                BIGINT PRIMARY KEY,
    tenant_id          UUID NOT NULL REFERENCES tenants(id),

    -- Auth (rotates on token refresh)
    access_token       TEXT NOT NULL,
    session_key        TEXT NOT NULL,
    secret             TEXT NOT NULL,
    machine_id         TEXT NOT NULL,

    -- Device identity (persistent across token refreshes — the cache key)
    device_id          UUID NOT NULL,
    family_device_id   UUID NOT NULL,

    -- User-Agent profile (rarely changes; bump when BizApp app_version bumps)
    app_version        TEXT NOT NULL DEFAULT '551.0.0.55.106',
    build_number       TEXT NOT NULL DEFAULT '955655792',
    device_model       TEXT NOT NULL DEFAULT 'SM-S931B',
    android_ver        TEXT NOT NULL DEFAULT '15',
    manufacturer       TEXT NOT NULL DEFAULT 'samsung',
    locale             TEXT NOT NULL DEFAULT 'en_US',
    density            TEXT NOT NULL DEFAULT '2.99375',
    screen_width       INT  NOT NULL DEFAULT 1080,
    screen_height      INT  NOT NULL DEFAULT 2340,
    abi                TEXT NOT NULL DEFAULT 'arm64-v8a',
    version_id         TEXT NOT NULL DEFAULT '26854813974149875',
    mqtt_capabilities  INT  NOT NULL DEFAULT 514,

    -- Bridge metadata (origin tracking; useful for refresh + audit)
    bridge_source      TEXT NOT NULL DEFAULT 'mautrix-meta-ios',
    bridge_envelope    JSONB NOT NULL, -- the original BridgeEnvelope blob

    -- Cookies (preserved for forward-compat / refresh paths)
    cookies            JSONB NOT NULL DEFAULT '{}',

    -- Status
    state              TEXT NOT NULL DEFAULT 'active',  -- active|suspended|burned
    last_connack_rc    SMALLINT,
    last_connack_at    TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (tenant_id, uid)
);

CREATE INDEX idx_mbs_sessions_tenant_state ON mbs_sessions(tenant_id, state);
CREATE INDEX idx_mbs_sessions_updated ON mbs_sessions(updated_at);
```

**Field-by-field mapping from current file-based layout:**

| File layout | DB column |
|---|---|
| `~/.mbs-native/sessions/<uid>.json` | one row in `mbs_sessions` |
| `~/.mbs-native/sessions/<uid>.bridge.json` | `bridge_envelope` (JSONB) |
| `~/.mbs-native/active` | not needed — gateway selects active session by `tenant_id` + RBAC |

**Encryption at rest:** TODO decision. Options:
- pgcrypto with a per-tenant master key (simple, but keys live in env)
- AWS KMS / GCP KMS envelope encryption (better, more infra)
- App-level AES-GCM with a key in `pkg/auth`

For phase 1, keep it plain in DB and rely on `chmod 0600` on the secret
material — match where Hermes is on whatsmeow creds today.

### Proto contract

Add `proto/hermes/v1/mbs.proto`:

```protobuf
syntax = "proto3";

package hermes.v1;

import "hermes/v1/common.proto";

service MBSService {
  // Bridge login — fans out to mbs-bridge-login subprocess.
  // Bidirectional stream because 2FA may interrupt the flow.
  rpc BridgeLogin(stream BridgeLoginRequest) returns (stream BridgeLoginUpdate);

  // List sessions for a tenant.
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);

  // Send a message via an active session.
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);

  // Stream inbound messages (server-streaming, long-lived).
  rpc Listen(ListenRequest) returns (stream InboundMessage);

  // Mark session burned (operator action — bypass risk-engine cleanup).
  rpc BurnSession(BurnSessionRequest) returns (BurnSessionResponse);
}

message BridgeLoginRequest {
  oneof payload {
    BridgeLoginStart start = 1;       // first frame: email/password
    BridgeLoginInput input = 2;       // subsequent frames: 2FA codes etc
    BridgeLoginCancel cancel = 3;
  }
}

message BridgeLoginStart {
  string tenant_id = 1;
  string email = 2;
  string password = 3;             // gateway never logs this
  bool   force_new_device_id = 4;  // optional override
}

message BridgeLoginInput {
  string field_id = 1;             // e.g. "totp_code"
  string value = 2;
}

message BridgeLoginUpdate {
  oneof event {
    BridgeLoginPrompt prompt = 1;       // server wants user input
    BridgeLoginProgress progress = 2;   // status update for UI
    BridgeLoginSuccess success = 3;     // terminal
    BridgeLoginFailure failure = 4;     // terminal
  }
}

message BridgeLoginPrompt {
  string step_id = 1;             // e.g. "two_step_verification"
  string instructions = 2;
  repeated BridgeLoginField fields = 3;
}

message BridgeLoginField {
  string id = 1;
  string name = 2;
  string type = 3;                // "text" | "code" | "password"
}

message BridgeLoginProgress {
  string stage = 1;               // "calling_caa" | "preflight" | "persisting"
  string detail = 2;
}

message BridgeLoginSuccess {
  int64 uid = 1;
  string display_name = 2;
  int32 page_count = 3;           // from /me/accounts probe
}

message BridgeLoginFailure {
  string code = 1;                // e.g. "auth_rejected" | "preflight_rc19"
  string message = 2;
  bool retryable = 3;
}

// SendMessage / Listen / etc — patterned on hermes-wa equivalents
```

### Service skeleton

```
hermes-mbs/
├── cmd/main.go
└── internal/mbs/
    ├── handler/
    │   ├── bridge_login.go       # streams to/from mbs-bridge-login subprocess
    │   ├── send.go
    │   ├── listen.go
    │   └── burn.go
    ├── session/
    │   ├── manager.go            # in-memory pool: uid → *mbsnative.Client
    │   ├── store.go              # Postgres adapter for the auth.Creds shape
    │   └── reconnect.go          # exponential backoff + state transitions
    └── bridge/
        ├── runner.go             # exec.Command wrapper around mbs-bridge-login
        └── envelope.go           # imports mbs-native/auth.BridgeEnvelope
```

**Key dependency:** `hermes-mbs` imports `mbs-native/auth`, `mbs-native/
client`, `mbs-native/mbsnative` directly. mbs-native already has a
library shape (`mbsnative.Session` interface) — that's what hermes-wa-
style session managers consume.

### The bridge subprocess problem

`mbs-bridge-login` is a 32MB binary that links the entire mautrix-meta
runtime. Three integration options:

**Option A — exec.Command per login (current mbs-native pattern):**
- Pros: clean isolation, no Go module conflict between mbs-native and
  mautrix-meta (which transitively pulls maunium/mautrix/bridgev2)
- Cons: 1-2s startup latency per login, binary needs to ship with the
  container
- **Recommended for phase 1.**

**Option B — vendor mautrix-meta as a Go dependency in hermes-mbs:**
- Pros: in-process, no subprocess overhead
- Cons: maunium ecosystem brings ~120 indirect deps; module graph
  conflicts with anything else Hermes uses; license is AGPL (Hermes
  needs to verify compatibility)
- **Not recommended until performance forces it.**

**Option C — wrap mbs-bridge-login in a long-lived service (sidecar):**
- Pros: avoids startup latency, isolated like A
- Cons: requires re-architecting the bridge binary to accept multiple
  logins on stdin (currently one-shot)
- **Defer — option A is fine until login throughput becomes a bottleneck.**

For phase 1, ship the bridge binary in the same container as `hermes-
mbs`, exec it per login. 2FA interactivity is handled via the gRPC
stream — the gateway forwards prompts to the web UI and sends inputs
back. Implementation note: hermes-mbs proxies the bridge subprocess's
stdin/stdout to the gRPC stream.

### NATS event flow

Subjects (matching Hermes EVENTS.md style):

```
mbs.session.created.{tenant}             # bridge login success
mbs.session.connected.{tenant}.{uid}     # MQTT CONNACK rc=0
mbs.session.burned.{tenant}.{uid}        # explicit operator action
mbs.session.disconnected.{tenant}.{uid}  # broker dropped us
mbs.message.outbound.{tenant}.{uid}      # campaign engine → hermes-mbs
mbs.message.inbound.{tenant}.{uid}       # broker → hermes-mbs → inbox
```

Stream config:
- `mbs.session.*` — durable, replay, audit retention 30d
- `mbs.message.*` — durable, ack-on-delivery, retention 7d

### Web UI flow

```
1. Operator clicks "Add MBS Session" in Hermes web dashboard.
2. Modal opens: email + password fields.
3. Submit triggers gateway → hermes-mbs.BridgeLogin (streaming).
4. UI shows live progress: "Authenticating with Meta..." → "Awaiting 2FA"
5. If 2FA: UI shows the prompt fields (whatever mautrix-meta surfaces),
   submits via the same stream.
6. On success: row appears in sessions table; session is active and
   ready to receive sends.
7. On failure: clear error code + suggestion (e.g. "preflight_rc19 —
   broker rejected, retry with --new-device-id").
```

Web component reuses Hermes's existing modal + form patterns. Stream-
based progress is the only new pattern; everything else mirrors how
hermes-wa onboards a whatsmeow session.

---

## Open questions for tomorrow

These need Sam's call before implementation starts.

1. **Multi-tenant boundary.** Does an `mbs_session` belong to a tenant,
   a user within a tenant, or a campaign? Affects RBAC + indexing.

2. **Token rotation / refresh.** mautrix-meta has `MaybeRefreshToken`.
   Do we wire periodic refresh into hermes-mbs from day 1, or wait
   until we see a token expire in production? (Tokens we've seen so
   far have 60+ day lifetimes — defer is probably fine.)

3. **Encryption at rest.** Plain JSONB + chmod-style ACL or pgcrypto/
   KMS envelope encryption? Pick a posture matching hermes-wa.

4. **Outbound rate limiting.** mbs-native has no built-in rate limiter
   today. hermes-campaign throttles whatsmeow sends; should mbs sends
   share that throttle pool or use a separate one? (Different broker,
   different ban surface — probably separate.)

5. **Page-token enumeration.** The bridge mints user-tokens which can
   be exchanged for page-tokens via `/me/accounts`. Some MBS operations
   (page-scoped messaging) need page-tokens. Do we cache these in DB
   alongside the user-token, or mint on demand? (Probably cache, with
   TTL refresh.)

6. **Web dashboard scope.** Is this an "internal-tool" admin-only flow
   or do tenants self-serve their own MBS sessions? Affects auth
   middleware and audit log granularity.

7. **Connection pool sizing.** mbs-native uses one TCP connection per
   `*Client`. Hermes-wa shards 500 sessions per pod via whatsmeow's
   internal pooling. mbs-mini broker tolerates concurrent connections
   from the same uid but emit risk signals on more than ~3. Need a
   per-uid mutex policy — propose "at most one active Lightspeed
   connection per uid, all sends serialized through it." This matches
   how real BizApp behaves.

---

## Risk register

| Risk | Severity | Mitigation |
|---|---|---|
| Meta closes the cross-stack pathway | High | We have the native Phase 3 Android wire path ready as fallback (just blocked by Play Integrity, not the broker). Monitor for shape changes on `pwd_key_fetch` and `send_login_request`. |
| mautrix-meta upstream breaking changes | Medium | We hold a patched fork in `re/mbs/mautrix-meta-patched/`. Rebase quarterly. Patch is 3 lines (LastLoginPayload + LastBridgeIdentity getter). |
| Token revocation by Meta (mass) | Medium | Bridge login is repeatable; web UI lets operator re-mint. Implement automatic re-bridge on `CONNACK rc=4` (auth rejected). |
| Device_id burn (specific uid blacklisted) | Low-Medium | `--new-device-id` regenerates. Track per-uid `burned_at` in DB; refuse sends from burned sessions. |
| Bridge binary OOM / hang | Low | 60s timeout in `exec.Command`. Kill the subprocess, mark login as failed, retryable. |
| Multiple Hermes pods running mbs sessions for same uid | High if it happens | Postgres advisory lock per uid in `hermes-mbs.session.manager`. Refuse second pod's CONNECT. |

---

## Phase 1 deliverable (minimum viable)

- [ ] `proto/hermes/v1/mbs.proto` written + reviewed
- [ ] Postgres migration `mbs/0001_init.sql`
- [ ] `cmd/mbs/main.go` boilerplate (config, DB, NATS, gRPC server)
- [ ] `internal/mbs/handler/bridge_login.go` (streaming login)
- [ ] `internal/mbs/handler/send.go` (single message send via existing
      `mbsnative.Session.Send`)
- [ ] `internal/mbs/session/store.go` (CRUD on `mbs_sessions`)
- [ ] `internal/mbs/session/manager.go` (in-memory client pool, advisory
      lock per uid)
- [ ] `internal/mbs/bridge/runner.go` (exec + stdin/stdout proxy)
- [ ] hermes-gateway: wire MBS endpoints into web API
- [ ] hermes-web: "Add MBS Session" modal + "Send via MBS" send action
- [ ] Smoke test: web UI → login → send → message arrives in receiver

Out of scope for phase 1 (defer):

- Listen / inbound message routing
- Bulk campaign send via MBS sessions
- Multi-page support (page-token caching)
- Automatic token refresh
- Session-pool autoscaling
- **Phone → thread_id resolver** — see `.hermes/plans/2026-05-12_phone-to-thread-resolver.md`.
  The dashboard UX of "type a phone, click send" requires this. Currently
  operator must already know the numeric thread_id. RE work is ~2 hours
  once we have a clean mitm capture of MBS doing a "new chat" flow on
  the rooted device. After RE, implementation is ~3 hours wire+cache+CLI.

---

## Reference — what the PoC did

For the integration team, a verbatim transcript of the working flow on
Samuel Adi (uid `1674772559`, target thread `1066489929880919`):

```
$ go run ./cmd/mbs-native login --via-bridge \
    --email arkreborn@gmail.com --password '<pw>' --verbose
[bridge] using binary: re/mbs/mbs-bridge-login/mbs-bridge-login
→ driving mautrix-meta iOS Messenger Lite login...
[bridge] Making Bloks request bloks_app=com.bloks.www.bloks.caa.login.process_client_data_and_redirect
[bridge] Request successful status_code=200 duration=2599ms url=.../graphql
[bridge] Request successful status_code=200 duration=201ms  url=.../pwd_key_fetch
[bridge] Making Bloks request bloks_app=com.bloks.www.bloks.caa.login.async.send_login_request
[bridge] Request successful status_code=200 duration=5548ms url=.../graphql
✓ envelope written to ~/.mbs-native/tmp/envelope-*.json (uid=1674772559)
✓ bridge envelope received (uid=1674772559, credential_type=password, 4 cookies)
→ pre-flight: CONNECT #1 (analytics) handshake to validate creds...
[mbs] warmup CONNECT #1 thrift: 1114 bytes uncompressed
[mbs] ✓ warmup CONNACK accepted — session cached
✓ pre-flight CONNACK accepted

✓ session 1674772559 saved (active)
  session:  ~/.mbs-native/sessions/1674772559.json
  envelope: ~/.mbs-native/sessions/1674772559.bridge.json
  active:   ~/.mbs-native/active

$ go run ./cmd/mbs-native send --thread 1066489929880919 --text 'persistent session test'
→ connecting to mqtt-mini.facebook.com (Lightspeed channel)...
✓ connected
→ bootstrapping thread 1066489929880919...
✓ bootstrapped
→ sending: "persistent session test"
=== Send result ===
OTID:     7460304886188273944
MID:      mid.$cAAAAAM58YiakUpReBWeIU8SBXPEY
Latency:  1321ms
```

Total time email-to-delivery: ~10 seconds (8s bridge + 2s MQTT). This
includes one inadvertent retry on `pwd_key_fetch` — steady-state should
be ~6-7s.

---

## Contact

If you hit something in the integration that doesn't match this doc,
ping Oracle (Hermes agent profile) for live debugging. The mbs-native
codebase is fully instrumented with `--verbose` and `MBSNATIVE_WIRE_
DUMP_DIR` env var for byte-level inspection.
