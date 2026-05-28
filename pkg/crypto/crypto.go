// Package crypto provides shared cryptographic primitives for Hermes
// services. The current surface is AES-256-GCM envelope encryption used
// by hermes-mbs to seal access tokens, session keys, secrets, cookies,
// and TOTP secrets at rest in PostgreSQL.
//
// Threat model: a DB-level read (replica leak, backup theft, SQL
// injection, DBA) sees only ciphertext. The cleartext only ever exists
// inside the running hermes-mbs process. The Data Encryption Key (DEK)
// is loaded once at startup from either:
//
//   - HERMES_MBS_DEK_FILE (recommended for K8s; tmpfs / projected volume)
//   - HERMES_MBS_DEK_HEX  (env var, fine for docker-compose dev/VPS)
//
// Failure to load fails the service closed.
//
// Generate a DEK once:
//
//	openssl rand -hex 32 > /etc/hermes-mbs/dek
//
// Loss of the DEK renders every encrypted column unrecoverable. Back it
// up out-of-band (e.g. vault/hermes-mbs-dek-v1.txt.gpg).
//
// # AAD binding (Additional Authenticated Data)
//
// Every Encrypt/Decrypt call takes an AAD argument. AAD is not encrypted
// but is bound to the ciphertext: decryption fails if the AAD differs
// from what was used at encryption. This prevents a row-write attacker
// from swapping ciphertexts between columns, rows, or accounts.
//
// Convention for hermes-mbs:
//
//	aad = "<service>.<column>.uid=<uid>"
//
// Examples:
//
//	"mbs.access_token.uid=61590134170831"
//	"mbs.session_key.uid=61590134170831"
//	"mbs.secret.uid=61590134170831"
//	"mbs.cookies.uid=61590134170831"
//	"mbs.totp_secret.uid=1674772559"
//
// AAD may be nil for use cases that genuinely have no contextual
// binding, but in that case do NOT use Encrypt/Decrypt for sensitive
// blobs that share a DEK — the absence of AAD means ciphertexts are
// interchangeable. The MBS store layer always supplies non-nil AAD.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// DataEncryptionKey is a 32-byte AES-256 key.
type DataEncryptionKey [32]byte

// IsZero reports whether the key is unset (all zero bytes). Useful for
// guarding against "never loaded" mistakes — callers that hit Encrypt
// with a zero key are almost always wrong.
func (k DataEncryptionKey) IsZero() bool {
	for _, b := range k {
		if b != 0 {
			return false
		}
	}
	return true
}

// ErrDecryptFailed is returned when the AES-GCM authentication tag does
// not verify (wrong key, tampered ciphertext, or truncated input). The
// underlying cipher error is intentionally not wrapped — distinguishing
// "wrong key" from "tampered bytes" would give an attacker information.
var ErrDecryptFailed = errors.New("crypto: AES-GCM authentication failed")

// ErrInvalidKey is returned by the loaders when the supplied hex string
// or file contents do not decode to exactly 32 bytes.
var ErrInvalidKey = errors.New("crypto: DEK must be exactly 32 bytes (64 hex chars)")

// LoadDEKFromHexEnv reads envVar, decodes it as hex, and returns the
// 32-byte key. Returns an error if the env var is unset, blank after
// trim, not valid hex, or not 32 bytes.
func LoadDEKFromHexEnv(envVar string) (DataEncryptionKey, error) {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return DataEncryptionKey{}, fmt.Errorf("crypto: env var %s is empty", envVar)
	}
	return decodeHex(raw)
}

// LoadDEKFromFile reads path, treats the contents as a hex-encoded key
// (trailing whitespace tolerated — matches `openssl rand -hex 32 > file`
// which appends a newline), and returns the 32-byte key.
func LoadDEKFromFile(path string) (DataEncryptionKey, error) {
	if path == "" {
		return DataEncryptionKey{}, fmt.Errorf("crypto: DEK file path is empty")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return DataEncryptionKey{}, fmt.Errorf("crypto: read DEK file %s: %w", path, err)
	}
	raw := strings.TrimSpace(string(contents))
	if raw == "" {
		return DataEncryptionKey{}, fmt.Errorf("crypto: DEK file %s is empty after trim", path)
	}
	return decodeHex(raw)
}

