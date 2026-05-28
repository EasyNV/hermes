# MBS Bridge — Hermes Integration (Phase 2 Supplement)

**Status:** ready to wire (PoC validated end-to-end on 2026-05-26)
**Authors:** Oracle + Sam
**Audience:** Whoever drives the Hermes-side implementation tomorrow
**Predecessor:** [`mbs-bridge-integration.md`](./mbs-bridge-integration.md) (May 12, base architecture)
**Companion plans:**
- `.hermes/plans/2026-05-26_path-c-phone-resolver.md`
- `.hermes/plans/2026-05-26_stable-login-to-message-pipeline.md`

---

## TL;DR — what's new since May 12

The May 12 doc described the end-to-end mechanism: bridge login → MQTToT
session → MID-returning send. Verified working.

Since then **the operator UX is now fully closed-loop** without ever
needing to type a thread_id or business asset id:

1. **TOTP secrets** are supported as a first-class input — no more
   manual code rotation during scripted logins.
2. **Asset auto-discovery** at login time — `creds.json` now carries
   `page_id`, `waba_id`, `wec_mailbox_id`, `wec_phone_number`. The
   operator never needs to look these up.
3. **Phone-number → thread_id resolver** is implemented — Path C, a
   single GraphQL mutation (`BizInboxWhatsAppCustomerMutation`). Cold-
   compose to arbitrary E.164 numbers works.
4. **`mbs-native send-to-phone`** ties everything together — type a
   phone, get a delivered message. Zero IDs needed.
5. **mitmproxy 12.x gotchas** documented for future debugging — both
   HTTP/3 default-on and HTTP/2 PING-relay regressions.

Net effect on Hermes phase-1 deliverable: **two open questions from the
May 12 doc are now closed** (asset discovery, phone resolver). Phase 1
shrinks accordingly.

---

## Verified end-to-end transcript (2026-05-12 — still works today)

The May 12 PoC transcript is reproduced verbatim at the bottom of the
predecessor doc. Re-verified today on `arkreborn@gmail.com`:

```bash
$ cd /Users/env/Projects/hermes/re/mbs/mbs-native
$ go run ./cmd/mbs-native login --via-bridge \
    --email 'arkreborn@gmail.com' --password '<pw>' --verbose
[bridge] using binary: …/mbs-bridge-login/mbs-bridge-login
→ driving mautrix-meta iOS Messenger Lite login...
[bridge] Bloks login process_client_data_and_redirect → 200 OK (2599ms)
[bridge] pwd_key_fetch → 200 OK (201ms)
[bridge] Bloks login async.send_login_request → 200 OK (5548ms)
✓ envelope written to ~/.mbs-native/tmp/envelope-*.json (uid=1674772559)
→ pre-flight: CONNECT #1 (analytics) handshake to validate creds...
[mbs] ✓ warmup CONNACK accepted — session cached
✓ pre-flight CONNACK accepted

✓ session 1674772559 saved (active)
  session:  ~/.mbs-native/sessions/1674772559.json
  envelope: ~/.mbs-native/sessions/1674772559.bridge.json
  active:   ~/.mbs-native/active

$ go run ./cmd/mbs-native send --thread 1066489929880919 \
    --text 'persistent session test'
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

Total time email-to-delivery: **~10 seconds** (8s bridge + 2s MQTT).

The Android native CAA flow (`mbs-native login` without `--via-bridge`)
still hits Meta's risk wall and returns `generic_error_dialog` —
confirmed again 2026-05-26 21:47 from this Mac IP. **The bridge is the
only production-viable login path.** Native login code stays for wire
RE archaeology, not for production use.

---

## What landed since May 12

### Stage 1 — TOTP secret support

**Files:**
- `re/mbs/mbs-native/auth/totp_secret.go`
- `re/mbs/mbs-native/auth/totp_secret_test.go`
- `re/mbs/mbs-native/cmd/mbs-native/login.go` (`--totp-secret` flag wiring)

**API change:**

```go
// auth/totp_secret.go
type SecretTOTPProvider struct {
    Secret string                  // base32 seed, any common formatting
    NowFn  func() time.Time        // test override; nil = real wall clock
}

