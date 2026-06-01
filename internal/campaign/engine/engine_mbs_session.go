package engine

import (
	"context"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// HandleSessionBurned processes one MbsSessionLifecycleEvent whose new_state is
// 'burned' (subject hermes.mbs.session.burned.*). It proactively disables the
// burned uid as a sender across every campaign so the campaign_senders table
// reflects reality the moment a session dies mid-campaign — rather than relying
// solely on the state-gated selection JOIN to mask it at pick time (G2).
//
// Idempotent: MarkMbsSenderBurned only touches rows currently 'active', so a
// redelivered burn event is a harmless 0-row update. Always returns true (Ack):
// the selection-time JOIN is the backstop, so even a transient DB failure here
// cannot cause a burned sender to be picked — we log and move on rather than
// redeliver indefinitely.
func (e *Engine) HandleSessionBurned(ctx context.Context, ev *hermesv1.MbsSessionLifecycleEvent) bool {
	if ev == nil {
		return true
	}
	uid := ev.GetUid()
	if uid == 0 {
		e.log.Warn().Msg("mbs burned: event has zero uid, dropping")
		return true
	}

	affected, err := e.store.MarkMbsSenderBurned(ctx, uid)
	if err != nil {
		e.log.Error().Err(err).Int64("uid", uid).
			Msg("mbs burned: failed to disable sender (selection JOIN is the backstop)")
		return true // Ack anyway; JOIN masks it at pick time
	}
	if affected > 0 {
		e.log.Info().Int64("uid", uid).Int64("senders_disabled", affected).
			Str("reason", ev.GetReason()).
			Msg("mbs burned: disabled sender across campaigns")
	}
	return true
}
