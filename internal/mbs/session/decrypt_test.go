package session

import (
	"crypto/rand"
	"errors"
	"testing"

	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
)

func genDEK(t *testing.T) crypto.DataEncryptionKey {
	t.Helper()
	var k crypto.DataEncryptionKey
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return k
}

// seedRow builds a SessionRow with the encrypted columns populated via
// the given DEK and column-specific AAD. Plaintext fields are filled
// with plausible BizApp identity values so Validate passes after
// decryption.
func seedRow(t *testing.T, dek crypto.DataEncryptionKey, uid int64) (
	row *store.SessionRow,
	accessTokenCT, secretCT, sessionKeyCT string,
) {
	t.Helper()
	encrypt := func(col store.AADColumn, pt string) []byte {
		ct, err := crypto.EncryptAESGCM(dek, []byte(pt), store.BuildAAD(col, uid))
		if err != nil {
			t.Fatalf("EncryptAESGCM(%s): %v", col, err)
		}
		return ct
	}
	row = &store.SessionRow{
		UID:                  uid,
		TenantID:             "tenant-A",
		State:                "active",
		EncryptedAccessToken: encrypt(store.AADAccessToken, "EAABu2IFZCq3o-plaintext"),
		EncryptedSecret:      encrypt(store.AADSecret, "cac415ec0937d6f1c78cf6fba753c9d1"),
		EncryptedSessionKey:  encrypt(store.AADSessionKey, "5.0oor9VhiOfiTgg.1778254326.11-61590134170831"),

		// Plaintext identity — required for Creds.Validate
		FamilyDeviceID:   "7a17b762-668d-4bef-a9cf-cd0abd58231c",
		DeviceID:         "7a17b762-668d-4bef-a9cf-cd0abd58231d",
		AppVersion:       "551.0.0.55.106",
		BuildNumber:      "955655792",
		DeviceModel:      "SM-S931B",
		AndroidVer:       "15",
		Manufacturer:     "samsung",
		Locale:           "en_US",
		Density:          "2.99375",
		ScreenWidth:      1080,
		ScreenHeight:     2340,
		ABI:              "arm64-v8a",
		VersionID:        "26854813974149875",
		MQTTCapabilities: 514,
	}
	return row, "EAABu2IFZCq3o-plaintext", "cac415ec0937d6f1c78cf6fba753c9d1", "5.0oor9VhiOfiTgg.1778254326.11-61590134170831"
}

func TestDecryptCreds_RoundTrip(t *testing.T) {
	dek := genDEK(t)
	const uid = int64(61590134170831)
	row, wantAT, wantSec, wantSK := seedRow(t, dek, uid)

	creds, err := decryptCreds(dek, row)
	if err != nil {
		t.Fatalf("decryptCreds: %v", err)
	}
	if creds.AccessToken != wantAT {
		t.Errorf("access_token: got %q want %q", creds.AccessToken, wantAT)
	}
	if creds.Secret != wantSec {
		t.Errorf("secret: got %q want %q", creds.Secret, wantSec)
	}
	if creds.SessionKey != wantSK {
		t.Errorf("session_key: got %q want %q", creds.SessionKey, wantSK)
	}
	if creds.UserID != uid {
		t.Errorf("UserID: got %d want %d", creds.UserID, uid)
	}
	// Spot-check plaintext fields threaded through.
	if creds.DeviceID == "" || creds.FamilyDeviceID == "" || creds.AppVersion == "" {
		t.Errorf("plaintext identity fields missing: %+v", creds)
	}
}

func TestDecryptCreds_WrongDEK_FailsClosed(t *testing.T) {
	good := genDEK(t)
	bad := genDEK(t)
	if good == bad {
		t.Fatal("two random DEKs collided")
	}
	row, _, _, _ := seedRow(t, good, 61590134170831)

	_, err := decryptCreds(bad, row)
	if err == nil {
		t.Fatal("expected error decrypting with wrong DEK")
	}
	if !errors.Is(err, crypto.ErrDecryptFailed) {
		t.Errorf("expected ErrDecryptFailed in chain, got %v", err)
	}
	// Error message must include column label (operator visibility) but
	// NEVER include any byte content from the failing column.
	if !contains(err.Error(), "access_token") {
		t.Errorf("error should identify failing column, got %q", err.Error())
	}
}

func TestDecryptCreds_EmptyColumn_ErrorsClearly(t *testing.T) {
	dek := genDEK(t)
	row, _, _, _ := seedRow(t, dek, 61590134170831)
	row.EncryptedSecret = nil // simulate corrupt/incomplete migration

	_, err := decryptCreds(dek, row)
	if err == nil {
		t.Fatal("expected error on empty column")
	}
	if !contains(err.Error(), "secret") {
		t.Errorf("error should name the empty column, got %q", err.Error())
	}
	if !contains(err.Error(), "empty") {
		t.Errorf("error should say 'empty', got %q", err.Error())
	}
}

func TestDecryptCreds_CrossColumnSwap_Rejected(t *testing.T) {
	// Attack scenario: row-write attacker copies access_token ciphertext
	// into the secret column. Decryption MUST fail because AAD differs.
	dek := genDEK(t)
	const uid = int64(61590134170831)
	row, _, _, _ := seedRow(t, dek, uid)

	// Swap the access_token ciphertext into the secret slot.
	row.EncryptedSecret = row.EncryptedAccessToken

	_, err := decryptCreds(dek, row)
	if err == nil {
		t.Fatal("column-swap attack should fail decryption")
	}
	if !errors.Is(err, crypto.ErrDecryptFailed) {
		t.Errorf("expected ErrDecryptFailed in chain, got %v", err)
	}
}

func TestDecryptCreds_NilRow(t *testing.T) {
	_, err := decryptCreds(genDEK(t), nil)
	if err == nil {
		t.Fatal("expected error on nil row")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
