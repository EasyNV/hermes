package handler

import (
	"context"
	"errors"
	"fmt"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error-mapping helpers. Every RPC funnels its returned errors through
// these helpers so the gateway sees a small, stable set of gRPC codes
// regardless of which downstream surface produced the error.
//
// IMPORTANT: never include cleartext secrets in the returned error
// message. The error chain may be logged at the gateway — keep
// messages structural ("decrypt failed", "store: not found"), not
// data-bearing ("decrypt failed for access_token=<bytes>").

// mapStoreErr maps store layer errors to gRPC status.
//
//   store.ErrNotFound         → codes.NotFound
//   store.ErrTenantMismatch   → codes.PermissionDenied
//   store.ErrClaimConflict    → codes.FailedPrecondition (no owner metadata at this layer)
//   store.ErrNotImplemented   → codes.Unimplemented   (shouldn't happen in prod; surfaced for chunk-dev visibility)
//   nil                       → nil
//   anything else             → codes.Internal
func mapStoreErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, "session not found")
	case errors.Is(err, store.ErrTenantMismatch):
		return status.Error(codes.PermissionDenied, "session belongs to a different tenant")
	case errors.Is(err, store.ErrClaimConflict):
		return status.Error(codes.FailedPrecondition, "session owned by another pod")
	case errors.Is(err, store.ErrNotImplemented):
		return status.Errorf(codes.Unimplemented, "store method not implemented: %v", err)
	default:
		return status.Errorf(codes.Internal, "store: %v", err)
	}
}

// mapSessionErr maps session.Manager errors to gRPC status. Includes
// the owner_pod_id metadata for ErrClaimConflict so the gateway/caller
// can transparently re-route to the owning pod in K8s.
//
//   session.ErrShutdown / ErrDrained → codes.Unavailable
//   session.ErrClaimConflict         → codes.FailedPrecondition + owner_pod_id in error details
//   crypto.ErrDecryptFailed (wrapped)→ codes.Unauthenticated  (creds rotation needed)
//   anything else                    → mapStoreErr (covers store hits inside connect path)
func mapSessionErr(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, session.ErrShutdown) || errors.Is(err, session.ErrDrained) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var conflict *session.ErrClaimConflict
	if errors.As(err, &conflict) {
		st := status.New(codes.FailedPrecondition, "session owned by another pod")
		// Attach owner_pod_id as ErrorInfo so K8s can route on it.
		// In compose this is informational only (single pod).
		detail := &errdetails.ErrorInfo{
			Reason: "MBS_CLAIM_CONFLICT",
			Domain: "hermes-mbs",
			Metadata: map[string]string{
				"owner_pod_id": conflict.OwnerPodID,
				"uid":          fmt.Sprintf("%d", conflict.UID),
			},
		}
		withDetails, derr := st.WithDetails(detail)
		if derr == nil {
			return withDetails.Err()
		}
		// WithDetails should never fail for ErrorInfo, but be safe.
		return st.Err()
	}

	if errors.Is(err, crypto.ErrDecryptFailed) {
		// Stored ciphertext won't decrypt with this DEK — the session
		// must be re-bridged. Treat as auth failure.
		return status.Error(codes.Unauthenticated, "session decrypt failed (re-bridge required)")
	}

	// Fall through to store-layer mapping. session.connect wraps store
	// returns from ClaimSession/GetSession/etc with %w; covers
	// ErrNotFound + ErrTenantMismatch from those paths.
	return mapStoreErr(err)
}

