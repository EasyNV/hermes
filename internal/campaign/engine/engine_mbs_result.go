package engine

import (
	"context"
	"strings"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// HandleMbsResult processes one MbsOutboundEvent (published by hermes-mbs on
// EVERY send attempt — success and failure — to hermes.mbs.message.outbound.*).
// This is the consumer half of the close-the-loop fix: it turns the open-loop
// "publish task → mark sent → hope" pipeline into a real feedback loop.
//
// Correlation: client_dedupe_id carries the originating
// MbsCampaignSendTask.idempotency_key = "<campaignID>:<contactID>".
//
// Idempotency: every write-back is guarded on status='queued' at the SQL
// layer, so duplicate/redelivered events and the NAK-storm's repeated ok=false
// events are absorbed — only the genuine first terminal transition bumps a
// counter or fires a completion check.
//
// Returns true if the message should be Ack'd (always, in practice — we never
// want to redeliver a result event; a transient DB error is logged and Ack'd
// because the reaper is the backstop for any contact left stuck in 'queued').
func (e *Engine) HandleMbsResult(ctx context.Context, ev *hermesv1.MbsOutboundEvent) bool {
	if ev == nil {
		return true
	}

	campaignID, contactID, ok := parseDedupeKey(ev.GetClientDedupeId())
	if !ok {
		// Not a campaign send (manual/inbox send, or legacy event with no
		// idempotency key). Inbox owns manual correlation — ignore + Ack.
		return true
	}

	// Confirm this is a campaign we know about; fetch workspace/tenant for the
	// completion status event. A result for an unknown/deleted campaign is a
	// drop-and-Ack (poison), not a retry.
	campaign, err := e.store.GetCampaign(ctx, campaignID)
	if err != nil || campaign == nil {
		e.log.Warn().Err(err).
			Str("campaign_id", campaignID).
			Str("contact_id", contactID).
			Msg("mbs result: unknown campaign, dropping")
		return true
	}

	tenantID := ev.GetMeta().GetTenantId()
	if tenantID == "" {
		// Fall back to the workspace→tenant mapping if the event omitted it.
		if t, terr := e.store.GetWorkspaceTenantID(ctx, campaign.WorkspaceID); terr == nil {
			tenantID = t
		}
	}

	if ev.GetOk() {
		affected, uerr := e.store.UpdateContactSentFromResult(ctx, campaignID, contactID)
		if uerr != nil {
			e.log.Error().Err(uerr).Str("campaign_id", campaignID).Str("contact_id", contactID).
				Msg("mbs result: failed to mark contact sent")
			return true // Ack anyway; reaper is the backstop
		}
		if affected == 1 {
			// Genuine first transition — bump counters now (moved here from
			// the fire-and-forget dispatch path).
			if cerr := e.store.IncrementSentCount(ctx, campaignID); cerr != nil {
				e.log.Error().Err(cerr).Str("campaign_id", campaignID).Msg("mbs result: IncrementSentCount")
			}
			if cerr := e.store.IncrementMbsSessionSentCount(ctx, campaignID, ev.GetUid()); cerr != nil {
				e.log.Error().Err(cerr).Str("campaign_id", campaignID).Int64("uid", ev.GetUid()).
					Msg("mbs result: IncrementMbsSessionSentCount")
			}
			e.log.Info().Str("campaign_id", campaignID).Str("contact_id", contactID).Int64("uid", ev.GetUid()).
				Msg("mbs result: contact sent")
		}
	} else {
		affected, uerr := e.store.UpdateContactFailedFromResult(ctx, campaignID, contactID, ev.GetError())
		if uerr != nil {
			e.log.Error().Err(uerr).Str("campaign_id", campaignID).Str("contact_id", contactID).
				Msg("mbs result: failed to mark contact failed")
			return true
		}
		if affected == 1 {
			if cerr := e.store.IncrementFailedCount(ctx, campaignID); cerr != nil {
				e.log.Error().Err(cerr).Str("campaign_id", campaignID).Msg("mbs result: IncrementFailedCount")
			}
			e.log.Warn().Str("campaign_id", campaignID).Str("contact_id", contactID).Int64("uid", ev.GetUid()).
				Str("error", ev.GetError()).Msg("mbs result: contact failed")
		}
	}

	// Re-check completion after every terminal transition. Completion =
	// no pending AND no queued. Idempotent: marking an already-completed
	// campaign 'completed' is a harmless no-op, and we guard the status-event
	// publish on the campaign not already being terminal.
	e.maybeCompleteCampaign(ctx, campaignID, campaign.WorkspaceID, tenantID)
	return true
}

// maybeCompleteCampaign marks a campaign 'completed' and publishes the status
// event iff no contacts remain pending or queued. Safe to call repeatedly.
func (e *Engine) maybeCompleteCampaign(ctx context.Context, campaignID, workspaceID, tenantID string) {
	pending, queued, err := e.store.CountInflightContacts(ctx, campaignID)
	if err != nil {
		e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("mbs: completion check failed")
		return
	}
	if pending > 0 || queued > 0 {
		return // still in flight
	}

	// All terminal. Flip to completed, but only publish the completion event if
	// THIS call actually performed the running→completed transition. A
	// redelivered/raced terminal result (or a result/reaper race) must not
	// re-publish. CompleteCampaignIfRunning's UPDATE is guarded on
	// status<>'completed', so the second caller gets transitioned=false.
	transitioned, err := e.store.CompleteCampaignIfRunning(ctx, campaignID)
	if err != nil {
		e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("mbs: failed to mark campaign completed")
		return
	}
	if !transitioned {
		return // already completed — no duplicate completion event
	}
	e.publishStatusEvent(tenantID, workspaceID, campaignID,
		hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING,
		hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED, "completed")
	e.log.Info().Str("campaign_id", campaignID).Msg("mbs: campaign completed (all results in)")
}

// ReapStuckQueued is invoked on a ticker by hermes-campaign. It transitions
// any contact stuck in 'queued' past the timeout to 'failed' (the result event
// never arrived — poison-Ack'd task, mbs crash mid-send, etc.), bumps
// failed_count per affected campaign, and re-checks completion so a campaign
// can't hang in 'running' forever. olderThan is the stuck threshold (e.g. 5m).
func (e *Engine) ReapStuckQueued(ctx context.Context, olderThan time.Duration) {
	reaped, err := e.store.ReapStuckQueuedMbs(ctx, olderThan)
	if err != nil {
		e.log.Error().Err(err).Msg("mbs reaper: failed to reap stuck queued contacts")
		return
	}
	if len(reaped) == 0 {
		return
	}

	// Count per campaign so we bump failed_count the right number of times and
	// only re-check completion once per affected campaign.
	perCampaign := make(map[string]int, len(reaped))
	for _, rc := range reaped {
		perCampaign[rc.CampaignID]++
	}

	for campaignID, n := range perCampaign {
		e.log.Warn().Str("campaign_id", campaignID).Int("count", n).
			Msg("mbs reaper: timed out stuck 'queued' contacts -> failed")
		for i := 0; i < n; i++ {
			if cerr := e.store.IncrementFailedCount(ctx, campaignID); cerr != nil {
				e.log.Error().Err(cerr).Str("campaign_id", campaignID).Msg("mbs reaper: IncrementFailedCount")
			}
		}
		// Workspace/tenant for the completion event.
		var workspaceID, tenantID string
		if c, gerr := e.store.GetCampaign(ctx, campaignID); gerr == nil && c != nil {
			workspaceID = c.WorkspaceID
			if t, terr := e.store.GetWorkspaceTenantID(ctx, workspaceID); terr == nil {
				tenantID = t
			}
		}
		e.maybeCompleteCampaign(ctx, campaignID, workspaceID, tenantID)
	}
}

// parseDedupeKey splits a "<campaignID>:<contactID>" dedupe id. Both halves
// must be non-empty. Returns ok=false for any other shape (empty, no colon,
// extra colons) so non-campaign sends are cleanly ignored.
func parseDedupeKey(b []byte) (campaignID, contactID string, ok bool) {
	if len(b) == 0 {
		return "", "", false
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
