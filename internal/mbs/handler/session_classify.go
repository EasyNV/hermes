package handler

import (
	"errors"

	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/pkg/crypto"
)

// errorsIsAny returns true if errors.Is(err, target) for any target.
// Convenience helper for "matches any of these sentinels" patterns.
func errorsIsAny(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}

// sessionSentinelErrs returns the set of session-package errors that
// should be classified as "session-layer failure" rather than
// "client-side send failure". Used by mapSendErr to route between
// mapSessionErr and mapClientErr.
//
// ErrClaimConflict needs special handling — it's a struct error,
// not a sentinel — so we also check via errors.As. Provided as a
// sentinel via ErrClaimConflictSentinel for errors.Is compatibility
// (chunk-3 already exports this).
func sessionSentinelErrs() []error {
	return []error{
		session.ErrShutdown,
		session.ErrDrained,
		session.ErrClaimConflictSentinel,
		crypto.ErrDecryptFailed,
	}
}