func (p SecretTOTPProvider) ProvideTOTP(ctx context.Context) (string, error)
func (p SecretTOTPProvider) WaitForPushApproval(ctx context.Context) error

func NormalizeTOTPSecret(raw string) (string, error)
```

`SecretTOTPProvider` satisfies the existing `auth.TwoFAProvider`
interface and is a drop-in replacement for `StaticTOTPProvider` whenever
the caller has access to the underlying TOTP secret (rather than a
one-shot 6-digit code).

**Behaviour:** on each `ProvideTOTP()` call the provider re-derives the
current code via RFC 6238 (HMAC-SHA1, 30s window, 6 digits). If the
orchestrator retries after a stale-code rejection, the second call
produces a fresh code automatically — no manual rotation needed in the
caller.

**Accepted secret formats** (all equivalent):
- `OTEFBWDKWIPEOHVA2XRV77MBIKYWRVEF`
- `OTEF BWDK WIPE OHVA 2XRV 77MB IKYW RVEF`
- `otef-bwdk-wipe-ohva-2xrv-77mb-ikyw-rvef`
- `otef_bwdk_wipe_ohva_2xrv_77mb_ikyw_rvef`
- mixed case + whitespace

**CLI:**

```bash
mbs-native login \
  --email <addr> --password <pw> \
  --totp-secret 'OTEF BWDK WIPE OHVA 2XRV 77MB IKYW RVEF'

# Also via env (useful for scripts where flag values are logged):
export MBSNATIVE_TOTP_SECRET='OTEF BWDK WIPE OHVA 2XRV 77MB IKYW RVEF'
mbs-native login --email <addr> --password <pw>
```

`--totp-secret` is mutually exclusive with `--totp` (one-shot code).
The CLI exits with code 2 on conflict.

**Hermes integration impact:** the `BridgeLoginInput` proto stays —
the operator still types a code in the modal because *the bridge*
prompts interactively. But the hermes-mbs service can choose to inject
a derived code automatically if it has the secret stored, by adapting
the bridge subprocess interaction:

```go
// hermes-mbs/internal/mbs/bridge/runner.go
if step.fieldID == "totp_code" && session.HasTOTPSecret() {
    code, _ := auth.SecretTOTPProvider{Secret: session.TOTPSecret}.
        ProvideTOTP(ctx)
    runner.WriteStdin(code + "\n")
    continue
}
// otherwise forward the prompt to the gRPC client (web UI)
```

This makes campaigns with stored TOTP secrets fully unattended.

**Storage implication (Postgres):** add a column.

```sql
ALTER TABLE mbs_sessions ADD COLUMN totp_secret_enc BYTEA;
-- encrypted at application layer (or pgcrypto / KMS)
```

Treat the TOTP secret with the same posture as `access_token`. It's a
sustained credential that can mint codes indefinitely. Never log,
never return over an API beyond the originating BridgeLogin RPC.

---

### Stage 2 — Asset auto-discovery (BizAppBusinessScopingConfigQuery)

**Files:**
- `re/mbs/mbs-native/graphql/client.go` — new package, Tigon-utls transport
- `re/mbs/mbs-native/graphql/scoping.go` — discovery query
- `re/mbs/mbs-native/graphql/fetch_mailbox.go` — page → WEC mailbox
- `re/mbs/mbs-native/graphql/wa_page_config.go` — fallback (waba+phone)
- `re/mbs/mbs-native/cmd/mbs-native/asset_discovery.go` — orchestration
- `re/mbs/mbs-native/graphql/testdata/biz_app_business_scoping_config.{req,res}.*` — golden artifacts

**The discovery query:**

```
POST graph.facebook.com/graphql
fb_api_req_friendly_name = BizAppBusinessScopingConfigQuery
client_doc_id            = 247000766411050793082820560896
fb_api_analytics_tags    = ["GraphServices"]
variables = {
  "profile_image_size": 500,
  "should_fetch_whatsapp_business_account": true,
  "should_fetch_mbs_data": true,
  "should_fetch_ig_backed_page": true,
  "include_dead_graphql_fields": false,
  "epd_feature_switches": ["FOLDER","SAVED_RESPONSE","AUTO_RESPONSE",
                           "NON_CONTENT_BASED_SEARCH_CUSTOMER_NAME",
                           "NON_CONTENT_BASED_SEARCH_LABEL",
                           "CONTENT_BASED_SEARCH_MESSENGER","CONTACT_LIST"],
  "should_fetch_wa_without_bpn": true
}
```

Variables are byte-equivalent to BizApp v551 cold-install wire.

**Response shape (only the fields we need):**

```
viewer.business_scoping.global_scopes.nodes[]
  └─ asset_lists[]
      └─ objects.nodes[]
          └─ ent
              ├─ id                                   ← page_id
              ├─ name                                 ← page display name
              └─ business_presence_node
                  ├─ id                               ← business_presence_node_id
                  └─ business_presence_linked_whatsapp_business_profile
                      ├─ phone_number                 ← WEC sender phone
                      └─ number_current_status
                          └─ whatsapp_business_account
                              └─ id                   ← waba_id
