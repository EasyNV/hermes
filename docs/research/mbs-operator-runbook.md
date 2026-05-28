# MBS Operator Runbook

**Audience:** Hermes operators and on-call engineers running MBS sessions
**Status:** validated 2026-05-26
**Companion docs:** [`mbs-bridge-integration.md`](./mbs-bridge-integration.md) (architecture), [`mbs-bridge-integration-phase2.md`](./mbs-bridge-integration-phase2.md) (current state)

---

## Quick reference

| Task | Command |
|---|---|
| Login a fresh account | `mbs-native login --via-bridge --email X --password Y --totp-secret Z` |
| Login (interactive 2FA prompt) | `mbs-native login --via-bridge --email X --password Y` |
| Inspect active session | `mbs-native creds-show` |
| Send to a known thread | `mbs-native send --thread <id> --text "..."` |
| Send to a phone (cold compose) | `mbs-native send-to-phone --phone +E164 --text "..."` |
| Resolve phone → thread_id (no send) | `mbs-native send-to-phone --phone +E164 --resolve-only` |
| Listen for inbound | `mbs-native listen` (interactive) or `mbs-native daemon` (background) |
| Switch active session | edit `~/.mbs-native/active` (single line: uid) |

---

## End-to-end smoke test (the canonical happy path)

```bash
cd /Users/env/Projects/hermes/re/mbs/mbs-native

# 1. Login (writes ~/.mbs-native/sessions/<uid>.json + sets active)
go run ./cmd/mbs-native login --via-bridge \
  --email 'arkreborn@gmail.com' \
  --password '<password>' \
  --totp-secret 'OTEF BWDK WIPE OHVA 2XRV 77MB IKYW RVEF' \
  --verbose

# Expected: ~10s wall time, ending with:
#   ✓ session 1674772559 saved (active)
#   business assets:
#     page_id: <id> (<page name>)
#     waba_id: <id>
#     wec_mailbox_id: <id>
#     wec_phone: <number>

# 2. Verify assets were discovered
go run ./cmd/mbs-native creds-show
# Expected: --- business assets --- block populated

# 3. Send a test message to a phone number (no thread_id needed)
go run ./cmd/mbs-native send-to-phone \
  --phone +6282142497885 --text "smoke test from runbook"

# Expected output ending with:
#   === Send result ===
#   OTID:     <random>
#   MID:      mid.$cAAAAA...
#   Latency:  <1500ms
```

If the recipient receives "smoke test from runbook" on WhatsApp, the
whole stack is healthy.

---

## What success looks like at each layer

### Layer 1: Bridge login

```
[bridge] using binary: …/mbs-bridge-login/mbs-bridge-login
→ driving mautrix-meta iOS Messenger Lite login...
DBG Making Bloks request bloks_app=com.bloks.www.bloks.caa.login.process_client_data_and_redirect
DBG Request successful status_code=200 url=https://graph.facebook.com/graphql
DBG Request successful status_code=200 url=https://graph.facebook.com/pwd_key_fetch
DBG Making Bloks request bloks_app=com.bloks.www.bloks.caa.login.async.send_login_request
DBG Request successful status_code=200 url=https://graph.facebook.com/graphql
✓ envelope written to ~/.mbs-native/tmp/envelope-*.json (uid=<uid>)
```

Three calls (PCDAR, pwd_key_fetch, send_login_request) → envelope on disk.

### Layer 2: MQTT preflight

```
→ pre-flight: CONNECT #1 (analytics) handshake to validate creds...
[mbs] warmup CONNECT #1 thrift: <bytes> bytes uncompressed
[mbs] warmup CONNECT #1 frame: <bytes> bytes
[mbs] ✓ warmup CONNACK accepted — session cached
✓ pre-flight CONNACK accepted
```

The MQTToT broker now has `(uid, device_id) → token` cached and will
accept Lightspeed CONNECT.

### Layer 3: Asset discovery

