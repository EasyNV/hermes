package refresh

import (
	"context"
	"errors"

	"mbs-native/web"
)

// refreshAction is the discrete outcome of classifying a Ping result.
// One action per attempt; the ticker uses this to decide whether to
// persist cookies, burn the session, suspend it, or log+retry.
type refreshAction int

const (
	actionUnknown refreshAction = iota

	// actionMergeCookies: Ping returned 2xx AND merged at least one
	// Set-Cookie. Persist the new envelope + bump LastRefreshedAt.
	actionMergeCookies

	// actionBumpValidated: Ping returned 2xx but no cookie change.
	// Session is alive; bump LastValidatedAt only.
	actionBumpValidated

	// actionBurnPermanent: token-invalidated or account-suspended.
	// State -> burned. Operator must re-bridge. Lifecycle: "burned".
	actionBurnPermanent

	// actionSuspend: checkpoint / challenge / consent. Recoverable
	// by user interaction (Meta lifts the gate). State -> suspended.
	// Lifecycle: "suspended". We still release the claim because
	// there's no live session to maintain.
	actionSuspend

	// actionTransientError: network, ctx, 5xx — no state change,
	// retry on next tick.
	actionTransientError
)

// String renders the action for log lines + test diagnostics.
func (a refreshAction) String() string {
	switch a {
	case actionMergeCookies:
		return "merge_cookies"
	case actionBumpValidated:
		return "bump_validated"
	case actionBurnPermanent:
		return "burn_permanent"
	case actionSuspend:
		return "suspend"
	case actionTransientError:
		return "transient_error"
	default:
		return "unknown"
	}
}

// classifyRefreshErr maps a Ping result + err to an action + reason
// string. The reason is used both for the burn-row reason column
// (store.BurnSession) AND the NATS lifecycle event subject fragment
// (handler.EventPublisher.PublishSessionLifecycle).
//
// Priority (most-specific first):
//
//  1. ctx errors -> transient (ticker is shutting down; not the
//     session's fault)
//  2. web.ErrTokenInvalidated -> burn (permanent invalidation)
//  3. web.ErrAccountSuspended -> burn (Meta-side action; permanent)
//  4. web.ErrCheckpointRequired -> suspend (user can clear)
//  5. web.ErrChallengeRequired -> suspend (user can clear)
//  6. web.ErrConsentRequired -> suspend (user can clear)
//  7. any other non-nil err -> transient
//  8. err == nil && signal.CookiesChanged -> merge_cookies
//  9. err == nil && !signal.CookiesChanged -> bump_validated
//  10. err == nil && signal == nil -> transient (broken Ping
//      contract; shouldn't happen but defend against nil-deref)
//
// The web.ErrSessionExpired catch-all is intentionally NOT matched
// here. The 5 specific sentinels above all wrap ErrSessionExpired
// via fmt.Errorf("%w: ..."), so errors.Is(checkErr, ErrSessionExpired)
// is true. If we matched ErrSessionExpired before the specifics
// we'd lose the burn vs suspend distinction. Match specifics only.
func classifyRefreshErr(signal *web.RefreshSignal, err error) (refreshAction, string) {
	// Ctx errors override everything — we're shutting down.
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return actionTransientError, "ctx_canceled"
		}
		switch {
		case errors.Is(err, web.ErrTokenInvalidated):
			return actionBurnPermanent, "token_invalidated"
		case errors.Is(err, web.ErrAccountSuspended):
			return actionBurnPermanent, "account_suspended"
		case errors.Is(err, web.ErrCheckpointRequired):
			return actionSuspend, "checkpoint_required"
		case errors.Is(err, web.ErrChallengeRequired):
			return actionSuspend, "challenge_required"
		case errors.Is(err, web.ErrConsentRequired):
			return actionSuspend, "consent_required"
		}
		// Any other error (HTTP 5xx, network, parse failure) is
		// transient — don't change session state.
		return actionTransientError, "network_or_5xx"
	}

	if signal == nil {
		// Ping returned (nil signal, nil err) — broken contract.
		// Treat as transient to avoid nil-deref downstream.
		return actionTransientError, "nil_signal"
	}

	if signal.CookiesChanged {
		return actionMergeCookies, ""
	}
	return actionBumpValidated, ""
}
