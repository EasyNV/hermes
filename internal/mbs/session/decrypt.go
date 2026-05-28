package session

import (
	"errors"
	"fmt"

	"mbs-native/auth"

	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
)

// decryptCreds turns a stored SessionRow (with encrypted BYTEA columns)
// into a usable *auth.Creds suitable for client.New.
//
// Three columns are decrypted (access_token, secret, session_key); each
// uses column-specific AAD via store.BuildAAD. Wrong DEK or column swap
// produces crypto.ErrDecryptFailed.
//
// Cookies are NOT decrypted here — the refresh ticker handles them
// separately (chunk 5). totp_secret is also not decrypted here; it's
// only needed by the bridge driver during re-login (chunk 5+).
func decryptCreds(dek crypto.DataEncryptionKey, row *store.SessionRow) (*auth.Creds, error) {
	return DecryptCreds(dek, row)
}

// DecryptCreds is the exported entry point for callers outside this
// package (handler in chunk 4 — standalone ResolvePhone/SendMessage paths
// that need decrypted creds but not a full MQTToT session). Same
// behavior as the package-private decryptCreds.
//
// IMPORTANT: returned *auth.Creds contains plaintext access_token /
// session_key / secret. Caller MUST treat it as a transient secret —
// never log, never persist plain, never send over the wire to anything
// other than Meta's endpoints via the mbs-native client stack.
func DecryptCreds(dek crypto.DataEncryptionKey, row *store.SessionRow) (*auth.Creds, error) {
	if row == nil {
		return nil, errors.New("session: decryptCreds: nil row")
	}

	accessToken, err := decryptCol(dek, row.EncryptedAccessToken, store.AADAccessToken, row.UID, "access_token")
	if err != nil {
		return nil, err
	}
	secret, err := decryptCol(dek, row.EncryptedSecret, store.AADSecret, row.UID, "secret")
	if err != nil {
		return nil, err
	}
	sessionKey, err := decryptCol(dek, row.EncryptedSessionKey, store.AADSessionKey, row.UID, "session_key")
	if err != nil {
		return nil, err
	}

	creds := &auth.Creds{
		AccessToken:      string(accessToken),
		SessionKey:       string(sessionKey),
		UserID:           row.UID,
		Secret:           string(secret),
		FamilyDeviceID:   row.FamilyDeviceID,
		DeviceID:         row.DeviceID,
		MachineID:        row.MachineID,
		AppVersion:       row.AppVersion,
		BuildNumber:      row.BuildNumber,
		DeviceModel:      row.DeviceModel,
		AndroidVer:       row.AndroidVer,
		Manufacturer:     row.Manufacturer,
		Locale:           row.Locale,
		Density:          row.Density,
		Width:            row.ScreenWidth,
		Height:           row.ScreenHeight,
		Abi:              row.ABI,
		VersionID:        row.VersionID,
		MqttCapabilities: int32(row.MQTTCapabilities),

		// Plaintext business-asset fields. These live in mbs_session_assets
		// proper, but auth.Creds keeps a denormalized copy for the
		// client.Send path. Manager fills them after ListAssets if a
		// primary asset is present.
	}
	if err := creds.Validate(); err != nil {
		return nil, fmt.Errorf("session: decrypted creds failed validation: %w", err)
	}
	return creds, nil
}

// decryptCol does a single-column decrypt with column-bound AAD. The
// label is for error messages — NEVER include cleartext bytes in the
// error chain (would leak secrets via logs).
func decryptCol(dek crypto.DataEncryptionKey, ct []byte, col store.AADColumn, uid int64, label string) ([]byte, error) {
	if len(ct) == 0 {
		return nil, fmt.Errorf("session: encrypted column %s is empty for uid %d", label, uid)
	}
	pt, err := crypto.DecryptAESGCM(dek, ct, store.BuildAAD(col, uid))
	if err != nil {
		// Wrap with column label but do NOT include any byte content.
		return nil, fmt.Errorf("session: decrypt %s: %w", label, err)
	}
	return pt, nil
}