```
→ discovering business assets (scoping query)...
  discovered <N> page(s)
  picked page <id> (waba=<waba>, phone=<phone>)
  WEC mailbox: <id>
```

GraphQL bootstrap populates `creds.PageID`, `creds.WABAID`,
`creds.WECMailboxID`, `creds.WECPhoneNumber`, `creds.PageName`.

### Layer 4: Send

```
→ resolving phone +<E164> on page <id> (waba=<id>, mailbox=<id>)...
✓ resolved: customer_id=<thread_fbid> (thread_fbid=<numeric>)
→ connecting to mqtt-mini.facebook.com (Lightspeed)...
✓ connected
→ bootstrapping thread <id>...
✓ bootstrapped
→ sending "<text>" to thread <id>...

=== Send result ===
OTID: <random>
MID:  mid.$cAAAAA...
Latency: <ms>
```

`MID` of the form `mid.$cAAAAA...` is the server-assigned message ID.
That's delivery confirmation.

---

## Failure modes & their fingerprints

### F1: Native CAA login → `generic_error_dialog`

```
ERROR: login: login: send_login_request: parse: auth: login failed
       (server-side): Login Error — An unexpected error occurred.
```

**Cause:** Meta's risk wall on Go's TCP/TLS/H2 stack from non-Android
network egress. We've confirmed this across ~40 attempts on multiple
fresh accounts and IPs since May 2026. Wire is byte-perfect; the wall
is sub-application-layer.

**Action:** Do not retry native flow. Use `--via-bridge`. The bridge
runs the same CAA login but through mautrix-meta's iOS app surface,
which doesn't trigger the same risk profile.

### F2: Bridge login → 2FA prompt loops

```
── two_step_verification ──
  > totp_code:
(user types code; bridge re-prompts after 30s)
```

**Cause:** code expired between user typing and submission. RFC 6238
windows are 30s.

**Action:** use `--totp-secret` instead of `--totp`. The
`SecretTOTPProvider` re-derives on retry so the second attempt gets a
fresh code automatically.

### F3: Preflight CONNACK rc=19

```
✗ warmup CONNACK rejected (rc=19)
```

**Cause:** `FAILED_CONNECTION_UNKNOWN_CONNECT_HASH`. Broker's session
cache doesn't have this `(uid, device_id) → (token, UA)` entry yet.

**Action:** retry once — sometimes the analytics CONNECT race-loses to
the Lightspeed CONNECT in our own preflight. If repeated:

```bash
mbs-native login --via-bridge ... --new-device-id  # fresh device → fresh cache key
```

### F4: Send → `Couldn't send.` in app, MID empty in CLI

```
=== Send result ===
OTID:     7464997580540287993
MID:                            ← empty
```

In the receiver's app, the message bubble shows "Couldn't send."

**Cause:** broker accepted publish but never routed. Almost always
because `Bootstrap()` was skipped — the broker silently drops label-46
publishes for un-bootstrapped threads.

**Action:** check that the CLI emitted the bootstrap lines. If using
the library directly, call `client.Bootstrap(ctx, threadID)` before
`Send`.

### F5: send-to-phone → resolver rejection

```
✗ resolve: create_customer rejected (phone=6285...) code=NOT_ELIGIBLE:
  Outside 24h window
```

**Cause:** WhatsApp business policy — page can't cold-compose to this
number outside the 24h window since their last inbound message. Not a
technical failure.

**Action:** either (a) ask the recipient to message the page first, or
(b) use a template message (not yet wired in mbs-native — future work).

### F6: GraphQL → HTTP 190 (OAuthException)

```
✗ graphql FetchBusinessScopingConfig: code=190 type=OAuthException
  subcode=463: Access token expired
```

**Cause:** access_token revoked or expired.

**Action:** re-bridge login. If repeated within hours of a fresh
login, the account is likely under risk review at Meta — back off,
try a different network, or rotate to a fresh account.

---

## Mitmproxy debugging recipe

