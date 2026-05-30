# MBS Tigon `0xfb1a` TLS Version — Future Reverse-Engineering Work

> **Status:** documented, not implemented
> **Owner:** Oracle (RE) + whoever picks up Stage F/G TLS surface
> **Triggered by:** 2026-05-30 campaign-send fb1a regression (root cause writeup below)
> **Severity if neglected:** medium-high — silent JA4 fingerprint drift, edge gating risk

---

## TL;DR

Meta's BizApp Android v551 advertises an extra version codepoint `0xfb1a` in the
TLS ClientHello's `supported_versions` extension, alongside the standard TLS 1.3
`0x0304`. When Meta's edge classifies a connection as "Tigon client" it
sometimes negotiates *back to* `0xfb1a` in the ServerHello. The wire format
that follows is byte-equivalent to TLS 1.3, but a stock TLS stack rejects the
ServerHello with "unsupported protocol version fb1a".

Our short-term fix (2026-05-30) is a one-line alias in our vendored utls fork:
treat `0xfb1a` as TLS 1.3 and let the rest of the state machine continue. That
works because we have *not* observed any wire-level deviation from RFC 8446 TLS
1.3 — handshake messages, key derivation, record framing, alerts, all
identical. `0xfb1a` is being used as a **signalling marker**, not a real
protocol.

That said, "we haven't observed a deviation yet" is not the same as "there
isn't one." This doc captures what we still don't know and the work to close
the gap.

---

## Current implementation (short-term Fix 1, shipped)

Two-line change:

1. **Vendored utls fork** at `re/mbs/mbs-native/third_party/utls/handshake_client.go`:

   ```go
   func (c *Conn) pickTLSVersion(serverHello *serverHelloMsg) error {
       peerVersion := serverHello.vers
       if serverHello.supportedVersion != 0 {
           peerVersion = serverHello.supportedVersion
       }
       // [UTLS PATCH: Meta Tigon 0xfb1a]
       const metaTigonV0 uint16 = 0xfb1a
       if peerVersion == metaTigonV0 {
           peerVersion = VersionTLS13
       }
       // [UTLS PATCH END]
       ...
   }
   ```

   Mirror patch in `handshake_client_tls13.go:188-197` for the
   `serverHello.supportedVersion != VersionTLS13` early-return path.

2. **Monorepo `go.mod`** replace pointing the build at the vendored fork:

   ```
   replace github.com/refraction-networking/utls => ./re/mbs/mbs-native/third_party/utls
   ```

   This replace MUST live in the main module's `go.mod`. Replace directives
   inside a *replaced* module (e.g. `re/mbs/mbs-native/go.mod`) are silently
   ignored by `go build` — that gotcha cost us a day of "but the CLI works?!"
   while production hermes-mbs ate upstream utls v1.8.2.

3. **Dockerfile** prime layer needs the new replace target's `go.mod`+`go.sum`
   copied before `go mod download`:

   ```
   COPY re/mbs/mbs-native/third_party/utls/go.mod \
        re/mbs/mbs-native/third_party/utls/go.sum \
        re/mbs/mbs-native/third_party/utls/
   ```

### What this fix gets right

- Restores end-to-end TLS handshake against `graph.facebook.com` for the
  hermes-mbs production binary.
- Keeps the `0xfb1a` codepoint on the wire in our ClientHello, so JA4 stays
  byte-perfect vs real BizApp.
- Zero functional risk: the alias only affects how *we* interpret the server's
  version pick. Wire bytes are unchanged.

### What this fix does NOT solve

- We're guessing that `0xfb1a` is purely a marker. If Meta ever changes the
  meaning (a real protocol extension, an experiment ID with side effects), we
  fail silently — handshake completes, but downstream signalling diverges.
- We don't know *when* Meta picks `0xfb1a` vs plain `0x0304`. Empirically:
  - Cold connections from non-reputational IPs → `0x0304`
  - Long-lived connections from IPs with Tigon-fingerprinted history → `0xfb1a`
- If Meta starts gating on "you said fb1a but didn't do X after" we won't
  notice until sends start failing.

---

## What we know about `0xfb1a` (evidence)

**Source:** `re/mbs/android-mbs/captures/ja4/bizapp_clienthello.bin` plus
inline notes in `re/mbs/mbs-native/transport/tigon_clienthello.go`.

