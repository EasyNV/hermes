package bridge

import (
	"context"
	"errors"
	"net"
	"strings"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"go.mau.fi/mautrix-meta/pkg/messagix"
)

// classifyMautrixErr maps a mautrix-meta DoLoginSteps error to the
// closest BridgeLoginErrorCode. The handler later runs the code through
// mapBridgeErr to produce the final gRPC status.
//
// Mapping table (priority: most-specific-first):
//
//	context.Canceled                    → INTERNAL (handler converts ctx
//	                                       errors via mapClientErr — but
//	                                       we still return a Failure so
//	                                       the stream gets a terminal event)
//	messagix.ErrTokenInvalidated*       → INVALID_CREDS
//	messagix.ErrAccountSuspended        → CHECKPOINT (caller must verify
//	                                       web; FailedPrecondition shape)
//	messagix.ErrCheckpointRequired      → CHECKPOINT
//	messagix.ErrChallengeRequired       → CHECKPOINT
//	messagix.ErrConsentRequired         → CHECKPOINT
//	messagix.ErrServerError /
//	  ErrRequestFailed /
//	  ErrMaxRetriesReached /
//	  net.Error (timeout/temporary)     → NETWORK
//	anything else                       → INTERNAL
//
// The Message field carries the original error text — useful for the
// gateway/UI display. The Retryable flag is set conservatively:
//
//	NETWORK     → true   (transient by definition)
//	2FA_*       → true   (user can retry the code)
//	all else    → false  (CHECKPOINT requires manual web action;
//	                      INVALID_CREDS means wrong password — retry
//	                      with new creds means a new BridgeLogin RPC)
type classifiedFailure struct {
	Code      hermesv1.BridgeLoginErrorCode
	Message   string
	Retryable bool
}

func classifyMautrixErr(err error) classifiedFailure {
	if err == nil {
		return classifiedFailure{Code: hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL}
	}

	msg := err.Error()

	// Most-specific first — errors.Is on the published mautrix sentinels.
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
			Message:   msg,
			Retryable: false,
		}

	case errors.Is(err, messagix.ErrTokenInvalidated):
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS,
			Message:   msg,
			Retryable: false,
		}

	case errors.Is(err, messagix.ErrCheckpointRequired),
		errors.Is(err, messagix.ErrChallengeRequired),
		errors.Is(err, messagix.ErrConsentRequired),
		errors.Is(err, messagix.ErrAccountSuspended):
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_CHECKPOINT,
			Message:   msg,
			Retryable: false,
		}

	case errors.Is(err, messagix.ErrServerError),
		errors.Is(err, messagix.ErrRequestFailed),
		errors.Is(err, messagix.ErrResponseReadFailed),
		errors.Is(err, messagix.ErrMaxRetriesReached),
		errors.Is(err, messagix.ErrTooManyRedirects):
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_NETWORK,
			Message:   msg,
			Retryable: true,
		}
	}

	// net.Error transient classification: any net error that reports
	// Timeout() is transient. (`Temporary()` is deprecated in newer
	// stdlib, so we only branch on Timeout.)
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_NETWORK,
			Message:   msg,
			Retryable: true,
		}
	}

	// Heuristic fallbacks for mautrix errors that are wrapped strings
	// (the Bloks interpreter emits these as plain fmt.Errorf chains
	// — no sentinel to match). Keep this list conservative; we'd
	// rather classify as INTERNAL than mis-route a real failure.
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "incorrect password"),
		strings.Contains(lower, "wrong password"),
		strings.Contains(lower, "invalid_credentials"),
		strings.Contains(lower, "login_password_error"):
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS,
			Message:   msg,
			Retryable: false,
		}

	case strings.Contains(lower, "totp"),
		strings.Contains(lower, "two_factor"),
		strings.Contains(lower, "two-factor"),
		strings.Contains(lower, "verification code"):
		// Whether it's "wrong code" vs "required" is ambiguous from a
		// flat message. Default to WRONG_CODE for retryability — the
		// user just types a new code.
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_WRONG_CODE,
			Message:   msg,
			Retryable: true,
		}

	case strings.Contains(lower, "checkpoint"),
		strings.Contains(lower, "challenge"),
		strings.Contains(lower, "suspended"):
		return classifiedFailure{
			Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_CHECKPOINT,
			Message:   msg,
			Retryable: false,
		}
	}

	return classifiedFailure{
		Code:      hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL,
		Message:   msg,
		Retryable: false,
	}
}
