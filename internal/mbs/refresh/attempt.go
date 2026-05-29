package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"mbs-native/auth"
	"mbs-native/web"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
)

// refreshClient is the narrow surface attempt.go consumes. Production
// uses *web.Client; tests inject a scripted ping fake.
type refreshClient interface {
	Ping(ctx context.Context) (*web.RefreshSignal, error)
}

// attemptResult is what tickOnce aggregates for the summary log.
// All fields are best-effort — Outcome alone is enough for routing.
type attemptResult struct {
	UID     int64
	Outcome string // human-readable: "merge_cookies", "bump_validated",
	// "burn_permanent", "suspend", "transient_error",
	// "decrypt_failed", "envelope_unmarshal_failed",
	// "cookies_build_failed", "encrypt_failed",
	// "store_update_failed"
	Reason  string        // burn/suspend reason (mirrors classify)
	Err     error         // populated when Outcome ∈ error states
	Latency time.Duration // wall-clock from start to persist (or err)
}

// attemptRefresh runs the cookie refresh for one session row.
//
// Flow (test-pinned):
//
//	 1. Decrypt creds + cookies (separate columns)
//	 2. Build a transient web.Client over the decrypted cookies
//	 3. client.Ping(ctx)
//	 4. classifyRefreshErr(signal, err) -> action + reason
//	 5. Dispatch:
//	    - merge_cookies      -> persist new envelope + LastRefreshedAt
//	    - bump_validated     -> persist same cookies + LastValidatedAt
//	    - burn_permanent     -> BurnSession + lifecycle "burned"
//	    - suspend            -> UpdateSessionState("suspended") + lifecycle "suspended"
//	    - transient_error    -> log + metric, no state change
//
// Each call is independent — no shared state with sibling attempts
// in the same tick. Returns the result for aggregation; never panics
// (errors are wrapped in attemptResult.Err).
func (t *Ticker) attemptRefresh(ctx context.Context, row *store.SessionRow) (result attemptResult) {
	start := t.nowFn()
	result = attemptResult{UID: row.UID}
	defer func() {
		result.Latency = t.nowFn().Sub(start)
	}()

	t.metrics.incAttempts()

	// 1. Decrypt creds.
	creds, err := session.DecryptCreds(t.dek, row)
	if err != nil {
		t.metrics.incDecryptFailures()
		result.Outcome = "decrypt_failed"
		result.Err = fmt.Errorf("decrypt creds: %w", err)
		return result
	}

	// 1b. Decrypt cookies (separate column; the BridgeEnvelope JSON
	// is encrypted as a unit under AADCookies).
	if len(row.EncryptedCookies) == 0 {
		// Legacy pre-Stage-D session with no envelope. Can't refresh.
		// Transient outcome so we don't burn — operator can re-bridge.
		t.metrics.incTransient()
		result.Outcome = "transient_error"
		result.Reason = "no_cookies"
		result.Err = fmt.Errorf("no encrypted cookies for uid %d", row.UID)
		return result
	}
	envelopeJSON, err := crypto.DecryptAESGCM(t.dek, row.EncryptedCookies,
		store.BuildAAD(store.AADCookies, row.UID))
	if err != nil {
		t.metrics.incDecryptFailures()
		result.Outcome = "decrypt_failed"
		result.Err = fmt.Errorf("decrypt cookies: %w", err)
		return result
	}
	var envelope auth.BridgeEnvelope
	if err := json.Unmarshal(envelopeJSON, &envelope); err != nil {
		t.metrics.incTransient()
		result.Outcome = "envelope_unmarshal_failed"
		result.Err = fmt.Errorf("unmarshal envelope: %w", err)
		return result
	}

	// 2. Build cookies + client.
	cookies, err := web.FromEnvelope(envelope.Cookies)
	if err != nil {
		t.metrics.incTransient()
		result.Outcome = "cookies_build_failed"
		result.Err = fmt.Errorf("build cookies from envelope: %w", err)
		return result
	}
	client := t.clientFactory(creds, cookies)

	// 3. Ping.
	signal, pingErr := client.Ping(ctx)

	// 4. Classify.
	action, reason := classifyRefreshErr(signal, pingErr)
	result.Reason = reason

	// 5. Dispatch.
	switch action {
	case actionMergeCookies:
		t.persistMergedCookies(ctx, row, &envelope, signal, &result)
	case actionBumpValidated:
		t.persistBumpedValidated(ctx, row, &envelope, signal, &result)
	case actionBurnPermanent:
		t.burnSession(ctx, row, reason, &result)
	case actionSuspend:
		t.suspendSession(ctx, row, reason, &result)
	case actionTransientError:
		t.metrics.incTransient()
		result.Outcome = "transient_error"
		result.Err = pingErr
	default:
		t.metrics.incTransient()
		result.Outcome = "unknown_action"
		result.Err = fmt.Errorf("unknown action %v", action)
	}

	return result
}