| Field | BizApp v551 wire bytes | RFC 8446 TLS 1.3 |
|---|---|---|
| `supported_versions` extension (0x002b) | `04 03 04 fb 1a` (len=4, list = [0x0304, 0xfb1a]) | `02 03 04` (len=2, list = [0x0304]) |
| ServerHello selected version | sometimes `0xfb1a`, sometimes `0x0304` | always `0x0304` |
| Crypto derivation after ServerHello | observed: identical to RFC 8446 | RFC 8446 |
| Record-layer framing | observed: identical | RFC 8446 |
| Alert codes | observed: identical | RFC 8446 |

**Captured TLS 1.3 handshake messages that followed a `0xfb1a` ServerHello**
(per our own probes and partial pcaps from `re/mbs/captures/probe24-fullpcap/`):

- `EncryptedExtensions` — present, normal contents
- `Certificate` — normal cert chain, server cert is a regular Facebook leaf
- `CertificateVerify` — normal ECDSA-SHA384
- `Finished` — normal HMAC
- `NewSessionTicket` — normal, ticket can be resumed

So far it really does look like a vanity codepoint.

---

## What we do NOT know (and should)

### Q1 — Does the server gate handshake completion on the client *acknowledging* `0xfb1a` somehow?

Right now we silently alias it to TLS 1.3 and proceed. If Meta has logic like
"if client offered fb1a AND server picked fb1a, then expect <extra
EncryptedExtensions field>" we'd miss it.

**How to investigate:**
- Run side-by-side captures: real BizApp v551 on a flagged-Tigon IP vs our
  client on the same IP.
- Compare every byte of `EncryptedExtensions`, `Certificate`,
  `CertificateVerify`, and the first 5 application records.
- Diff `NewSessionTicket` payloads.
- Tools: Frida hook BoringSSL's `SSL_do_handshake` on Android side
  (see `re/mbs/android-mbs/scripts/`), Wireshark with the captured
  `(client_random, master_secret)` keylog file on our side.

### Q2 — When does the server pick `0xfb1a` vs `0x0304`?

Hypotheses, ordered by likelihood:

1. **Source-IP reputation** (most likely). Meta's edge tags an IP after it
   sees ≥N successful Tigon-fingerprinted handshakes → subsequent connections
   from that IP get `0xfb1a` ServerHellos.
2. **JA4 prefix match.** Server side-table of "if JA4 starts with `t13d0308h2_55b375c5d22e`, return fb1a".
3. **Region/POP-specific.** Some edge POPs have the experiment enabled, others
   don't. Production hermes-mbs hit POP `157.240.208.16` (Singapore-ish);
   cold-test from a different POP might never see fb1a.
4. **Time-of-day / experiment cohort.** Meta has many A/B knobs; this could be
   one.

**How to investigate:**
- Probe `graph.facebook.com` from a matrix of {fresh AWS EC2, OpenVPN IPs,
  residential proxy, our prod host} with identical ClientHello bytes. Log
  ServerHello version picks. Pattern-match.
- Probe across all `c10r.facebook.com` A records (it's a CDN cluster — ~30
  IPs). Log per-POP behavior.
- If a recognizable pattern emerges, document it in the runbook so ops can
  predict when the edge will negotiate fb1a.

### Q3 — Are there other `fb*` codepoints?

`0xfb1a` looks like a Meta-private allocation within the experimental block
`0xfb00..0xfbff`. If they have `fb1a`, they might add `fb1b`, `fb1c`, etc.

**How to investigate:**
- Audit `re/mbs/captures/` for any other unrecognized version codepoints.
- Watch BizApp Android updates — diff the `supported_versions` extension after
  each major version bump.
- Build a per-handshake telemetry hook in our utls fork that logs any
  unrecognized peerVersion at WARN level (not ERROR, since we want to keep
  shipping).

### Q4 — Is `0xfb1a` correlated with `h2-fb` ALPN selection?

Both are Meta-private signals. If the server picks fb1a in TLS, does it also
pick h2-fb in ALPN? Our captures so far show:

- ALPN selected: `h2` (standard HTTP/2) — never seen `h2-fb` picked back.

But this could be the same gating logic as Q2 (we haven't been on
"BizApp-reputation" IPs long enough to see h2-fb negotiated).

If `h2-fb` ever IS selected, our okhttp_h2_transport will accept it (we treat
unknown ALPN as h2-compatible), but we might be missing Meta's Tigon-specific
HTTP/2 framing rules. Real RE work.

---

## Two real fixes (long-term, ordered by effort)

### Fix A — Document `0xfb1a` semantics, keep the alias (smallest viable)

1. Spend 1-2 days on Q1 (does the server gate anything on fb1a awareness).
2. If no behavioral divergence found, leave the alias in place permanently.
3. Add a WARN-level log in `pickTLSVersion` when we alias fb1a, so we can
   correlate alias frequency with send success rate. If we ever see "100%
   aliased → send fails go up" we know fb1a *meant* something.

