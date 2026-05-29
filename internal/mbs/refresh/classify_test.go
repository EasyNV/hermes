package refresh

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"mbs-native/web"
)

// TestClassify_StageDSentinels pins each of the 5 Stage-D sentinel
// errors to its action+reason. These are the lifecycle pivot
// points — drift here = silently broken burn/suspend wiring.
func TestClassify_StageDSentinels(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantAction refreshAction
		wantReason string
	}{
		{"TokenInvalidated", web.ErrTokenInvalidated, actionBurnPermanent, "token_invalidated"},
		{"AccountSuspended", web.ErrAccountSuspended, actionBurnPermanent, "account_suspended"},
		{"CheckpointRequired", web.ErrCheckpointRequired, actionSuspend, "checkpoint_required"},
		{"ChallengeRequired", web.ErrChallengeRequired, actionSuspend, "challenge_required"},
		{"ConsentRequired", web.ErrConsentRequired, actionSuspend, "consent_required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotAct, gotReason := classifyRefreshErr(nil, c.err)
			if gotAct != c.wantAction {
				t.Errorf("action: got %v want %v", gotAct, c.wantAction)
			}
			if gotReason != c.wantReason {
				t.Errorf("reason: got %q want %q", gotReason, c.wantReason)
			}
		})
	}
}

// TestClassify_WrappedSentinelsStillMatch pins that fmt.Errorf-wrapped
// Stage-D errors still classify correctly. mbs-native/web wraps
// upstream HTTP errors via %w, so production wrapped errors must
// continue to route to the right action.
func TestClassify_WrappedSentinelsStillMatch(t *testing.T) {
	wrapped := fmt.Errorf("ping /latest/inbox: %w", web.ErrCheckpointRequired)
	gotAct, gotReason := classifyRefreshErr(nil, wrapped)
	if gotAct != actionSuspend {
		t.Errorf("wrapped checkpoint: got action %v want suspend", gotAct)
	}
	if gotReason != "checkpoint_required" {
		t.Errorf("wrapped checkpoint: got reason %q want checkpoint_required", gotReason)
	}
}

// TestClassify_CtxOverridesSentinel: when both ctx-canceled AND a
// Stage-D error are in the chain (rare but possible if the body
// read after a ctx cancel races into a checkpoint response), ctx
// wins — we're shutting down, not making session-state decisions.
func TestClassify_CtxOverridesSentinel(t *testing.T) {
	wrapped := fmt.Errorf("ping: %w (also %v)", context.Canceled, web.ErrCheckpointRequired)
	gotAct, gotReason := classifyRefreshErr(nil, wrapped)
	if gotAct != actionTransientError {
		t.Errorf("ctx+sentinel: got action %v want transient", gotAct)
	}
	if gotReason != "ctx_canceled" {
		t.Errorf("ctx+sentinel: got reason %q want ctx_canceled", gotReason)
	}
}

// TestClassify_GenericErrorIsTransient: any non-sentinel error
// (network, 5xx, parse failure) maps to transient — no state change.
func TestClassify_GenericErrorIsTransient(t *testing.T) {
	gotAct, gotReason := classifyRefreshErr(nil, errors.New("connection reset"))
	if gotAct != actionTransientError {
		t.Errorf("got %v want transient", gotAct)
	}
	if gotReason != "network_or_5xx" {
		t.Errorf("reason: got %q want network_or_5xx", gotReason)
	}
}

// TestClassify_NilErrCookiesChanged pins the happy path: 2xx with
// Set-Cookie diff -> merge_cookies action, empty reason.
func TestClassify_NilErrCookiesChanged(t *testing.T) {
	sig := &web.RefreshSignal{CookiesChanged: true}
	gotAct, gotReason := classifyRefreshErr(sig, nil)
	if gotAct != actionMergeCookies {
		t.Errorf("got %v want merge_cookies", gotAct)
	}
	if gotReason != "" {
		t.Errorf("reason should be empty, got %q", gotReason)
	}
}

// TestClassify_NilErrNoCookieChange: 2xx without Set-Cookie diff
// -> bump_validated. Session is alive but Meta didn't bother
// rotating cookies on this call.
func TestClassify_NilErrNoCookieChange(t *testing.T) {
	sig := &web.RefreshSignal{CookiesChanged: false}
	gotAct, gotReason := classifyRefreshErr(sig, nil)
	if gotAct != actionBumpValidated {
		t.Errorf("got %v want bump_validated", gotAct)
	}
	if gotReason != "" {
		t.Errorf("reason should be empty, got %q", gotReason)
	}
}

// TestClassify_NilErrNilSignal pins the defensive "Ping returned
// (nil, nil)" contract — we treat as transient so the ticker logs +
// retries instead of panic-dereffing.
func TestClassify_NilErrNilSignal(t *testing.T) {
	gotAct, gotReason := classifyRefreshErr(nil, nil)
	if gotAct != actionTransientError {
		t.Errorf("got %v want transient", gotAct)
	}
	if gotReason != "nil_signal" {
		t.Errorf("reason: got %q want nil_signal", gotReason)
	}
}

// TestRefreshAction_StringRoundtrip pins the human-readable form
// used in log lines. Adding a new action constant should update
// String() in lockstep.
func TestRefreshAction_StringRoundtrip(t *testing.T) {
	cases := map[refreshAction]string{
		actionMergeCookies:   "merge_cookies",
		actionBumpValidated:  "bump_validated",
		actionBurnPermanent:  "burn_permanent",
		actionSuspend:        "suspend",
		actionTransientError: "transient_error",
		actionUnknown:        "unknown",
	}
	for act, want := range cases {
		if got := act.String(); got != want {
			t.Errorf("action %d: got %q want %q", act, got, want)
		}
	}
}
