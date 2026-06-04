package session

import (
	"errors"
	"fmt"
)

// ErrClaimConflict is returned by GetOrConnect when another pod owns
// the session. OwnerPodID lets the gateway forward the request to the
// pod that holds the connection.
//
// Distinguish via errors.As:
//
//	var conflict *ErrClaimConflict
//	if errors.As(err, &conflict) { route to conflict.OwnerPodID }
//
// Or via errors.Is with ErrClaimConflictSentinel:
//
//	if errors.Is(err, ErrClaimConflictSentinel) { ... }
type ErrClaimConflict struct {
	UID        int64
	OwnerPodID string
}

func (e *ErrClaimConflict) Error() string {
	return fmt.Sprintf("session: uid %d owned by pod %q", e.UID, e.OwnerPodID)
}

// ErrClaimConflictSentinel is matched by ErrClaimConflict.Is so callers
// can use errors.Is without an As-with-typed-pointer.
var ErrClaimConflictSentinel = errors.New("session: claim conflict")

func (e *ErrClaimConflict) Is(target error) bool {
	return target == ErrClaimConflictSentinel
}

// ErrDrained is returned by GetOrConnect after Drain has been called.
// Gateway interprets this as "this pod is shutting down, retry against
// another pod" — once K8s multi-pod ships.
var ErrDrained = errors.New("session: manager drained, not accepting new connects")

// ErrShutdown is returned when an operation hits a fully-shutdown
// manager (post-Shutdown). Distinct from ErrDrained so callers can
// distinguish "shutting down" from "shut down".
var ErrShutdown = errors.New("session: manager already shut down")

// ErrProxyRequired is returned by connect when PROXY_REQUIRED is set but no
// proxy could be resolved for the session. The connect is refused rather than
// falling back to a direct (datacenter-IP) connection — the hard anti-ban
// policy (D3). The pod_id claim is released before returning.
var ErrProxyRequired = errors.New("session: proxy required but none could be resolved")
