package config

import (
	"os"
	"strings"
)

// LoadSecret resolves a secret value from one of two env-var sources, in
// priority order:
//
//  1. envName       — the value itself, e.g. JWT_SECRET=hunter2
//  2. fileEnvName   — a path to a file whose contents are the value,
//                     e.g. JWT_SECRET_FILE=/run/secrets/jwt_signing_key
//
// Behaviour:
//
//   - If envName is set to a non-empty value, that value is returned verbatim
//     (no trimming). Inline secrets are the simpler dev posture.
//   - Otherwise, if fileEnvName points at a readable file, the file's contents
//     (with one trailing newline trimmed, if present) are returned.
//   - If neither is set, or if the file read fails, the empty string is
//     returned and the boolean is false. Callers decide whether empty is a
//     fatal misconfiguration (gateway treats an empty JWT secret as fatal at
//     boot; chunk 3 leaves that policy with the caller).
//
// The newline-trim makes the helper play nicely with files written by
// `scripts/dek-generate.sh` and any `openssl rand -hex 32 > file` workflow,
// which append exactly one '\n'. We only strip a single trailing newline,
// not all whitespace, so binary-shaped secrets with intentional trailing
// content survive.
//
// The file is read with the calling process's user; production compose
// mounts file-based Docker secrets with uid/gid:65532 mode 0400, which
// matches the non-root user the chunk-2 Dockerfile bakes in.
//
// Errors are intentionally swallowed: the caller only learns "got a
// secret" or "got nothing." Operational errors (file unreadable due to
// permission drift) surface to logs through the caller's own boot-time
// fatal-if-empty check.
func LoadSecret(envName, fileEnvName string) (string, bool) {
	if v := os.Getenv(envName); v != "" {
		return v, true
	}
	if fileEnvName == "" {
		return "", false
	}
	path := os.Getenv(fileEnvName)
	if path == "" {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := string(b)
	// Strip exactly one trailing newline (matching POSIX text-file convention
	// and `scripts/dek-generate.sh`'s output). Do NOT use TrimSpace — that
	// would corrupt deliberately-trailing-space secrets, however unlikely.
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return "", false
	}
	return s, true
}
