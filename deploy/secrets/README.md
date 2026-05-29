# Hermes Secrets Directory

This directory holds secret material consumed by the Hermes services at
runtime. Real secrets are **gitignored**; only `*.example` placeholder
files are committed.

## Layout

```
deploy/secrets/
├── README.md                 ← this file
├── dev/                      ← dev-only secrets
│   ├── .gitignore            ← keeps real secrets out of git
│   ├── mbs-dek.bin.example   ← committed placeholder (64 ASCII '0' hex chars + \n)
│   └── mbs-dek.bin           ← gitignored, generated per-developer
└── prod/                     ← prod secrets (added in Stage F chunk 3)
```

## What lives here

### `mbs-dek.bin` — hermes-mbs Data Encryption Key

64 hex characters (32 bytes of cryptographic randomness, hex-encoded) +
trailing newline. Wraps every encrypted field in the `mbs_sessions`
table using the chunk-1 AAD format `mbs.<column>.uid=<uid>`. See
`pkg/crypto.LoadDEKFromFile` (the loader trims trailing whitespace then
hex-decodes) and `internal/mbs/config/config.go::HERMES_MBS_DEK_FILE`.

**Why hex instead of raw 32 bytes?** The loader contract is hex — it
matches `openssl rand -hex 32`, survives accidental `cat` of the secret
without terminal corruption, and is the same format `HERMES_MBS_DEK_HEX`
takes for env-var-based loading.

**Generate (dev):**

```sh
./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin
```

Output: 65 bytes total (64 hex chars + newline), file mode `0400`. The
generator refuses to overwrite an existing file — rotation is an
explicit follow-up procedure (`docs/runbooks/secret-management.md`,
ships in chunk 3).

**Mount in compose:** the dev compose file declares a top-level
`secrets:` block:

```yaml
secrets:
  mbs_dek:
    file: ./deploy/secrets/dev/mbs-dek.bin
```

and the `mbs` service consumes it:

```yaml
services:
  mbs:
    secrets:
      - source: mbs_dek
        target: mbs_dek         # → /run/secrets/mbs_dek
        mode: 0400
    environment:
      HERMES_MBS_DEK_FILE: /run/secrets/mbs_dek
```

The container reads from `/run/secrets/mbs_dek` (Docker tmpfs mount —
not visible via `docker inspect` as a bind path, not persisted in the
image layer).

## Conventions

- One file per secret. No multi-secret JSON blobs.
- Real files end in `.bin`, `.hex`, `.key`, `.pem`, `.token` or live in
  `.env` files. `dev/.gitignore` rejects all of these.
- Committed placeholders end in `.example` and are safe to look at.
- Permissions: `chmod 400` on the host file. Compose mounts read-only.
- Do **not** commit a `.env` file containing real secrets — the
  prod-equivalent is `.env.prod.example` (chunk 3) which carries
  placeholders only.

## Pitfalls

- **Compose `secrets.file` is a host path**, not a Docker secret name in
  the registry sense. The container always sees `/run/secrets/<target>`.
- **The `.example` file is 64 ASCII '0' chars + newline (65 bytes).**
  It hex-decodes to a 32-byte all-zero key and must never be used as a
  real DEK; the service does not check for all-zero input (encryption
  silently "succeeds" with a zero key but every ciphertext becomes
  trivially decryptable). The `.gitignore` rule (`!*.example`) is the
  only guardrail — name your real file without the `.example` suffix.
- **Two dev clones on one host with the same `POD_ID`** will fight over
  pod-claim rows in `mbs_sessions`. Override `POD_ID` per shell if you
  run parallel stacks.
- **Rotation requires a re-encrypt sweep.** `MBS_ENCRYPT_REWRITE_ON_STARTUP=true`
  re-keys existing rows once on next boot; flip it off again after one
  successful run. See chunk 3's runbook.

## Stage F chunk roadmap (this layout's history)

- **chunk 1** (this commit): dev secrets directory + DEK file + README +
  compose wiring.
- **chunk 3**: `prod/` subdirectory + JWT signing key + PG password file
  + NATS creds + `BIZAPP_CLIENT_TOKEN`. `.env.prod.example` for everything
  non-secret. `docs/runbooks/secret-management.md` covers rotation.
- **future**: external secret store (Vault / SOPS / SealedSecrets) is a
  separate stage.