func decodeHex(raw string) (DataEncryptionKey, error) {
	if len(raw) != 64 {
		return DataEncryptionKey{}, fmt.Errorf("%w: got %d hex chars", ErrInvalidKey, len(raw))
	}
	dec, err := hex.DecodeString(raw)
	if err != nil {
		return DataEncryptionKey{}, fmt.Errorf("crypto: hex decode: %w", err)
	}
	if len(dec) != 32 {
		return DataEncryptionKey{}, fmt.Errorf("%w: got %d bytes", ErrInvalidKey, len(dec))
	}
	var k DataEncryptionKey
	copy(k[:], dec)
	return k, nil
}

// nonceLen is the standard GCM nonce length. Do not change this without
// a coordinated migration of every encrypted column in the database.
const nonceLen = 12

// EncryptAESGCM seals plaintext with AES-256-GCM using a cryptographically
// random 12-byte nonce. The returned byte slice layout is:
//
//	[nonce(12)] [ciphertext+tag(N+16)]
//
// Total overhead is 28 bytes over plaintext length. AAD length does not
// affect the output size — AAD is authenticated but not encrypted.
//
// Nonce reuse with a given key would be catastrophic; we use crypto/rand
// for every call so reuse is statistically impossible (2^96 nonce space).
//
// AAD (Additional Authenticated Data) is bound to the ciphertext: any
// later DecryptAESGCM with a different AAD will return ErrDecryptFailed.
// AAD may be nil but callers that share a DEK across logical columns
// should pass non-nil context strings — see the package doc convention.
//
// The DEK must not be zero — callers that pass an unset DataEncryptionKey
// get an error rather than silently producing a useless ciphertext that
// any attacker could decrypt.
func EncryptAESGCM(dek DataEncryptionKey, plaintext, aad []byte) ([]byte, error) {
	if dek.IsZero() {
		return nil, fmt.Errorf("crypto: refusing to encrypt with zero DEK")
	}
	block, err := aes.NewCipher(dek[:])
	if err != nil {
		// AES-256 keys are always valid; this path is unreachable.
		return nil, fmt.Errorf("crypto: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher.NewGCM: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read random nonce: %w", err)
	}
	// gcm.Seal appends ciphertext+tag to its first arg. By passing the
	// nonce slice itself we get a single contiguous [nonce|ct+tag] buffer.
	out := gcm.Seal(nonce, nonce, plaintext, aad)
	return out, nil
}

// DecryptAESGCM reverses EncryptAESGCM. Returns ErrDecryptFailed on any
// authentication failure — wrong key, wrong AAD, tampered bytes,
// truncated input. All failure modes collapse to a single sentinel by
// design (distinguishing them would leak information to an attacker).
func DecryptAESGCM(dek DataEncryptionKey, sealed, aad []byte) ([]byte, error) {
	if dek.IsZero() {
		return nil, fmt.Errorf("crypto: refusing to decrypt with zero DEK")
	}
	if len(sealed) < nonceLen+16 {
		// 12 bytes nonce + 16 bytes auth tag is the absolute minimum;
		// shorter input cannot be a valid AES-GCM ciphertext.
		return nil, ErrDecryptFailed
	}
	block, err := aes.NewCipher(dek[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher.NewGCM: %w", err)
	}
	nonce := sealed[:nonceLen]
	ct := sealed[nonceLen:]
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		// Intentionally do NOT wrap err — distinguishing wrong-key from
		// wrong-AAD from tampered-bytes would leak information.
		return nil, ErrDecryptFailed
	}
	return pt, nil
}
