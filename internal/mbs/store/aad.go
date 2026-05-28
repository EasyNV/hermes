package store

import (
	"fmt"
	"strconv"
)

// AADColumn identifies which encrypted column the AAD binds to. The
// crypto package binds AAD bytes into the AES-GCM authenticator, so a
// ciphertext encrypted with AAD "mbs.access_token.uid=N" cannot be
// decrypted as "mbs.secret.uid=N" — column/uid swap attacks fail by
// construction.
type AADColumn string

const (
	AADAccessToken AADColumn = "access_token"
	AADSecret      AADColumn = "secret"
	AADSessionKey  AADColumn = "session_key"
	AADCookies     AADColumn = "cookies"
	AADTOTPSecret  AADColumn = "totp_secret"
)

// BuildAAD constructs the AAD bytes for one encrypted column on one uid.
// Format: "mbs.<column>.uid=<uid>"
//
// This is the ONLY function that constructs AAD strings in hermes-mbs.
// Callers thread (column, uid) through; the store handles the rest. Any
// drift between encrypt-time AAD and decrypt-time AAD (column swap, uid
// swap, format change) causes AES-GCM auth failure.
//
// Example:
//
//	store.BuildAAD(store.AADAccessToken, 61590134170831)
//	→ []byte("mbs.access_token.uid=61590134170831")
func BuildAAD(col AADColumn, uid int64) []byte {
	// strconv.FormatInt is alloc-free for cached small ints and faster
	// than fmt.Sprintf. Both call sites (encrypt path, decrypt path)
	// must produce identical bytes — keep this implementation simple.
	return []byte("mbs." + string(col) + ".uid=" + strconv.FormatInt(uid, 10))
}

// FormatAAD returns the same string as BuildAAD as a Go string, for
// logging and debug output ONLY. Never use this in actual encrypt or
// decrypt calls — pass BuildAAD's []byte directly so we don't have a
// second code path that could drift.
func FormatAAD(col AADColumn, uid int64) string {
	return fmt.Sprintf("mbs.%s.uid=%d", col, uid)
}
