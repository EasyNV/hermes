# Hermes Build / Audit Status

This document captures the latest audited state of the codebase. It is not a guarantee that every known issue is fixed.

Scope of the latest audit/status pass:

- Included: `/Users/env/Projects/hermes` non-`re/**` code and docs.
- Excluded: `/Users/env/Projects/hermes/re/**`.
- No source code changes were made during the audit phase.
- Temporary frontend build output was written to `/tmp/hermes-web-audit-dist`.
- Secrets policy: credentials/tokens/passwords/connection strings are redacted as `[REDACTED]`.

## Current codebase shape

Primary services:

- `gateway`
- `proxy`
- `contacts`
- `notify`
- `wa`
- `mbs`
- `campaign`
- `inbox`

Operator/import tool:

- `mbs-import`

Protobuf services:

- `HermesGateway` — 75 RPCs.
- `HermesMbs` — 9 RPCs.
- `HermesCampaign` — 17 RPCs.
- `HermesInbox` — 14 RPCs.
- `HermesContacts` — 11 RPCs.
- `HermesProxy` — 11 RPCs.
- `HermesWa` — 8 RPCs.
- `HermesNotify` — 6 RPCs.

REST/WebSocket adapter:

- 89 mounted routes total.
- Includes `/ws/mbs/bridge-login` for MBS bridge login.

Third-party local replacements:

- `third_party/mbs-native`
- `third_party/mautrix-meta-patched`
- `third_party/mbs-native/third_party/utls`

## Latest quality gates

- `go test -count=1` over non-`re` Go packages: **passed**.
  - Package count observed: 50.
- `go test -race -count=1` over non-`re` Go packages: **passed**.
  - Package count observed: 50.
- `go build ./cmd/...` over non-`re` command packages: **passed**.
  - Command package count observed: 9.
- `gofmt -l` over tracked/untracked non-`re` Go files: **passed** / no output.
- `npx --no-install tsc --noEmit` in `web`: **passed**.
- Vite production build in `web`: **passed** when rerun in background.
  - Output target: `/tmp/hermes-web-audit-dist`.
  - Warning: main JS chunk around `680 kB`, above Vite's default `500 kB` threshold.
- `npm audit --omit=dev --audit-level=moderate --json` in `web`: **passed** / exit `0`.
- Full `npm audit --audit-level=moderate --json` in `web`: **failed** / exit `1`.
  - Known dev dependency advisories included `vite` and `postcss`.
- `go vet`: **failed**.
  - Location: `internal/mbs/session/listener_hook_test.go`.
  - Cause: range variable copies a value containing `sync/atomic.Int64` / `sync/atomic.noCopy`.
- `go mod verify`: **failed**.
  - Module: `mbs-native v0.0.0-00010101000000-000000000000`.
  - Cause: missing ziphash/hash metadata for local/replaced module state.
- `buf lint`: **not run**.
  - Cause: `buf` was missing in the audit environment.

## Tooling availability observed

Checked tools included:

- `go`
- `gofmt`
- `npm`
- `npx`
- `node`
- `python3`
- `buf`
- `gitleaks`
- `trufflehog`
- `semgrep`
- `gosec`
- `govulncheck`
- `staticcheck`
- `golangci-lint`
- `eslint`

Known observations:

- `tsc` was available at version `5.9.3`.
- `buf` was unavailable.
- Earlier checks showed `gosec`, `govulncheck`, `staticcheck`, `golangci-lint`, and `eslint` unavailable/blank in the audit environment.

## Security-oriented review areas inspected

Gateway/auth/RBAC:

- `cmd/gateway/main.go`
- `internal/gateway/config/config.go`
- `internal/gateway/rest/rest.go`
- `internal/gateway/middleware/auth.go`
- `internal/gateway/middleware/rbac.go`
- `internal/gateway/handler/auth.go`
- `internal/gateway/handler/handler.go`
- `internal/gateway/handler/store.go`

Gateway REST/MBS/WebSocket:

- `internal/gateway/rest/handlers.go`
- `internal/gateway/rest/handlers_mbs.go`
- `internal/gateway/rest/mbs_bridge_ws.go`
- `internal/gateway/websocket/hub.go`
- `internal/gateway/websocket/events.go`
- `internal/gateway/handler/mbs.go`

MBS backend:

- `internal/mbs/handler/rpc_bridge_login.go`
- `internal/mbs/handler/rpc_session_lifecycle.go`
- `internal/mbs/handler/rpc_resolve_phone.go`
- `internal/mbs/handler/rpc_send_message.go`
- `internal/mbs/handler/tenant.go`
- `cmd/mbs/send_consumers.go`
- `cmd/mbs/nats_streams.go`

Campaign/inbox/contacts/frontend/config areas were also reviewed for tenant scoping, auth flow, WebSocket behavior, token handling, compose/env posture, and security headers.

## Current findings / watch items

### High / needs confirmation: MBS bridge envelope persistence classification

A prior audit observed that MBS bridge envelope material is marshaled and encrypted for cookie/blob storage, but a similarly named bridge envelope field appeared to be stored/commented as plaintext metadata in `internal/mbs/handler/rpc_bridge_login.go` around the persistence path.

Action: verify whether every secret-bearing field in bridge envelopes, cookies, credentials, TOTP secrets, machine/device identifiers, and access/session tokens is encrypted at rest and redacted from logs. Treat “metadata” labels as untrusted until confirmed.

### Medium: `go vet` atomic copy issue

`internal/mbs/session/listener_hook_test.go` copies a value containing `sync/atomic.Int64`. Fix the test iteration pattern so range variables do not copy atomic/noCopy state.

### Medium: frontend dev dependency advisories

Full `npm audit` including dev dependencies fails due to dev advisories (`vite`, `postcss`). Production-only audit passed.

### Medium: local module verification drift

`go mod verify` fails for the local/replaced `mbs-native` module state. The repo intentionally uses local replacements, but the verification gate should either be normalized or explicitly documented as an expected exception.

### Low: protobuf lint tooling unavailable

Install `buf` and re-run `buf lint` / `make proto-gen` validation.

### Low: Vite chunk-size warning

Production frontend build passes but emits a main chunk warning around `680 kB`. Consider code splitting if user-perceived startup performance matters.

## Current remediation priorities

1. Confirm and harden MBS bridge/session material encryption/redaction.
2. Enforce REST RBAC parity with gRPC RBAC.
3. Verify tenant/workspace/session object-level authorization in gateway, store, MBS, campaign, and inbox paths.
4. Restrict production CORS/origin posture.
5. Harden WebSocket token handling and query-string log scrubbing.
6. Validate MBS multi-page routing fields such as `page_id_override` against session-owned assets.
7. Cross-check NATS tenant subject suffixes against payload/session tenant where feasible.
8. Clarify campaign status semantics: enqueue/send attempt vs confirmed delivery.
9. Fix `go vet` atomic copy failure.
10. Normalize or document the `go mod verify` behavior for local replacements.
11. Upgrade/audit frontend dev dependencies.
12. Install `buf` and restore protobuf lint coverage.

## Recommended release-gate command set

From repo root, excluding `re/**` where relevant:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go build ./cmd/...
gofmt -l .
go vet ./...
go mod verify
```

Frontend:

```bash
cd web
npx --no-install tsc --noEmit
npm run build
npm audit --omit=dev --audit-level=moderate
npm audit --audit-level=moderate
```

Proto/tooling:

```bash
make tools
buf lint
make proto-gen
```

Security scanners, when installed:

```bash
gitleaks detect --redact
trufflehog filesystem --no-update .
semgrep scan --config auto
```
