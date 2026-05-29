package importer

import (
	"bytes"
	"encoding/json"
	"testing"

	"mbs-native/auth"

	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
)

// testDEK returns a deterministic non-zero 32-byte DEK for tests.
func testDEK(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var k crypto.DataEncryptionKey
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// testDEK2 returns a different deterministic DEK to test cross-DEK
// decrypt failure.
func testDEK2(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var k crypto.DataEncryptionKey
	for i := range k {
		k[i] = byte(0xff - i)
	}
	return k
}

// validCreds builds a minimum-viable Creds that passes Validate().
func validCreds(uid int64) *auth.Creds {
	return &auth.Creds{
		AccessToken:    "EAAB..testtoken",
		SessionKey:     "5.aBcDeFgHiJ.0000000.0-" + "9999",
		UserID:         uid,
		Secret:         "abcd1234abcd1234abcd1234abcd1234",
		FamilyDeviceID: "11111111-2222-3333-4444-555555555555",
		DeviceID:       "11111111-2222-3333-4444",
		MachineID:      "machinex123",
		AppVersion:     "551.0.0.55.106",
		BuildNumber:    "955655792",
		DeviceModel:    "SM-S931B",
		AndroidVer:     "15",
		Manufacturer:   "samsung",
		Locale:         "en_US",
		Density:        "2.99375",
		Width:          1080,
		Height:         2340,
		Abi:            "arm64-v8a",
		VersionID:      "26854813974149875",
	}
}

func TestEncryptForUID_Happy(t *testing.T) {
	dek := testDEK(t)
	const uid int64 = 1674772559
	creds := validCreds(uid)

	cols, err := encryptForUID(dek, uid, creds, nil)
	if err != nil {
		t.Fatalf("encryptForUID: %v", err)
	}
	if len(cols.AccessToken) == 0 || len(cols.Secret) == 0 || len(cols.SessionKey) == 0 {
		t.Fatalf("encrypted columns must be non-empty: at=%d sec=%d sk=%d",
			len(cols.AccessToken), len(cols.Secret), len(cols.SessionKey))
	}
	if cols.Cookies != nil {
		t.Errorf("Cookies should be nil when envelope is nil, got %d bytes", len(cols.Cookies))
	}
	if cols.BridgeEnvelope != nil {
		t.Errorf("BridgeEnvelope should be nil when envelope is nil")
	}
	if cols.TOTPSecret != nil {
		t.Errorf("TOTPSecret should always be nil for legacy import (no TOTP in legacy JSON)")
	}

	// Round-trip: decrypt with correct AAD.
	pt, err := crypto.DecryptAESGCM(dek, cols.AccessToken,
		store.BuildAAD(store.AADAccessToken, uid))
	if err != nil {
		t.Fatalf("DecryptAESGCM access_token: %v", err)
	}
	if string(pt) != creds.AccessToken {
		t.Errorf("access_token round-trip mismatch: got %q want %q", pt, creds.AccessToken)
	}
}

func TestEncryptForUID_WithEnvelope(t *testing.T) {
	dek := testDEK(t)
	const uid int64 = 99999
	creds := validCreds(uid)
	env := &auth.BridgeEnvelope{
		Version:     auth.SupportedBridgeVersion,
		AccessToken: creds.AccessToken,
		UID:         uid,
		SessionKey:  creds.SessionKey,
		Secret:      creds.Secret,
		MachineID:   creds.MachineID,
		Cookies: map[string]string{
			"c_user": "1674772559",
			"datr":   "abcdefghij",
			"xs":     "session-xs-value",
		},
	}

	cols, err := encryptForUID(dek, uid, creds, env)
	if err != nil {
		t.Fatalf("encryptForUID: %v", err)
	}
	if len(cols.Cookies) == 0 {
		t.Fatal("Cookies must be non-empty when envelope carries cookies")
	}
	if len(cols.BridgeEnvelope) == 0 {
		t.Fatal("BridgeEnvelope plaintext must be non-empty when envelope provided")
	}

	// Decrypt cookies blob.
	pt, err := crypto.DecryptAESGCM(dek, cols.Cookies, store.BuildAAD(store.AADCookies, uid))
	if err != nil {
		t.Fatalf("DecryptAESGCM cookies: %v", err)
	}
	// Should round-trip to JSON envelope that contains the original cookies.
	var decoded auth.BridgeEnvelope
	if err := json.Unmarshal(pt, &decoded); err != nil {
		t.Fatalf("unmarshal decrypted envelope: %v", err)
	}
	if decoded.Cookies["c_user"] != "1674772559" {
		t.Errorf("cookie round-trip lost c_user: got %q", decoded.Cookies["c_user"])
	}
	// Plaintext BridgeEnvelope JSON must be the same bytes we encrypted.
	if !bytes.Equal(cols.BridgeEnvelope, pt) {
		t.Errorf("BridgeEnvelope plaintext should equal decrypted cookies plaintext (same source)")
	}
}

func TestEncryptForUID_EmptyEnvelopeCookies_LeavesCookiesNil(t *testing.T) {
	dek := testDEK(t)
	const uid int64 = 42
	creds := validCreds(uid)
	env := &auth.BridgeEnvelope{
		Version: auth.SupportedBridgeVersion,
		UID:     uid,
		Cookies: nil, // no cookies → don't encrypt
	}
	cols, err := encryptForUID(dek, uid, creds, env)
	if err != nil {
		t.Fatalf("encryptForUID: %v", err)
	}
	if cols.Cookies != nil {
		t.Errorf("Cookies should remain nil when envelope.Cookies is empty")
	}
	if cols.BridgeEnvelope != nil {
		t.Errorf("BridgeEnvelope should remain nil when no cookies are present")
	}
}

func TestEncryptForUID_AADBindingByColumn(t *testing.T) {
	// Verify column-bound AAD: ciphertext sealed with one column's AAD
	// must NOT decrypt under another column's AAD. This is the security
	// guarantee BuildAAD exists to enforce.
	dek := testDEK(t)
	const uid int64 = 7
	creds := validCreds(uid)

	cols, err := encryptForUID(dek, uid, creds, nil)
	if err != nil {
		t.Fatalf("encryptForUID: %v", err)
	}

	// Try decrypting access_token ciphertext under secret AAD — must fail.
	_, err = crypto.DecryptAESGCM(dek, cols.AccessToken,
		store.BuildAAD(store.AADSecret, uid))
	if err == nil {
		t.Fatal("access_token ciphertext decrypted under AADSecret — column binding broken")
	}

	// Try decrypting secret ciphertext under session_key AAD — must fail.
	_, err = crypto.DecryptAESGCM(dek, cols.Secret,
		store.BuildAAD(store.AADSessionKey, uid))
	if err == nil {
		t.Fatal("secret ciphertext decrypted under AADSessionKey — column binding broken")
	}
}

func TestEncryptForUID_AADBindingByUID(t *testing.T) {
	// Verify uid-bound AAD: ciphertext sealed with uidA's AAD must NOT
	// decrypt under uidB's AAD — even if both rows live in the same DB.
	dek := testDEK(t)
	const uidA int64 = 100
	const uidB int64 = 200

	credsA := validCreds(uidA)
	cols, err := encryptForUID(dek, uidA, credsA, nil)
	if err != nil {
		t.Fatalf("encryptForUID(uidA): %v", err)
	}

	_, err = crypto.DecryptAESGCM(dek, cols.AccessToken,
		store.BuildAAD(store.AADAccessToken, uidB))
	if err == nil {
		t.Fatal("ciphertext for uidA decrypted under uidB AAD — uid binding broken")
	}
}

func TestEncryptForUID_RejectsWrongDEK(t *testing.T) {
	const uid int64 = 1
	creds := validCreds(uid)
	cols, err := encryptForUID(testDEK(t), uid, creds, nil)
	if err != nil {
		t.Fatalf("encryptForUID: %v", err)
	}

	_, err = crypto.DecryptAESGCM(testDEK2(t), cols.AccessToken,
		store.BuildAAD(store.AADAccessToken, uid))
	if err == nil {
		t.Fatal("decrypt succeeded with wrong DEK — AES-GCM auth broken")
	}
}

func TestEncryptForUID_Errors(t *testing.T) {
	dek := testDEK(t)

	tests := []struct {
		name    string
		uid     int64
		creds   *auth.Creds
		wantErr string
	}{
		{"nil creds", 1, nil, "nil creds"},
		{"zero uid", 0, validCreds(1), "invalid uid"},
		{"uid mismatch", 5, validCreds(6), "uid mismatch"},
		{"empty access_token", 1, func() *auth.Creds {
			c := validCreds(1)
			c.AccessToken = ""
			return c
		}(), "missing access_token"},
		{"empty secret", 1, func() *auth.Creds {
			c := validCreds(1)
			c.Secret = ""
			return c
		}(), "missing secret"},
		{"empty session_key", 1, func() *auth.Creds {
			c := validCreds(1)
			c.SessionKey = ""
			return c
		}(), "missing session_key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encryptForUID(dek, tc.uid, tc.creds, nil)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestEncryptForUID_ZeroDEKRejected(t *testing.T) {
	// crypto.EncryptAESGCM refuses zero-value DEK. Importer must
	// propagate that failure cleanly rather than producing bogus
	// ciphertext.
	var zero crypto.DataEncryptionKey
	creds := validCreds(1)
	_, err := encryptForUID(zero, 1, creds, nil)
	if err == nil {
		t.Fatal("encryptForUID with zero DEK must error")
	}
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