```

The parser walks defensively — any missing branch just yields fewer
assets, no hard schema dependence beyond `id` + `business_presence_node`.

**Public API:**

```go
// graphql/client.go
type Client struct { /* ... */ }
func New(creds *auth.Creds) (*Client, error)

// graphql/scoping.go
type Asset struct {
    PageID                 string
    PageName               string
    BusinessPresenceNodeID string
    WABAID                 string  // empty if no WABA
    WABAPhoneNumber        string
    IGAccountID            string
}
func (a Asset) HasWABA() bool

func (c *Client) FetchBusinessScopingConfig(ctx context.Context) ([]Asset, error)
func ParseBusinessScopingAssets(root map[string]any) ([]Asset, error)
func PickWABAAsset(assets []Asset) (*Asset, error)   // returns ErrNoWABAAsset if none
```

**Discovery orchestration (`cmd/mbs-native/asset_discovery.go`):**

```go
func discoverAndPopulateAssets(ctx context.Context, creds *auth.Creds, verbose bool) error {
    // 1. graphql.FetchBusinessScopingConfig — enumerate
    // 2. PickWABAAsset — choose primary
    // 3. graphql.FetchPageMailboxInfo — WEC mailbox_id
    // 4. graphql.FetchWhatsAppPageConfig — wec_phone_number fallback
    // All failures are non-fatal — login still succeeds with whatever
    // info was gathered. Worst case: send-to-phone needs --page/--waba flags.
}
```

**Creds extension (`auth/auth.go`):**

```go
type Creds struct {
    // ...existing fields...

    // Business assets — populated post-login by the CLI bootstrap.
    // All omitempty so existing creds files load unchanged.
    PageID         string `json:"page_id,omitempty"`
    WABAID         string `json:"waba_id,omitempty"`
    WECMailboxID   string `json:"wec_mailbox_id,omitempty"`
    WECPhoneNumber string `json:"wec_phone_number,omitempty"`
    PageName       string `json:"page_name,omitempty"`
}
```

**Hermes integration impact:**

Schema update (`mbs/0002_assets.sql` migration):

```sql
ALTER TABLE mbs_sessions
    ADD COLUMN page_id          TEXT,
    ADD COLUMN page_name        TEXT,
    ADD COLUMN waba_id          TEXT,
    ADD COLUMN wec_mailbox_id   TEXT,
    ADD COLUMN wec_phone_number TEXT;

