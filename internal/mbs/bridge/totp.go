// Package bridge integrates mautrix-meta's Confidential Authentication API
// (CAA) login state machine into hermes-mbs as an embedded goroutine.
// It implements handler.Driver — produces DriverUpdates that the handler
// relays to the BridgeLogin gRPC stream.
//
// Architecture:
//
//	handler.BridgeLogin RPC
//	    └→ DriverFactory(opts) → MautrixDriver
//	          └→ Run(ctx, req) starts loginLoop goroutine
//	                 └→ mautrix-meta messagix.MessengerLite.DoLoginSteps
//	                       ↓ (prompts, success, failure)
//	                 ← DriverUpdate channel ← emitProgress/Prompt/Success/Failure
//	          ← Submit(input) feeds user response (2FA, captcha) to the loop
//	          ← Close() cancels driver-owned ctx + waits for goroutine
package bridge

import (
	"errors"
	"fmt"
	"strings"
)

// normalizeTOTPSecret mirrors mbs-native/auth.NormalizeTOTPSecret and the
// POC's re/mbs/mbs-bridge-login/totp.go::normalizeTOTPSecret. We re-host
// instead of importing because:
//
//  1. mbs-native lives in a separate Go module pulled in via the
//     `replace mbs-native => ./re/mbs/mbs-native` directive — but the
//     normalizer is one of those small private helpers that doesn't
//     warrant exposing a package boundary in mbs-native/auth.
//  2. pquerna/otp (used for the actual TOTP derivation) does NOT do
//     base32 validation OR character normalization (spaces, dashes,
//     case). Bad inputs from the UI surface as cryptic "decode base32"
//     errors deep in the totp.GenerateCode call instead of a clean
//     "this isn't a base32 secret" error at the door.
//
// Both sides MUST normalize identically — if the handler accepts a
// secret and the bridge rejects it (or vice versa) the user sees a
// confusing UX. This implementation is byte-equivalent to the POC.
//
// Behavior:
//   - Strips whitespace (space, tab, CR, LF), dashes, underscores.
//   - Uppercases a-z → A-Z.
//   - Validates remaining chars are RFC 4648 base32: A-Z, 2-7, optional '='.
//   - Requires the normalized result to be ≥ 16 chars (= 80 bits per
//     RFC 6238 §5.1's lower bound for a shared secret).
//
// Returns (normalized, nil) on success or ("", error) describing what was wrong.
func normalizeTOTPSecret(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("totp secret is empty")
	}
	var sb strings.Builder
	sb.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '-' || r == '_':
			continue
		case r >= 'a' && r <= 'z':
			sb.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r)
		case r >= '2' && r <= '7':
			sb.WriteRune(r)
		case r == '=':
			sb.WriteRune(r)
		default:
			return "", fmt.Errorf("totp secret contains invalid base32 char %q (allowed: A-Z, 2-7, padding =)", r)
		}
	}
	out := sb.String()
	if len(out) < 16 {
		return "", fmt.Errorf("totp secret normalized to %d chars; RFC 6238 expects >= 16 base32 chars (= 80 bits)", len(out))
	}
	return out, nil
}
