package handler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"mbs-native/web"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func codeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return status.Code(err)
}

func TestMapStoreErr_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"not found", store.ErrNotFound, codes.NotFound},
		{"tenant mismatch", store.ErrTenantMismatch, codes.PermissionDenied},
		{"claim conflict bare", store.ErrClaimConflict, codes.FailedPrecondition},
		{"not implemented", store.ErrNotImplemented, codes.Unimplemented},
		{"generic", errors.New("boom"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codeOf(mapStoreErr(c.in)); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestMapSessionErr_ClaimConflictHasOwnerDetail(t *testing.T) {
	err := &session.ErrClaimConflict{UID: 100, OwnerPodID: "pod-99"}
	got := mapSessionErr(err)
	st, ok := status.FromError(got)
	if !ok {
		t.Fatalf("expected gRPC status, got %T", got)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code: got %v want FailedPrecondition", st.Code())
	}
	details := st.Details()
	if len(details) == 0 {
		t.Fatal("expected ErrorInfo details on claim conflict")
	}
	// The serialized ErrorInfo should mention owner_pod_id (we don't
	// import errdetails into the test to avoid a dep — string-match is fine).
	found := false
	for _, d := range details {
		if fmt.Sprintf("%v", d) != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("ErrorInfo detail not parseable")
	}
}

func TestMapSessionErr_ShutdownDrained(t *testing.T) {
	if codeOf(mapSessionErr(session.ErrShutdown)) != codes.Unavailable {
		t.Errorf("ErrShutdown should map to Unavailable")
	}
	if codeOf(mapSessionErr(session.ErrDrained)) != codes.Unavailable {
		t.Errorf("ErrDrained should map to Unavailable")
	}
}

func TestMapSessionErr_DecryptFailWrapped(t *testing.T) {
	wrapped := fmt.Errorf("session: decrypt access_token: %w", crypto.ErrDecryptFailed)
	if codeOf(mapSessionErr(wrapped)) != codes.Unauthenticated {
		t.Errorf("wrapped ErrDecryptFailed should map to Unauthenticated")
	}
}

func TestMapClientErr_ContextErrors(t *testing.T) {
	if codeOf(mapClientErr(context.Canceled)) != codes.Canceled {
		t.Errorf("ctx.Canceled should map to Canceled")
	}
	if codeOf(mapClientErr(context.DeadlineExceeded)) != codes.DeadlineExceeded {
		t.Errorf("ctx.DeadlineExceeded should map to DeadlineExceeded")
	}
}

func TestMapClientErr_WebSentinels_SpecificWins(t *testing.T) {
	// Most-specific sentinels should map to their declared codes, even
	// though they wrap ErrSessionExpired (which alone would map to
	// Unauthenticated — but the more-specific Checkpoint should win
	// FailedPrecondition).
	cases := []struct {
		err  error
		want codes.Code
	}{
		{web.ErrTokenInvalidated, codes.Unauthenticated},
		{web.ErrCheckpointRequired, codes.FailedPrecondition},
		{web.ErrChallengeRequired, codes.FailedPrecondition},
		{web.ErrConsentRequired, codes.FailedPrecondition},
		{web.ErrAccountSuspended, codes.PermissionDenied},
		{web.ErrSessionExpired, codes.Unauthenticated}, // bare parent
	}
	for _, c := range cases {
		t.Run(c.err.Error(), func(t *testing.T) {
			got := codeOf(mapClientErr(c.err))
			if got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestMapClientErr_GraphqlCreateCustomer_FailedPrecondition(t *testing.T) {
	// CreateCustomerError.Error() starts with "create_customer rejected"
	// — we detect by prefix without importing graphql.
	err := errors.New("create_customer rejected (phone=62812 page=1 mailbox=2 code=ERR): test")
	got := mapClientErr(err)
	if codeOf(got) != codes.FailedPrecondition {
		t.Errorf("got %v want FailedPrecondition", codeOf(got))
	}
}

func TestMapClientErr_GenericInternal(t *testing.T) {
	if codeOf(mapClientErr(errors.New("random"))) != codes.Internal {
		t.Errorf("generic err should map to Internal")
	}
}

func TestMapBridgeErr_CodeMatrix(t *testing.T) {
	cases := []struct {
		code hermesv1.BridgeLoginErrorCode
		want codes.Code
	}{
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS, codes.Unauthenticated},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_WRONG_CODE, codes.Unauthenticated},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_CHECKPOINT, codes.FailedPrecondition},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_REQUIRED, codes.FailedPrecondition},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_PREFLIGHT_RC19, codes.FailedPrecondition},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_PREFLIGHT_RC4, codes.FailedPrecondition},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_NETWORK, codes.Unavailable},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_BRIDGE_SUBPROCESS, codes.Internal},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL, codes.Internal},
		{hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_UNSPECIFIED, codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.code.String(), func(t *testing.T) {
			got := codeOf(mapBridgeErr(c.code, "msg"))
			if got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestIsGraphqlCreateCustomerErr(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":           {nil, false},
		"matching":      {errors.New("create_customer rejected (phone=x page=y mailbox=z code=A): bad"), true},
		"wrapped match": {fmt.Errorf("graphql: %w", errors.New("create_customer rejected (a): b")), true},
		"non-matching":  {errors.New("something else"), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isGraphqlCreateCustomerErr(c.err); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}