CREATE INDEX idx_mbs_sessions_page_id ON mbs_sessions(page_id) WHERE page_id IS NOT NULL;
CREATE INDEX idx_mbs_sessions_waba_id ON mbs_sessions(waba_id) WHERE waba_id IS NOT NULL;
```

`BridgeLoginSuccess` proto gains:

```protobuf
message BridgeLoginSuccess {
  int64 uid = 1;
  string display_name = 2;
  int32 page_count = 3;

  // NEW: asset info from BizAppBusinessScopingConfigQuery
  string page_id = 4;
  string page_name = 5;
  string waba_id = 6;
  string wec_mailbox_id = 7;
  string wec_phone_number = 8;
}
```

**Multi-page sessions:** when an account admins >1 WABA-connected page,
`PickWABAAsset` returns the first one and the CLI logs a warning. For
Hermes, the better UX is to surface the full list to the operator and
let them pick. Add an RPC:

```protobuf
rpc ListSessionAssets(ListSessionAssetsRequest) returns (ListSessionAssetsResponse);

message Asset {
  string page_id = 1;
  string page_name = 2;
  string waba_id = 3;
  string wec_mailbox_id = 4;
  string wec_phone_number = 5;
  bool has_waba = 6;
}
```

Or store all assets per session (separate table `mbs_session_assets`)
and let campaigns choose page per send. **Recommendation:** keep
session→single-primary-asset for v1, model multi-page as a v2 feature
once we see real multi-page accounts in the field.

---

### Stage 3 — Phone → thread_id resolver (Path C)

**Files:**
- `re/mbs/mbs-native/graphql/resolver.go` — the mutation
- `re/mbs/mbs-native/graphql/testdata/biz_inbox_whatsapp_customer_mutation.*` — goldens

**The resolver:**

```
POST graph.facebook.com/graphql
fb_api_req_friendly_name = BizInboxWhatsAppCustomerMutation
client_doc_id            = 170668148515982196402669844503
fb_api_analytics_tags    = ["visitation_id=null","GraphServices"]
variables = {"input":{
  "page_id":      "<page>",
  "phone_number": "<E.164 without leading +>",
  "mailbox_id":   "<wec_mailbox_id>"
}}
→ {"data":{"xfb_biz_inbox_whatsapp_create_customer":{
   "customer_id":"<thread_fbid>",
   "is_success":true
}}}
```

The returned `customer_id` IS the `thread_fbid` we feed to `client.Send`.

**Public API:**

```go
// graphql/resolver.go
func (c *Client) CreateWhatsAppCustomer(
    ctx context.Context,
    pageID, mailboxID, phone string,
) (customerID string, err error)

func (c *Client) ResolvePhoneToThreadID(
    ctx context.Context,
    pageID, phone string,
) (customerID, wecMailboxID string, err error)

func NormalizePhone(raw string) (string, error)

type CreateCustomerError struct {
    Phone, PageID, MailboxID string
    ErrorCode, ErrorMessage  string
}
```

**Phone normalization rules:**
- Strip all non-digits
- `"00…"` prefix → drop the `00` (international form)
- `"0…"` prefix → prepend `"62"` (Indonesia default, since Sam's ID-region)
- Validate length 8-15 (E.164)

For non-ID deployments, callers should pass E.164 form directly. The
parser tolerates `+`, spaces, dashes, parens.

**Hermes integration impact:**

New gRPC method on `MBSService`:

```protobuf
rpc ResolvePhone(ResolvePhoneRequest) returns (ResolvePhoneResponse);

