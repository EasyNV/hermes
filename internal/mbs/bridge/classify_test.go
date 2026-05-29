package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"go.mau.fi/mautrix-meta/pkg/messagix"
)

func TestClassifyMautrixErr_NilReturnsInternal(t *testing.T) {
	got := classifyMautrixErr(nil)
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("nil err: got %v want INTERNAL", got.Code)
	}
}

func TestClassifyMautrixErr_ContextCanceled(t *testing.T) {
	got := classifyMautrixErr(context.Canceled)
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("ctx.Canceled: got %v", got.Code)
	}
	if got.Retryable {
		t.Errorf("ctx.Canceled should NOT be retryable")
	}

	// Wrapped via fmt.Errorf still classified.
	wrapped := fmt.Errorf("outer wrap: %w", context.DeadlineExceeded)
	got = classifyMautrixErr(wrapped)
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("wrapped DeadlineExceeded: got %v", got.Code)
	}
}

func TestClassifyMautrixErr_TokenInvalidatedToInvalidCreds(t *testing.T) {
	got := classifyMautrixErr(messagix.ErrTokenInvalidated)
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS {
		t.Errorf("ErrTokenInvalidated: got %v want INVALID_CREDS", got.Code)
	}
	if got.Retryable {
		t.Errorf("INVALID_CREDS should NOT be retryable")
	}

	// Wrapped error chain.
	wrapped := fmt.Errorf("login wrapper: %w", messagix.ErrTokenInvalidated)
	got = classifyMautrixErr(wrapped)
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS {
		t.Errorf("wrapped ErrTokenInvalidated: got %v", got.Code)
	}
}

func TestClassifyMautrixErr_CheckpointFamily(t *testing.T) {
	cases := []error{
		messagix.ErrCheckpointRequired,
		messagix.ErrChallengeRequired,
		messagix.ErrConsentRequired,
		messagix.ErrAccountSuspended,
	}
	for _, err := range cases {
		got := classifyMautrixErr(err)
		if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_CHECKPOINT {
			t.Errorf("%v: got %v want CHECKPOINT", err, got.Code)
		}
		if got.Retryable {
			t.Errorf("%v: should NOT be retryable", err)
		}
	}
}

func TestClassifyMautrixErr_NetworkFamily(t *testing.T) {
	cases := []error{
		messagix.ErrServerError,
		messagix.ErrRequestFailed,
		messagix.ErrResponseReadFailed,
		messagix.ErrMaxRetriesReached,
		messagix.ErrTooManyRedirects,
	}
	for _, err := range cases {
		got := classifyMautrixErr(err)
		if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_NETWORK {
			t.Errorf("%v: got %v want NETWORK", err, got.Code)
		}
		if !got.Retryable {
			t.Errorf("%v: should be retryable", err)
		}
	}
}

// timeoutErr implements net.Error with Timeout()=true for the
// net.Error branch test.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyMautrixErr_NetErrorTimeoutIsNetwork(t *testing.T) {
	var _ net.Error = timeoutErr{} // assert interface satisfied
	got := classifyMautrixErr(timeoutErr{})
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_NETWORK {
		t.Errorf("net.Error timeout: got %v want NETWORK", got.Code)
	}
	if !got.Retryable {
		t.Errorf("net.Error timeout should be retryable")
	}
}

func TestClassifyMautrixErr_HeuristicPasswordMessages(t *testing.T) {
	cases := []string{
		"incorrect password",
		"WRONG password please retry",
		"login_password_error code 42",
		"invalid_credentials returned from CAA",
	}
	for _, msg := range cases {
		got := classifyMautrixErr(errors.New(msg))
		if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS {
			t.Errorf("%q: got %v want INVALID_CREDS", msg, got.Code)
		}
	}
}

func TestClassifyMautrixErr_HeuristicTOTPMessages(t *testing.T) {
	cases := []string{
		"totp_code mismatch",
		"two_factor verification failed",
		"two-factor session expired",
		"the verification code you entered is wrong",
	}
	for _, msg := range cases {
		got := classifyMautrixErr(errors.New(msg))
		if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_WRONG_CODE {
			t.Errorf("%q: got %v want 2FA_WRONG_CODE", msg, got.Code)
		}
		if !got.Retryable {
			t.Errorf("%q: 2FA_WRONG_CODE should be retryable", msg)
		}
	}
}

func TestClassifyMautrixErr_HeuristicCheckpointMessages(t *testing.T) {
	cases := []string{
		"checkpoint flow triggered",
		"account challenge_picker presented",
		"this account is suspended",
	}
	for _, msg := range cases {
		got := classifyMautrixErr(errors.New(msg))
		if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_CHECKPOINT {
			t.Errorf("%q: got %v want CHECKPOINT", msg, got.Code)
		}
	}
}

func TestClassifyMautrixErr_UnknownErrorIsInternal(t *testing.T) {
	got := classifyMautrixErr(errors.New("totally novel and unknown reason"))
	if got.Code != hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL {
		t.Errorf("unknown: got %v want INTERNAL", got.Code)
	}
}

func TestClassifyMautrixErr_TimePackageImport(t *testing.T) {
	// Smoke ensures the time import is real (not dropped by go fmt).
	_ = time.Second
}