When you need to inspect what BizApp / mbs-native is putting on the
wire, this is the working invocation as of 2026-05-26:

```bash
mitmdump \
  --listen-host 0.0.0.0 \
  --listen-port 8080 \
  --set http3=false \
  --tcp-hosts 'mqtt-mini\.facebook\.com|edge-mqtt\.facebook\.com|gateway\.facebook\.com' \
  -w captures/$(date +%Y%m%d-%H%M%S).mitm
```

Both options are required. See the phase 2 supplement doc for the
forensics of why.

To replay the captured flows against the parsers without re-running
the network:

```bash
mitmdump -nr captures/<file>.mitm -s <your-analysis-script>.py
```

The graphql package supports `MBSNATIVE_HOST_OVERRIDE` so you can
point its `Client` at an httptest.Server holding a captured response —
useful for parser regression testing.

---

## Production runbook (for hermes-mbs operators, once shipped)

### Daily checks
- `SELECT count(*) FROM mbs_sessions WHERE state = 'active'` — should
  match expected campaign count
- Prometheus: `mbs_session_connack_rc` distribution should be ~100% on rc=0
- NATS: `mbs.session.disconnected.*` rate should be < 1/hour per session
  (transient reconnects are normal)

### Session refresh cadence
The bridge mints tokens with 60+ day lifetimes. Hermes doesn't yet have
an automated refresh path. Manual rotation:

```bash
# Find sessions older than 50 days
psql -c "SELECT uid, tenant_id, age(now(), updated_at) FROM mbs_sessions
         WHERE state='active' AND updated_at < now() - interval '50 days'"

# For each, re-login (operator-facing flow in web UI)
```

V2 should add automatic `MaybeRefreshToken` paths.

### When an account gets burned

Signature: any of:
- Repeated `connack_rc=4` on warmup
- HTTP 190 (OAuthException) on graph.facebook.com calls
- Login responses include "Account locked" or similar copy

Recovery:
```sql
UPDATE mbs_sessions SET state = 'burned', burned_at = NOW() WHERE uid = $1;
```

Operator manually re-onboards a different account or retries this
account from a different network after a cooldown (24-48 hours
typical).

### Multi-pod safety

Per-uid Postgres advisory lock in `hermes-mbs.session.manager`:

```sql
SELECT pg_try_advisory_lock(hashtext($1::text));
-- where $1 = uid::text
```

The pod that acquires the lock owns the session. Second pod refuses
its own CONNECT. Lock released on graceful shutdown or after stale
timeout (~5 min).

---

## File paths cheat sheet (mbs-native standalone)

```
~/.mbs-native/
├── active                           # text: uid of currently active session
├── creds.json                       # legacy single-session (kept for back-compat)
├── sessions/
│   ├── <uid>.json                   # full Creds, chmod 0600
│   ├── <uid>.bridge.json            # raw bridge envelope (diagnostic)
│   └── <uid>.json.bak               # last-good before overwrite
├── devices/
│   └── <sha256(email)[:16]>.json    # device state (persistent UUID)
├── debug/
│   └── send_login_request-*.json    # failed login response bodies
└── tmp/
    └── envelope-*.json              # bridge → consumer handoff
```

In Hermes those collapse to:

```
Postgres: mbs_sessions, mbs_phone_threads, mbs_session_devices
KMS/encrypted: access_token, secret, session_key, totp_secret
```

---

## When this doc is wrong

The mbs-native CLI is fully `--verbose` instrumented and the underlying
library exposes every wire-level knob via env vars
(`MBSNATIVE_TLS_PROFILE`, `MBSNATIVE_H2_PROFILE`,
`MBSNATIVE_WIRE_DUMP_DIR`, `MBSNATIVE_HOST_OVERRIDE`,
`MBSNATIVE_TOTP_SECRET`, `MBSNATIVE_CONN_UUID`, etc.). Use those for
diagnosis; ping Oracle if you find something this runbook doesn't
cover.