message ResolvePhoneRequest {
  int64  uid = 1;
  string phone = 2;          // any common format
  string page_id_override = 3;  // optional, defaults to creds.page_id
}
message ResolvePhoneResponse {
  string customer_id = 1;    // = thread_fbid (decimal string)
  string wec_mailbox_id = 2;
  string normalized_phone = 3;
}
```

`SendMessage` should grow a phone-shaped variant:

```protobuf
message SendMessageRequest {
  int64 uid = 1;
  oneof recipient {
    int64  thread_fbid = 2;   // existing behaviour
    string phone = 3;         // NEW — auto-resolve via Path C
  }
  string text = 4;
  bytes  client_dedupe_id = 5;
}
```

When `phone` is set, hermes-mbs calls the resolver, persists the
mapping `(uid, phone) → thread_fbid` in a small `mbs_phone_threads`
cache table (so repeated sends to the same number skip the resolve
call), then forwards to the existing `client.Send` codepath.

```sql
CREATE TABLE mbs_phone_threads (
    uid          BIGINT NOT NULL REFERENCES mbs_sessions(uid),
    phone        TEXT NOT NULL,          -- E.164 normalized
    thread_fbid  BIGINT NOT NULL,
    page_id      TEXT NOT NULL,          -- which page resolved this
    mailbox_id   TEXT NOT NULL,
    last_send_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (uid, phone, page_id)
);
```

TTL: the mapping is stable for the lifetime of the page+phone
relationship (verified: same phone re-resolved on a different day
returns the same `customer_id`). No expiry needed; we evict on
explicit cache-bust.

**Failure handling:**

`CreateCustomerError` surfaces `error_code` (string) and `error_message`
when the server rejects the resolve. Most common rejection: 24h-window
policy violation on accounts that haven't established opt-in. Surface
this verbatim to the operator — it's a business-rule problem, not a
technical one.

---

### Stage 4 — `send-to-phone` CLI (the closed loop)

**File:** `re/mbs/mbs-native/cmd/mbs-native/send_to_phone.go`

**Usage:**

```bash
# Auto-load page/waba/mailbox from creds (set at login time):
mbs-native send-to-phone --phone +6282142497885 --text "hello"

# Override any/all of those:
mbs-native send-to-phone --page <id> --waba <id> \
  --phone +6282142497885 --text "hello"

# Diagnostic: resolve without sending:
mbs-native send-to-phone --phone +6282142497885 --resolve-only
```

Internal flow:
1. `auth.ResolveCreds()` — picks active session
2. Resolve page_id / waba_id / wec_mailbox_id (flag → creds → live fetch
   via `FetchPageMailboxInfo` as last resort)
3. `graphql.CreateWhatsAppCustomer(page, mailbox, phone)` → `customer_id`
4. `client.New(creds)` + `ConnectLightspeed` + `Bootstrap(customer_id)`
5. `client.Send(customer_id, text)` → `MID`

This is the **operator-facing equivalent of the full Hermes send-via-MBS
pipeline**. If this works end-to-end, hermes-mbs implementation is just
a gRPC translation layer.

---

## mitmproxy 12.x pitfalls (debugging the bridge in the lab)

When debugging the bridge + native flow via mitmproxy 12.x, two
defaults will break MBS traffic. Burned several hours figuring this
out — documented here so the next person doesn't.

### Pitfall 1: HTTP/3 default-on

mitmproxy 12.x enables HTTP/3/QUIC by default (was off in 11.x). Under
macOS 26.4 + OpenSSL 3.5, mitm's QUIC implementation returns version-
negotiation packets to BizApp's Tigon. Tigon retries 100+/min and
starves the MSYS task queue — sends timeout with `DGW-send-ack-time-
out` cascades, MSYS writes `"Couldn't send."` to local SQLite as a
client-side give-up.

**Signature in logcat:**
```
HQSession.cpp: Connection closed with error err=Local error:
  0x4000000a msg=Received version negotiation packet
```

**Fix:**
```bash
mitmdump --set http3=false ...
```

### Pitfall 2: HTTP/2 PING relay broken for gateway.facebook.com

The MBS streaming surface uses bidirectional HTTP/2 PING frames as
keepalive on `gateway.facebook.com:/lightspeed`, `/rpsignaling`,
`/streamcontroller`. mitmproxy 12.x consumes client PINGs at the
proxy hop instead of forwarding them. ~25s without a PING ACK and
the client kills the stream — reset loop, sends never get a working
channel.

**Signature in logcat:**
```
DGW: StreamGroupTransport[lightspeed] kill connection on ping timeout
_rt_mqtt_bridge_bootstrap_callback: Failed to boot stap bridged mqtt
  channel. RT error code:2002 token:6/-17
