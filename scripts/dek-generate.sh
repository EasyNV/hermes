#!/usr/bin/env bash
# scripts/dek-generate.sh — generate a hex-encoded 32-byte DEK at $1.
#
# Usage: ./scripts/dek-generate.sh deploy/secrets/dev/mbs-dek.bin
#
# Writes 64 hex chars (32 random bytes, hex-encoded) followed by a newline
# and sets the file mode to 0400 so only the owner can read it. The file
# format matches `pkg/crypto.LoadDEKFromFile` — it trims trailing whitespace
# and decodes the hex content into a 32-byte AES-256 key.
#
# Why hex instead of raw bytes? The loader contract is hex (see
# pkg/crypto/crypto.go::LoadDEKFromFile + ErrInvalidKey). Hex files are
# also pasteable and survive a misconfigured `cat` of the secret without
# corruption, which matters for the dev workflow.
#
# Refuses to overwrite an existing file — rotation is an explicit follow-up
# via the secret-management runbook.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <path>" >&2
  exit 64
fi

target="$1"

if [[ -e "$target" ]]; then
  echo "refusing to overwrite existing file: $target" >&2
  echo "to rotate, see docs/runbooks/secret-management.md" >&2
  exit 1
fi

mkdir -p "$(dirname "$target")"

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl not found in PATH" >&2
  exit 127
fi

# openssl rand -hex emits 64 hex chars; some builds append a newline,
# others do not. Strip any trailing whitespace and re-add exactly one
# newline so the file is deterministically 65 bytes long.
hex_value=$(openssl rand -hex 32 | tr -d '[:space:]')
printf '%s\n' "$hex_value" > "$target"
chmod 400 "$target"

written=$(wc -c < "$target" | tr -d '[:space:]')
# 64 hex chars + 1 newline = 65 bytes.
if [[ "$written" != "65" ]]; then
  echo "wrote $written bytes; expected 65 (64 hex chars + newline)" >&2
  rm -f "$target"
  exit 1
fi

# Sanity: verify the file contains only hex chars (plus the trailing newline).
hex_only=$(head -c 64 "$target")
if ! [[ "$hex_only" =~ ^[0-9a-fA-F]{64}$ ]]; then
  echo "file does not contain 64 hex chars" >&2
  rm -f "$target"
  exit 1
fi

echo "wrote 64-char hex DEK to $target (0400, 32 bytes of entropy)"