Estimated effort: 2-3 days RE + 1 day implementation/observability.

### Fix B — Implement `0xfb1a` as a first-class TLS 1.3-compatible version

If Q1 reveals behavioral divergence (the server expects us to do something
fb1a-specific in the post-handshake state machine), we need a real
implementation:

1. Capture a full `0xfb1a` handshake from a real Android BizApp v551 device on
   a flagged IP. Use a tap on the device (Frida pinning bypass + mitmproxy in
   transparent mode, or a custom router-side `tcpdump` if you have the
   server's session key from BoringSSL hook).
2. Diff every byte of every TLS message against the `0x0304` baseline.
3. For each divergence, decide:
   - Is it just an ID change? → trivial fix in utls
   - Is it a new message field? → real protocol work
   - Is it a different key schedule? → reimplement TLS 1.3 derivation under
     fb1a
4. Test against `graph.facebook.com` and `mqtt-mini.facebook.com` separately —
   they may have different edge logic.

Estimated effort: 1-2 weeks RE + 1 week implementation + 1 week test
infrastructure.

### Fix C — Drop `0xfb1a` from our ClientHello (escape hatch only)

Edit `transport/tigon_clienthello.go:91`:

```go
// before: Data: []byte{0x04, 0x03, 0x04, 0xfb, 0x1a},
// after:  Data: []byte{0x02, 0x03, 0x04},
```

This degrades JA4 fidelity by one extension byte but works against any TLS 1.3
server. Use only if Fix A/B prove untenable. Risk: Meta's edge may have
already moved on to gating on JA4_r (raw extension ordering and content), in
which case dropping fb1a flags us as "fake Tigon" and we get edge-banned.

---

## How to reproduce the original bug (regression test recipe)

If someone "improves" the utls dependency in the future and accidentally drops
the patch:

```bash
# In hermes monorepo
cd /Users/env/Projects/hermes

# Bypass the replace temporarily
sed -i.bak '/refraction-networking\/utls => \.\/re/d' go.mod
go mod tidy

# Build + deploy mbs
docker-compose -f docker-compose.prod.yml --env-file .env.prod build mbs
docker-compose -f docker-compose.prod.yml --env-file .env.prod up -d --no-deps mbs

# Trigger a campaign send via the NATS task publisher
# (see scripts/dev/pub_one_mbs.go — TODO: extract from /tmp/pub_one.go)

# Watch logs for: "utls handshake to graph.facebook.com: tls: server selected
# unsupported protocol version fb1a"
docker logs hermes-mbs-1 --since 30s | grep fb1a

# Should see the error within seconds. Restore via:
mv go.mod.bak go.mod && go mod tidy
```

A proper regression test would be:

```go
// in re/mbs/mbs-native/transport/tigon_clienthello_test.go (new test)
func TestTigonV551_FB1AAliasIntegration(t *testing.T) {
    // Spin up an httptest.Server that picks 0xfb1a in supportedVersions,
    // then assert our client completes the handshake.
    // Needs a custom listener that pokes the version field directly.
}
```

That requires a TLS 1.3 server that responds with a tampered supported_versions
ext — feasible with a custom net.Listener wrapping a hand-rolled TLS 1.3
implementation. Not yet built.

---

## Related artifacts in the repo

- `re/mbs/mbs-native/transport/tigon_clienthello.go` — ClientHello spec
- `re/mbs/mbs-native/third_party/utls/handshake_client.go:559-590` — pickTLSVersion patch
- `re/mbs/mbs-native/third_party/utls/handshake_client_tls13.go:188-197` — mirror patch
- `re/mbs/android-mbs/captures/ja4/ja4_diff.md` — full JA4 side-by-side
- `re/mbs/android-mbs/captures/ja4/bizapp_clienthello.bin` — reference wire bytes
- `re/mbs/captures/probe24-fullpcap/` — full pcap of a real BizApp session
- `go.mod` lines 84-87 — the monorepo replace directive
- `Dockerfile` lines 40-46 — prime layer including utls fork

---

## Decision log

**2026-05-30:** Shipped Fix-1 (alias 0xfb1a → TLS1.3 in vendored fork +
monorepo go.mod replace). Production hermes-mbs handshake healed. TLS surface
no longer the bottleneck. Filed this doc as the future-work tracker.

Open question for the next RE session: Q1 (does fb1a have semantic meaning) is
the highest-priority unknown. Recommend tackling that before Fix B.