```

**Fix:** add gateway.facebook.com to TCP-passthrough so mitm doesn't
parse HTTP/2 on that host:

```bash
mitmdump --tcp-hosts 'gateway\.facebook\.com|edge-mqtt\.facebook\.com|mqtt-mini\.facebook\.com' ...
```

You lose ability to inspect the streaming content (it's FlatBuffer
binary anyway), but you keep one-shot `/messaging/lightspeed/request`
calls inspectable. That's where the actual MSYS task envelopes live —
useful for RE work.

### Combined command (the working baseline)

```bash
mitmdump \
  --set http3=false \
  --tcp-hosts 'mqtt-mini\.facebook\.com|edge-mqtt\.facebook\.com|gateway\.facebook\.com' \
  --listen-port 8080 \
  -w captures/mbs-debug.mitm \
  ...
```

---

## Phase 1 deliverable (updated)

Crossed-out items are done in mbs-native already; just need wiring to
Hermes:

- [ ] `proto/hermes/v1/mbs.proto` (extend with `ResolvePhone` + phone
      variant of `SendMessage` + asset fields on `BridgeLoginSuccess`)
- [ ] Postgres migrations:
  - [ ] `mbs/0001_init.sql` (sessions table — from May 12 doc)
  - [ ] `mbs/0002_assets.sql` (page_id/waba_id/wec_mailbox_id columns)
  - [ ] `mbs/0003_phone_threads.sql` (resolver cache)
  - [ ] `mbs/0004_totp_secret.sql` (encrypted column)
- [ ] `cmd/mbs/main.go` boilerplate (config, DB, NATS, gRPC server)
- [ ] `internal/mbs/handler/bridge_login.go` (streaming login;
      auto-inject TOTP code from `totp_secret_enc` when present)
- [ ] `internal/mbs/handler/send.go` (single send;
      auto-resolve phone via `graphql.Client.ResolvePhoneToThreadID`
      when `recipient.phone` is set)
- [ ] `internal/mbs/handler/resolve_phone.go` (standalone RPC)
- [ ] `internal/mbs/session/store.go` (sessions + phone_threads CRUD)
- [ ] `internal/mbs/session/manager.go` (in-memory client pool,
      advisory lock per uid)
- [ ] `internal/mbs/bridge/runner.go` (exec + stdin/stdout proxy +
      TOTP auto-fill)
- [ ] hermes-gateway: wire MBS endpoints into web API
- [ ] hermes-web: "Add MBS Session" modal + "Send to phone" send action
- [ ] Smoke test: web UI → login (TOTP) → resolve → send → message arrives
- [x] ~~Bridge binary~~ (`mbs-bridge-login` exists in `re/mbs/`)
- [x] ~~Native session library~~ (`mbs-native/auth`, `client`, `mbsnative`)
- [x] ~~Asset discovery~~ (lands in `creds.json` automatically)
- [x] ~~Phone resolver~~ (`graphql.ResolvePhoneToThreadID`)
- [x] ~~TOTP secret derivation~~ (`auth.SecretTOTPProvider`)

**Net: phase 1 is now ~70% library + ~30% gRPC translation, instead of
mid-50/50.** Most of the protocol work is done.

---

## Resolved questions from May 12 doc

These were "open" on May 12 and are now answered by the work shipped:

- **Q: How does the operator avoid typing thread_id manually?**
  A: `BizInboxWhatsAppCustomerMutation` resolves any E.164 phone to
  a `customer_id` in one round-trip. See Stage 3 above.

- **Q: How does the operator discover their own page_id and waba_id?**
  A: `BizAppBusinessScopingConfigQuery` enumerates all assets at
  login. Discovered values are persisted in creds. See Stage 2.

- **Q: Can we automate TOTP without manual code entry?**
  A: Yes — `SecretTOTPProvider` takes the base32 seed and derives
  codes on demand. The bridge subprocess can have TOTP codes injected
  programmatically without operator interaction. See Stage 1.

Still open from the May 12 doc:

1. Multi-tenant boundary (tenant vs user vs campaign)
2. Token rotation / refresh cadence
3. Encryption-at-rest decision (plain JSONB vs pgcrypto vs KMS)
4. Outbound rate limiting (share with hermes-campaign or separate?)
5. Page-token enumeration (cache or mint-on-demand?)
6. Web dashboard scope (internal-tool admin or tenant-self-serve?)
7. Connection-pool sizing (per-uid mutex policy)

These are still policy decisions, not technical blockers. Phase 1
implementation can proceed with reasonable defaults on all of them.

---

## Risk register (updated)

| Risk | Severity | Status / Mitigation |
|---|---|---|
| Meta closes the cross-stack pathway | High | Unchanged — monitor `pwd_key_fetch` + `send_login_request` shape. Native CAA wire path stays maintained as RE archive. |
| mautrix-meta upstream breaking changes | Medium | Unchanged — patched fork at `re/mbs/mautrix-meta-patched/`. Rebase quarterly. |
| Meta closes BizInboxWhatsAppCustomerMutation | Medium | NEW — Path C is a specific GraphQL mutation. If shape changes we need to re-RE. Catalog at `decoded/jadx/extracted/graphql_query_catalog.{json,csv}` (706 queries) helps find the replacement quickly. |
| Token revocation | Medium | Unchanged — bridge login is repeatable. |
| Device_id burn | Low-Medium | Unchanged — `--new-device-id` regenerates. |
| Bridge binary OOM / hang | Low | 60s timeout in `exec.Command`. |
| Same uid logging in twice | High | Postgres advisory lock per uid. |
| TOTP secret theft (NEW) | Medium | Encrypt at rest. Never log. Treat with the same posture as access_token. |
| Phone resolver mass-rate-limit (NEW) | Low-Medium | The mutation is per-page; rate limit at hermes-mbs (~10/min/page suggested baseline). Surface 429-equivalent errors to operator. |

---

## Files added/changed since May 12 (full inventory)

```
re/mbs/mbs-native/
├── auth/
│   ├── auth.go                              [edited: +5 fields on Creds]
│   ├── totp_secret.go                       [new]
│   └── totp_secret_test.go                  [new]
├── graphql/                                 [new package]
│   ├── client.go
│   ├── gzip.go
│   ├── scoping.go
│   ├── fetch_mailbox.go
│   ├── resolver.go
│   ├── wa_page_config.go
│   ├── client_test.go
│   ├── scoping_test.go
│   └── testdata/
│       ├── biz_app_business_scoping_config.req.urlenc
│       ├── biz_app_business_scoping_config.res.json
│       ├── biz_inbox_whatsapp_customer_mutation.{req.urlenc,res.json,meta.json}
│       └── fetch_page_mailbox_info.{req.urlenc,res.json,meta.json}
├── cmd/mbs-native/
│   ├── asset_discovery.go                   [new]
│   ├── send_to_phone.go                     [new]
│   ├── login.go                             [edited: --totp-secret, --no-asset-discovery]
│   └── main.go                              [edited: usage + creds-show with assets]
└── go.mod                                   [edited: +github.com/pquerna/otp]

decoded/jadx/extracted/
├── graphql_query_catalog.json               [706 queries]
└── graphql_query_catalog.csv

.hermes/plans/
├── 2026-05-26_path-c-phone-resolver.md
└── 2026-05-26_stable-login-to-message-pipeline.md
```

Test status: `go test ./...` green across all packages
(`auth`, `client`, `cmd/mbs-native`, `fb`, `graphql`, `mbsnative`,
`mqtt`, `thrift`, `transport`).

---

## Contact

Same as May 12: ping Oracle for live debugging. mbs-native CLI has
`--verbose` and `MBSNATIVE_WIRE_DUMP_DIR` for byte-level inspection;
the graphql package supports `MBSNATIVE_HOST_OVERRIDE` for hermetic
test-server routing.