// persistMergedCookies re-marshals the envelope with merged cookies +
// updated LastRefreshedAt and stores via UpdateSessionCookies. On
// failure the result.Outcome is set to encrypt_failed or
// store_update_failed (Err populated).
func (t *Ticker) persistMergedCookies(
	ctx context.Context,
	row *store.SessionRow,
	envelope *auth.BridgeEnvelope,
	signal *web.RefreshSignal,
	result *attemptResult,
) {
	envelope.Cookies = signal.Cookies.ToMap()
	envelope.LastRefreshedAt = signal.ResponseTime
	envelope.LastValidatedAt = signal.ResponseTime

	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		t.metrics.incTransient()
		result.Outcome = "envelope_marshal_failed"
		result.Err = fmt.Errorf("marshal envelope: %w", err)
		return
	}
	enc, err := crypto.EncryptAESGCM(t.dek, envelopeJSON,
		store.BuildAAD(store.AADCookies, row.UID))
	if err != nil {
		t.metrics.incTransient()
		result.Outcome = "encrypt_failed"
		result.Err = fmt.Errorf("encrypt cookies: %w", err)
		return
	}
	if err := t.store.UpdateSessionCookies(ctx, row.UID, enc,
		envelope.LastRefreshedAt, envelope.LastValidatedAt); err != nil {
		t.metrics.incTransient()
		result.Outcome = "store_update_failed"
		result.Err = fmt.Errorf("update cookies: %w", err)
		return
	}
	t.metrics.incSuccesses()
	result.Outcome = "merge_cookies"

	// Lifecycle event (refreshed). prev/next both ACTIVE — refresh
	// doesn't change session state when successful.
	t.publisher.PublishSessionLifecycle(
		row.UID, row.TenantID,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		"refreshed", 0, t.podID,
	)
}

// persistBumpedValidated re-marshals the envelope with the SAME
// cookies (no change) and ONLY bumps LastValidatedAt. We still need
// to write because UpdateSessionCookies's signature takes both
// timestamps; LastRefreshedAt stays as-is (carrying the prior value).
//
// Subtle: we MUST still encrypt + persist to update the
// LastValidatedAt column, otherwise the next list query would
// re-pick this session. Cheap to do — 1 AES-GCM + 1 UPDATE per tick.
func (t *Ticker) persistBumpedValidated(
	ctx context.Context,
	row *store.SessionRow,
	envelope *auth.BridgeEnvelope,
	signal *web.RefreshSignal,
	result *attemptResult,
) {
	// LastRefreshedAt unchanged; only LastValidatedAt advances.
	envelope.LastValidatedAt = signal.ResponseTime

	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		t.metrics.incTransient()
		result.Outcome = "envelope_marshal_failed"
		result.Err = fmt.Errorf("marshal envelope: %w", err)
		return
	}
	enc, err := crypto.EncryptAESGCM(t.dek, envelopeJSON,
		store.BuildAAD(store.AADCookies, row.UID))
	if err != nil {
		t.metrics.incTransient()
		result.Outcome = "encrypt_failed"
		result.Err = fmt.Errorf("encrypt envelope: %w", err)
		return
	}
	if err := t.store.UpdateSessionCookies(ctx, row.UID, enc,
		envelope.LastRefreshedAt, envelope.LastValidatedAt); err != nil {
		t.metrics.incTransient()
		result.Outcome = "store_update_failed"
		result.Err = fmt.Errorf("update cookies: %w", err)
		return
	}
	t.metrics.incSuccesses()
	result.Outcome = "bump_validated"
}

// burnSession marks the row burned and emits a lifecycle event.
// Failure to record the burn is logged (via attemptResult.Err) but
// not propagated — Meta has already invalidated the session; the
// state machine catches up on the next tick.
func (t *Ticker) burnSession(
	ctx context.Context,
	row *store.SessionRow,
	reason string,
	result *attemptResult,
) {
	if err := t.store.BurnSession(ctx, row.UID, reason); err != nil {
		t.metrics.incTransient()
		result.Outcome = "store_burn_failed"
		result.Err = fmt.Errorf("burn session: %w", err)
		return
	}
	// Release pod_id so another pod (or this one after restart) can
	// claim the row for cleanup operations. Best-effort.
	if err := t.store.ReleaseSession(ctx, row.UID, t.podID); err != nil {
		// Not fatal — burn is recorded, lifecycle event will fire.
		// The next tick's list won't return this row anyway
		// (state != active).
		result.Err = fmt.Errorf("release after burn: %w", err)
	}
	t.metrics.incBurns(reason)
	result.Outcome = "burn_permanent"

	t.publisher.PublishSessionLifecycle(
		row.UID, row.TenantID,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_BURNED,
		"burned", 0, t.podID,
	)
}

// suspendSession marks the row suspended (recoverable) and emits a
// lifecycle event. Same release-claim semantics as burn.
func (t *Ticker) suspendSession(
	ctx context.Context,
	row *store.SessionRow,
	reason string,
	result *attemptResult,
) {
	if err := t.store.UpdateSessionState(ctx, row.UID, "suspended", nil); err != nil {
		t.metrics.incTransient()
		result.Outcome = "store_suspend_failed"
		result.Err = fmt.Errorf("suspend session: %w", err)
		return
	}
	if err := t.store.ReleaseSession(ctx, row.UID, t.podID); err != nil {
		result.Err = fmt.Errorf("release after suspend: %w", err)
	}
	t.metrics.incSuspends(reason)
	result.Outcome = "suspend"

	t.publisher.PublishSessionLifecycle(
		row.UID, row.TenantID,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_ACTIVE,
		hermesv1.MbsSessionState_MBS_SESSION_STATE_SUSPENDED,
		"suspended", 0, t.podID,
	)
}
