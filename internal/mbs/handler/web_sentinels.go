package handler

import (
	"mbs-native/web"

	"google.golang.org/grpc/codes"
)

// webAuthSentinels is the single source of truth for "this web error
// = this gRPC code". Listed most-specific-first so errors.Is matches
// the narrowest sentinel before the broad ErrSessionExpired catch-all.
//
// Maintained alongside mbs-native/web/errors.go — if a new sentinel
// is added there, add it here too.
var webAuthSentinels = []sentinelMapping{
	{err: web.ErrTokenInvalidated, code: codes.Unauthenticated},
	{err: web.ErrCheckpointRequired, code: codes.FailedPrecondition},
	{err: web.ErrChallengeRequired, code: codes.FailedPrecondition},
	{err: web.ErrConsentRequired, code: codes.FailedPrecondition},
	{err: web.ErrAccountSuspended, code: codes.PermissionDenied},
	// ErrSessionExpired LAST — it's the wrap parent of the four
	// above, so any specific-error match wins before we fall through
	// to the generic Unauthenticated.
	{err: web.ErrSessionExpired, code: codes.Unauthenticated},
}

type sentinelMapping struct {
	err  error
	code codes.Code
}

// IMPORTANT: mapWebAuthErr in errors.go iterates webAuthSentinels with
// errors.Is + returns the first match. Because the specific errors
// (Checkpoint/Challenge/Consent/Suspended/TokenInvalidated) all wrap
// ErrSessionExpired via fmt.Errorf("%w: ...", ErrSessionExpired),
// errors.Is(checkpointErr, ErrSessionExpired) is ALSO true.
//
// The list order above ensures specific codes win. Tests in
// errors_test.go pin this ordering — don't reorder without updating
// the tests.
