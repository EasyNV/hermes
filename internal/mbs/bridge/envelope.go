package bridge

import (
	"errors"
	"fmt"
	"time"

	"mbs-native/auth"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
)

// loginIdentityProvider is the narrow surface buildBridgeEnvelope needs
// from the mautrix-meta client. Lets tests inject a fake; production
// passes a *messagix.Client (its MessengerLite.GetLoginIdentity matches).
type loginIdentityProvider interface {
	// LoginIdentity returns (deviceID, familyDeviceID, machineID).
	// All three may be empty before a successful login.
	LoginIdentity() (deviceID, familyDeviceID, machineID string)
}

// messagixLoginIdentity wraps a *messagix.Client to satisfy
// loginIdentityProvider. The wrapper exists so the bridge tests can
// substitute fakes without needing to construct a real messagix.Client
// (which requires a cookie jar, HTTP transport, logger, and side-effect
// inits we don't want in unit tests).
type messagixLoginIdentity struct {
	client *messagix.Client
}

func (m *messagixLoginIdentity) LoginIdentity() (string, string, string) {
	return m.client.MessengerLite.GetLoginIdentity()
}

// buildBridgeEnvelope assembles an *auth.BridgeEnvelope from the
// mautrix-meta MessengerLite login output. Mirrors the POC's
// re/mbs/mbs-bridge-login/main.go::emit() construction so a session
// bridged inside hermes-mbs is byte-equivalent to one bridged via the
// standalone POC binary.
//
// Inputs:
//   - payload: the post-login BloksLoginActionResponsePayload (carries
//     access_token, uid, session_key, secret, machine_id, etc.). MUST
//     be non-nil — caller is responsible for the nil check (failure to
//     emit Success is an INTERNAL error in the loginLoop).
//   - finalCookies: the cookie jar at end of login. May be nil — we
//     just emit an empty Cookies map in that case.
//   - identity: surface for pulling device identity. Production wraps a
//     *messagix.Client; tests pass a stub.
//
// The envelope's MachineID prefers the post-login LoginIdentity() value
// (the bridge browser's settled value) and falls back to the payload's
// machine_id. This matches the POC behavior.
func buildBridgeEnvelope(
	payload *messagix.BloksLoginActionResponsePayload,
	finalCookies *cookies.Cookies,
	identity loginIdentityProvider,
) *auth.BridgeEnvelope {
	var deviceID, familyDeviceID, machineID string
	if identity != nil {
		deviceID, familyDeviceID, machineID = identity.LoginIdentity()
	}
	if machineID == "" {
		machineID = payload.MachineID
	}

	cookieMap := map[string]string{}
	if finalCookies != nil {
		for k, v := range finalCookies.GetAll() {
			cookieMap[string(k)] = v
		}
	}

	return &auth.BridgeEnvelope{
		Version:              auth.SupportedBridgeVersion,
		IssuedAt:             time.Now().Unix(),
		AccessToken:          payload.AccessToken,
		UID:                  payload.UID,
		SessionKey:           payload.SessionKey,
		Secret:               payload.Secret,
		MachineID:            machineID,
		CredentialType:       payload.CredentialType,
		IsAccountConfirmed:   payload.IsAccountConfirmed,
		BridgeDeviceID:       deviceID,
		BridgeFamilyDeviceID: familyDeviceID,
		Cookies:              cookieMap,
	}
}

// envelopeToCreds produces the BizApp Android-shaped *auth.Creds the
// downstream MQTT/Lightspeed stack reads. Thin wrapper around
// mbs-native's canonical auth.MaterializeCreds so we don't fork the
// UA constant table or the device-id lowercasing logic.
//
// forceNewDevice controls whether the envelope's BridgeDeviceID /
// BridgeFamilyDeviceID override an existing device-state file. In the
// hermes-mbs path we're persisting fresh per BridgeLogin so there's
// no existing state — pass nil + false.
func envelopeToCreds(env *auth.BridgeEnvelope) (*auth.Creds, error) {
	if env == nil {
		return nil, errors.New("envelopeToCreds: nil envelope")
	}
	creds, err := auth.MaterializeCreds(env, nil, false)
	if err != nil {
		return nil, fmt.Errorf("materialize creds: %w", err)
	}
	return creds, nil
}

// extractDisplayName attempts to derive a human-readable display name
// from the login payload. mautrix-meta's BloksLoginActionResponsePayload
// doesn't carry a clean "full name" field (Meta hides that until a
// follow-up GraphQL query), so we fall back to the login identifier
// (typically the email or phone the user logged in with). Better than
// an empty DisplayName in the session row; chunk 6's asset-discovery
// path can overwrite it with the real Page or User name.
func extractDisplayName(payload *messagix.BloksLoginActionResponsePayload) string {
	if payload == nil {
		return ""
	}
	// Identifier is the login handle (email/phone). If empty, leave
	// blank — the handler tolerates empty DisplayName.
	return payload.Identifier
}
