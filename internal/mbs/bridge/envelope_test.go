package bridge

import (
	"testing"

	"mbs-native/auth"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

// stubIdentity satisfies loginIdentityProvider for tests.
type stubIdentity struct {
	dev, fdid, mach string
}

func (s stubIdentity) LoginIdentity() (string, string, string) {
	return s.dev, s.fdid, s.mach
}

// realisticPayload returns a BloksLoginActionResponsePayload populated
// from the cold-install gold capture so envelope→creds round-trips
// against actual data, not synthetic values.
func realisticPayload() *messagix.BloksLoginActionResponsePayload {
	return &messagix.BloksLoginActionResponsePayload{
		AccessToken:        "EAABu2IFZCq3o-realistic-150-chars-or-whatever",
		UID:                1674772559,
		SessionKey:         "5.0oor9VhiOfiTgg.1778254326.11-1674772559",
		Secret:             "cac415ec0937d6f1c78cf6fba753c9d1",
		MachineID:          "9gH-aUzBMyDfrMwqEnEPkcaV",
		CredentialType:     "two_factor",
		IsAccountConfirmed: true,
		Identifier:         "alice@example.com",
	}
}

func TestBuildBridgeEnvelope_HappyPath(t *testing.T) {
	pay := realisticPayload()
	c := &cookies.Cookies{Platform: types.MessengerLite}
	c.UpdateValues(map[cookies.MetaCookieName]string{
		"c_user": "1674772559",
		"datr":   "datrvalue",
		"xs":     "xsvalue",
	})
	identity := stubIdentity{
		dev:  "7A17B762-668D-4BEF-A9CF-CD0ABD58231D",
		fdid: "7A17B762-668D-4BEF-A9CF-CD0ABD58231C",
		mach: "9gH-aUzBMyDfrMwqEnEPkcaV-from-bridge",
	}

	env := buildBridgeEnvelope(pay, c, identity)

	if env.Version != auth.SupportedBridgeVersion {
		t.Errorf("Version: got %d want %d", env.Version, auth.SupportedBridgeVersion)
	}
	if env.AccessToken != pay.AccessToken {
		t.Errorf("AccessToken mismatch")
	}
	if env.UID != pay.UID {
		t.Errorf("UID: got %d want %d", env.UID, pay.UID)
	}
	if env.SessionKey != pay.SessionKey {
		t.Errorf("SessionKey mismatch")
	}
	if env.Secret != pay.Secret {
		t.Errorf("Secret mismatch")
	}
	// Bridge identity wins for MachineID when non-empty.
	if env.MachineID != "9gH-aUzBMyDfrMwqEnEPkcaV-from-bridge" {
		t.Errorf("MachineID: got %q, want bridge value to win", env.MachineID)
	}
	if env.BridgeDeviceID != identity.dev {
		t.Errorf("BridgeDeviceID: got %q want %q", env.BridgeDeviceID, identity.dev)
	}
	if env.BridgeFamilyDeviceID != identity.fdid {
		t.Errorf("BridgeFamilyDeviceID: got %q want %q", env.BridgeFamilyDeviceID, identity.fdid)
	}
	if env.CredentialType != "two_factor" {
		t.Errorf("CredentialType: got %q", env.CredentialType)
	}
	if !env.IsAccountConfirmed {
		t.Errorf("IsAccountConfirmed: false, want true")
	}
	if env.Cookies["c_user"] != "1674772559" || env.Cookies["datr"] != "datrvalue" {
		t.Errorf("Cookies: %+v", env.Cookies)
	}
	if env.IssuedAt == 0 {
		t.Errorf("IssuedAt should be set")
	}
}

func TestBuildBridgeEnvelope_FallsBackToPayloadMachineID(t *testing.T) {
	pay := realisticPayload()
	// Identity reports empty machine_id — should fall back to payload.
	identity := stubIdentity{dev: "d", fdid: "f", mach: ""}
	env := buildBridgeEnvelope(pay, nil, identity)
	if env.MachineID != pay.MachineID {
		t.Errorf("MachineID fallback: got %q want %q", env.MachineID, pay.MachineID)
	}
}

func TestBuildBridgeEnvelope_NilIdentityNoPanic(t *testing.T) {
	pay := realisticPayload()
	env := buildBridgeEnvelope(pay, nil, nil)
	// All three bridge-source fields empty; MachineID falls back to payload.
	if env.BridgeDeviceID != "" || env.BridgeFamilyDeviceID != "" {
		t.Errorf("expected empty bridge ids on nil identity")
	}
	if env.MachineID != pay.MachineID {
		t.Errorf("MachineID: %q", env.MachineID)
	}
}

func TestBuildBridgeEnvelope_NilCookiesEmitsEmptyMap(t *testing.T) {
	pay := realisticPayload()
	env := buildBridgeEnvelope(pay, nil, stubIdentity{mach: "m"})
	if env.Cookies == nil {
		t.Errorf("Cookies should never be nil (empty map ok)")
	}
	if len(env.Cookies) != 0 {
		t.Errorf("Cookies should be empty, got %v", env.Cookies)
	}
}

func TestEnvelopeToCreds_RoundtripValid(t *testing.T) {
	pay := realisticPayload()
	identity := stubIdentity{
		dev:  "7A17B762-668D-4BEF-A9CF-CD0ABD58231D",
		fdid: "7A17B762-668D-4BEF-A9CF-CD0ABD58231C",
		mach: pay.MachineID,
	}
	env := buildBridgeEnvelope(pay, nil, identity)
	creds, err := envelopeToCreds(env)
	if err != nil {
		t.Fatalf("envelopeToCreds: %v", err)
	}
	if creds.AccessToken != pay.AccessToken {
		t.Errorf("AccessToken not preserved")
	}
	if creds.UserID != pay.UID {
		t.Errorf("UserID: got %d want %d", creds.UserID, pay.UID)
	}
	// MaterializeCreds lowercases device UUIDs.
	if creds.DeviceID != "7a17b762-668d-4bef-a9cf-cd0abd58231d" {
		t.Errorf("DeviceID not lowercased: %q", creds.DeviceID)
	}
	if creds.FamilyDeviceID != "7a17b762-668d-4bef-a9cf-cd0abd58231c" {
		t.Errorf("FamilyDeviceID not lowercased: %q", creds.FamilyDeviceID)
	}
	// Constants are filled in.
	if creds.AppVersion == "" || creds.DeviceModel == "" || creds.MqttCapabilities == 0 {
		t.Errorf("UA constants missing: %+v", creds)
	}
}

func TestEnvelopeToCreds_NilEnvelope(t *testing.T) {
	if _, err := envelopeToCreds(nil); err == nil {
		t.Errorf("expected error on nil envelope")
	}
}

func TestExtractDisplayName(t *testing.T) {
	if got := extractDisplayName(nil); got != "" {
		t.Errorf("nil payload: got %q want empty", got)
	}
	got := extractDisplayName(realisticPayload())
	if got != "alice@example.com" {
		t.Errorf("got %q want alice@example.com", got)
	}
}