// mapClientErr maps mbs-native client + graphql + web errors. The
// shape varies (typed errors in web/, concrete struct in graphql/, plain
// errors elsewhere); centralize the mapping so handlers stay terse.
//
//   ctx.Canceled / DeadlineExceeded → propagate as-is
//   web auth errors (token/checkpoint/challenge/consent/suspended)  → matching codes
//   graphql.CreateCustomerError → codes.FailedPrecondition + details
//   anything else → codes.Internal
//
// We import the web errors symbolically via interface assertions to
// avoid pulling all of mbs-native/web into this package's dep graph
// when only the error sentinels are used.
func mapClientErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.FromContextError(err).Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}

	// CreateCustomerError carries Phone/PageID/Mailbox + ErrorCode/Message.
	// We unwrap rather than import the graphql package directly because the
	// type's only meaningful surface is its Error() string and its presence
	// in the chain — and importing graphql would pull a heavy transitive
	// dependency graph just for the type assertion.
	if isGraphqlCreateCustomerErr(err) {
		return status.Errorf(codes.FailedPrecondition,
			"resolve customer rejected by Meta: %v", err)
	}

	// Token-class errors from mbs-native/web. We pattern-match on
	// errors.Is against sentinel pointers exported via the small
	// IsWebAuthError interface block below.
	if classified := mapWebAuthErr(err); classified != nil {
		return classified
	}

	return status.Errorf(codes.Internal, "client: %v", err)
}

// mapBridgeErr converts a BridgeLoginErrorCode + message to a gRPC
// status code suitable for terminating a BridgeLogin stream. The
// handler also sends a BridgeLoginUpdate{failure: ...} before
// returning the status — this is just the stream-close code.
func mapBridgeErr(code hermesv1.BridgeLoginErrorCode, msg string) error {
	if msg == "" {
		msg = code.String()
	}
	switch code {
	case hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INVALID_CREDS,
		hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_WRONG_CODE:
		return status.Error(codes.Unauthenticated, msg)
	case hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_CHECKPOINT,
		hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_2FA_REQUIRED,
		hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_PREFLIGHT_RC19,
		hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_PREFLIGHT_RC4:
		return status.Error(codes.FailedPrecondition, msg)
	case hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_NETWORK:
		return status.Error(codes.Unavailable, msg)
	case hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_BRIDGE_SUBPROCESS,
		hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_INTERNAL:
		return status.Error(codes.Internal, msg)
	case hermesv1.BridgeLoginErrorCode_BRIDGE_ERR_UNSPECIFIED:
		return status.Error(codes.Internal, "bridge: unspecified error")
	default:
		return status.Errorf(codes.Internal, "bridge: %s: %s", code.String(), msg)
	}
}

// ──────────────────────────────────────────────────────────────────
// Web auth error classification
// ──────────────────────────────────────────────────────────────────

// Web auth errors live in mbs-native/web — we'd ideally import them
// directly, but to keep this package's compile-time deps minimal we
// pattern-match via errors.Is against pointers we lazy-resolve below.
//
// The mapping table (single source of truth):
//
//   web.ErrTokenInvalidated  → Unauthenticated
//   web.ErrSessionExpired    → Unauthenticated
//   web.ErrCheckpointRequired → FailedPrecondition
//   web.ErrChallengeRequired → FailedPrecondition
//   web.ErrConsentRequired   → FailedPrecondition
//   web.ErrAccountSuspended  → PermissionDenied
//
// Implementation: web is imported below — small, stable, no diamond
// deps.
func mapWebAuthErr(err error) error {
	// We import the web package below for the sentinels. Keeping the
	// match in its own helper makes it trivial to add new sentinels.
	for _, s := range webAuthSentinels {
		if errors.Is(err, s.err) {
			return status.Error(s.code, s.err.Error())
		}
	}
	return nil
}

// isGraphqlCreateCustomerErr does an unwrap-style scan for the
// *graphql.CreateCustomerError type without importing the graphql
// package. The needle is the struct's Error() text shape, which is
// stable enough for this purpose. If graphql is imported elsewhere
// later, swap this for errors.As(err, &graphql.CreateCustomerError{}).
func isGraphqlCreateCustomerErr(err error) bool {
	if err == nil {
		return false
	}
	// CreateCustomerError.Error() always starts with "create_customer rejected".
	const needle = "create_customer rejected"
	cur := err
	for cur != nil {
		if msg := cur.Error(); len(msg) >= len(needle) && msg[:len(needle)] == needle {
			return true
		}
		uw := errors.Unwrap(cur)
		if uw == cur {
			break
		}
		cur = uw
	}
	return false
}
