# Hermes MBS Bootstrap Runbook

**Audience:** Operators bringing up a fresh Hermes deployment and enrolling the first Meta Business Suite account.

**Scope:** Secret provisioning → stack boot → tenant/admin setup → browser login → first MBS bridge login.

This runbook reflects the current in-stack bridge flow: email/password plus optional TOTP secret through the frontend dialog and `/ws/mbs/bridge-login`. The older cookie-blob paste flow is no longer the operator path; cookies/bridge envelopes are now internal bridge/native material and must be treated as secret-bearing data.

Do not paste real passwords, cookies, JWTs, TOTP secrets, bridge envelopes, or access tokens into docs, tickets, screenshots, or chat. Redact as `[REDACTED]`.

## 0. Prerequisites

- Docker + Compose available.
- Repo cloned with submodules initialized.
- Domain/reverse proxy available for production HTTPS.
- Meta Business Suite account with WhatsApp Business messaging enabled.
- Access to the account's email/phone, password, and 2FA method if configured.

```bash
git submodule update --init --recursive
```

## 1. Provision secrets

### Development

```bash
./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin
```

### Production

```bash
mkdir -p deploy/secrets/prod
./scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin
./scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key
printf '%s' '[REDACTED_STRONG_POSTGRES_PASSWORD]' > deploy/secrets/prod/postgres-password
chmod 0400 deploy/secrets/prod/*
```

`dek-generate.sh` writes 64 hex chars plus one trailing newline (65-byte file, 32 bytes entropy).

Back up `deploy/secrets/prod/mbs-dek.bin` immediately. Losing the MBS DEK makes encrypted MBS session material unrecoverable.

## 2. Boot the stack

Development:

```bash
make deploy-dev-up
make deploy-dev-ps
```

Production:

```bash
cp .env.prod.example .env.prod
# edit .env.prod locally; keep secrets redacted in shared materials
make docker-build-all
make deploy-prod-up
make deploy-prod-ps
```

Expected stack shape:

- Infrastructure: `postgres`, `redis`, `nats`.
- Init: `migrate` exits successfully.
- Backends: `proxy`, `contacts`, `notify`, `wa`, `campaign`, `inbox`, `mbs`, `gateway` healthy.
- Frontend: `web` running.

## 3. Create or rotate the first admin

The gateway migrations include a development seed superadmin. Treat seeded credentials as bootstrap-only and rotate/remove them immediately in any environment that matters.

Recommended production path:

1. Create a tenant.
2. Create a workspace under that tenant.
3. Create a fresh admin user with a strong password.
4. Bind the admin to the workspace.
5. Delete or disable bootstrap credentials.

When documenting commands or tickets, replace real passwords/hashes with `[REDACTED]`.

Example login request shape:

```bash
curl -X POST http://localhost:8081/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"operator@example.com","password":"[REDACTED]"}'
```

Expected response contains redacted access/refresh tokens:

```json
{
  "accessToken": "[REDACTED]",
  "refreshToken": "[REDACTED]"
}
```

## 4. Verify service health before bridge login

```bash
curl -fsS http://localhost:8081/healthz
curl -fsS http://localhost:9092/livez
curl -fsS http://localhost:9092/readyz
```

Confirm gateway can reach MBS in dev compose:

```bash
docker-compose -f docker-compose.dev.yml exec gateway nc -zv mbs 8082
```

Check MBS DEK mount:

```bash
docker-compose -f docker-compose.dev.yml exec mbs sh -c 'wc -c /run/secrets/mbs_dek'
# expected: 65 (64 hex chars + trailing newline)
```

## 5. First browser session

Development:

- Open `http://localhost:5173`.
- Log in with the admin account you created/rotated.

Production:

- Open `https://<HERMES_DOMAIN>` through the reverse proxy.
- Confirm `/api/v1/*` and WebSocket upgrades proxy to gateway `:8081`.

## 6. First MBS bridge login

### Flow overview

```text
Browser BridgeLoginDialog
  └─► /ws/mbs/bridge-login?token=<jwt>
       └─► gateway validates JWT and forces tenant from claims
            └─► HermesMbs.BridgeLogin bidi stream
                 └─► patched mautrix-meta bridge driver
                      └─► mbs-native credential/session materialization
                           └─► encrypted DB persistence + asset discovery
```

### Operator procedure

1. In the Hermes UI, open the MBS sessions page.
2. Click **New Bridge Login**.
3. Enter the Meta Business Suite email/phone.
4. Enter the password.
5. Optional: enter the base32 TOTP secret if the account has TOTP and you want Hermes to auto-generate the code.
6. Submit.
7. If Hermes surfaces a prompt, answer it in the dialog. Typical prompt: `totp_code`.
8. Wait for success. The UI should show the new session UID/display name and refresh the sessions list.

### Current bridge-login states

Frontend phases:

- `idle`
- `connecting`
- `progress`
- `prompt`
- `success`
- `failure`
- `error`

Backend progress stages include:

