package importer

import (
	"encoding/json"
	"errors"
	"fmt"

	"mbs-native/auth"

	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
)

// encryptedColumns bundles the BYTEA outputs the importer feeds into
// store.SessionRow. Each column uses its own AAD built via
// store.BuildAAD so a column-swap attack on stored ciphertext fails
// closed at decrypt time.
//
// Cookies is nil when no envelope is provided OR the envelope carries
// no cookies — the refresh ticker will treat this as a non-Stage-D
// row and either repair it via Set-Cookie merges on the next Ping or
// burn the session on the first sentinel.
//
// TOTPSecret is nil because legacy JSON archives never carried a
// TOTP secret; that's a Stage-E1 chunk-4 addition exposed only via
// BridgeLoginStart with PersistTotpSecret=true. Imported sessions
// requiring 2FA will go through the bridge driver on first re-auth.
type encryptedColumns struct {
	AccessToken    []byte
	Secret         []byte
	SessionKey     []byte
	Cookies        []byte
	TOTPSecret     []byte
	BridgeEnvelope []byte // plaintext JSONB — non-secret metadata
}

// encryptForUID seals the three mandatory secret columns
// (access_token, secret, session_key) and the optional cookies blob
// under their column-bound AADs. Inputs:
//
//   - dek: the DEK loaded by the caller (same DEK used by the live
//     hermes-mbs service — drift here produces silently-undecryptable
//     rows). The importer never logs or echoes the DEK.
//   - uid: the user_id; bound into every AAD so cross-uid swaps fail.
//   - creds: legacy Creds (loaded via auth.LoadFromFile). Must have
//     non-empty AccessToken / Secret / SessionKey. UID consistency is
//     the caller's responsibility (importer compares UID against
//     filename before calling).
//   - envelope: optional BridgeEnvelope sidecar. When non-nil and
//     carrying cookies, we marshal it as-is (preserving Stage-D
//     freshness timestamps) and encrypt the JSON. When nil OR cookies
//     empty, Cookies stays nil.
//
// Returns the encrypted columns on success. On any single-column
// encryption failure, returns the partial result plus a wrapped
// error: importer.Run logs + skips the session, never persists a
// half-encrypted row.
func encryptForUID(
	dek crypto.DataEncryptionKey,
	uid int64,
	creds *auth.Creds,
	envelope *auth.BridgeEnvelope,
) (encryptedColumns, error) {
	var out encryptedColumns

	if creds == nil {
		return out, errors.New("encryptForUID: nil creds")
	}
	if uid <= 0 {
		return out, fmt.Errorf("encryptForUID: invalid uid %d", uid)
	}
	if creds.UserID != uid {
		// Cheap belt-and-suspenders against a swap between walker
		// pairing and encrypt. Walker rejects mismatched filenames,
		// but if a future caller passes uid from a different
		// source, fail loudly.
		return out, fmt.Errorf("encryptForUID: uid mismatch: arg=%d creds.UserID=%d", uid, creds.UserID)
	}
	if creds.AccessToken == "" {
		return out, errors.New("encryptForUID: missing access_token (cannot encrypt empty plaintext)")
	}
	if creds.Secret == "" {
		return out, errors.New("encryptForUID: missing secret")
	}
	if creds.SessionKey == "" {
		return out, errors.New("encryptForUID: missing session_key")
	}

	var err error
	out.AccessToken, err = crypto.EncryptAESGCM(dek, []byte(creds.AccessToken),
		store.BuildAAD(store.AADAccessToken, uid))
	if err != nil {
		return out, fmt.Errorf("encrypt access_token: %w", err)
	}
	out.Secret, err = crypto.EncryptAESGCM(dek, []byte(creds.Secret),
		store.BuildAAD(store.AADSecret, uid))
	if err != nil {
		return out, fmt.Errorf("encrypt secret: %w", err)
	}
	out.SessionKey, err = crypto.EncryptAESGCM(dek, []byte(creds.SessionKey),
		store.BuildAAD(store.AADSessionKey, uid))
	if err != nil {
		return out, fmt.Errorf("encrypt session_key: %w", err)
	}

	// Cookies + envelope are paired: we encrypt the marshaled
	// envelope (which contains the cookies map) and keep the raw
	// JSON for the non-secret BridgeEnvelope JSONB column. Skip when
	// envelope is nil or has no cookies — refresh ticker handles
	// cookie-less rows by burning on first sentinel.
	if envelope != nil && len(envelope.Cookies) > 0 {
		envJSON, err := json.Marshal(envelope)
		if err != nil {
			return out, fmt.Errorf("marshal bridge envelope: %w", err)
		}
		out.BridgeEnvelope = envJSON
		out.Cookies, err = crypto.EncryptAESGCM(dek, envJSON,
			store.BuildAAD(store.AADCookies, uid))
		if err != nil {
			return out, fmt.Errorf("encrypt cookies: %w", err)
		}
	}

	return out, nil
}