- `BRIDGE_STAGE_CALLING_CAA`
- `BRIDGE_STAGE_AWAITING_2FA`
- `BRIDGE_STAGE_PREFLIGHT`
- `BRIDGE_STAGE_DISCOVERING_ASSETS`
- `BRIDGE_STAGE_PERSISTING`

Terminal failure codes include:

- `BRIDGE_ERR_INVALID_CREDS`
- `BRIDGE_ERR_2FA_REQUIRED`
- `BRIDGE_ERR_2FA_WRONG_CODE`
- `BRIDGE_ERR_CHECKPOINT`
- `BRIDGE_ERR_PREFLIGHT_RC19`
- `BRIDGE_ERR_PREFLIGHT_RC4`
- `BRIDGE_ERR_NETWORK`
- `BRIDGE_ERR_BRIDGE_SUBPROCESS`
- `BRIDGE_ERR_INTERNAL`

## 7. Verify session and assets

List sessions through the UI or API:

```bash
curl -fsS http://localhost:8081/api/v1/mbs-sessions \
  -H 'Authorization: Bearer [REDACTED]'
```

Fetch assets for the new UID:

```bash
curl -fsS http://localhost:8081/api/v1/mbs-sessions/<uid>/assets \
  -H 'Authorization: Bearer [REDACTED]'
```

Expected asset fields may include:

- `pageId`
- `pageName`
- `wabaId`
- `wecMailboxId`
- `wecPhoneNumber`
- `businessPresenceNodeId`
- `businessId`
- `businessName`
- `isPrimary`
- `wecAccountRegistered`

## 8. Resolve a phone and send a test message

Resolve phone:

```bash
curl -fsS -X POST http://localhost:8081/api/v1/mbs-sessions/<uid>/resolve-phone \
  -H 'Authorization: Bearer [REDACTED]' \
  -H 'Content-Type: application/json' \
  -d '{"phone":"[REDACTED_PHONE]"}'
```

Send message by phone:

```bash
curl -fsS -X POST http://localhost:8081/api/v1/mbs-sessions/<uid>/messages \
  -H 'Authorization: Bearer [REDACTED]' \
  -H 'Content-Type: application/json' \
  -d '{"phone":"[REDACTED_PHONE]","text":"Test from Hermes"}'
```

Send message by thread ID:

```bash
curl -fsS -X POST http://localhost:8081/api/v1/mbs-sessions/<uid>/messages \
  -H 'Authorization: Bearer [REDACTED]' \
  -H 'Content-Type: application/json' \
  -d '{"threadId":"[REDACTED_THREAD_ID]","text":"Test from Hermes"}'
```

For accounts with multiple pages/WABA assets, `pageIdOverride` may be supported. Treat it as authorization-sensitive and only use a page ID returned from that session's assets endpoint.

## 9. Campaign MBS path smoke check

MBS campaign sends publish work to:

```text
hermes.mbs.send.campaign.<tenant_id>
```

The MBS service consumes with durable `mbs-campaign-send`, injects tenant from the subject suffix, and calls the same `SendMessage` handler used by direct sends.

Manual send work uses:

```text
hermes.mbs.send.manual.<tenant_id>
```

Review campaign progress semantics before relying on status as confirmed delivery. Enqueue/send attempt status is not the same as downstream recipient delivery confirmation.

## 10. Troubleshooting

### WebSocket fails immediately

Check:

- Browser is logged in and has an access token.
- Reverse proxy supports WebSocket upgrade for `/ws/mbs/bridge-login`.
- Gateway has `MBS_ADDR=mbs:8082`.
- MBS service is healthy.
- Query strings containing JWTs are scrubbed from proxy logs.

### Bridge login returns checkpoint

Meta may require manual verification in a browser/app. Complete the checkpoint outside Hermes, then retry. Do not paste checkpoint screenshots containing account/session data into shared channels.

### TOTP fails

Check whether the account uses TOTP vs another 2FA method. If using TOTP, verify the base32 secret locally. If omitted or invalid, Hermes should surface a prompt so the current code can be entered manually.

### MBS preflight fails with RC19 / RC4

- RC19 usually indicates device/risk classification issues; retry with a fresh device identity if exposed in operator tooling.
- RC4 usually indicates token rejection/credential invalidity.

### Assets list is empty

Check that the Meta account has Business Suite access to a WhatsApp Business enabled page/WABA. MBS native asset discovery uses Business Suite/bootstrap data and can only expose assets the account can see.

### Session sends fail

Check:

- Session state is `ACTIVE`.
- Session is not burned/checkpointed.
- Recipient phone resolves to a thread.
- `pageIdOverride`, if used, belongs to the session assets.
- NATS `HERMES_MBS_SEND` stream exists for queued sends.
- MBS logs classify the error as permanent vs transient.

## 11. Secret handling rules

Never log or commit:

- Meta passwords.
- TOTP secrets or codes.
- Cookies.
- JWTs.
- Bridge envelopes.
- Access tokens.
- Session keys/secrets.
- Machine/device identifiers tied to live accounts.
- Connection strings with passwords.

Use `[REDACTED]` in all shared examples.
