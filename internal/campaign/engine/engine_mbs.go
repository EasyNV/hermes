package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/campaign/handler"
	"github.com/hermes-waba/hermes/internal/campaign/spintax"
)

// dispatchMbsLoop is the MBS-channel dispatcher (chunk 9). Sister of
// dispatchWaLoop. Rotates across campaign_senders WHERE sender_kind='mbs',
// builds MbsCampaignSendTask, publishes to hermes.mbs.send.campaign.{tenant}.
//
// The hermes-mbs consumer (cmd/mbs/send_consumers.go) is already subscribed
// and dispatches via handler.SendMessage with tenant injected from the
// subject suffix and idempotency via IdempotencyKey → ClientDedupeId.
//
// Contract (see plan chunk-9 C9-*):
//   - Subject: hermes.mbs.send.campaign.<tenant_id> exactly.
//   - IdempotencyKey: campaignID + ":" + contactID (deterministic).
//   - RecipientPhone: E.164 minus leading "+".
//   - On every send: campaign_contacts.{status='sent', mbs_session_uid, sent_at=now()}
//     + campaigns.sent_count++ + campaign_senders.sent_count++ for sender_kind='mbs'.
func (e *Engine) dispatchMbsLoop(ctx context.Context, campaign *handler.CampaignRow, tmpl *handler.TemplateRow, tenantID, workspaceID string) {
	campaignID := campaign.ID

	var rotator MbsRotator
	switch campaign.RotationStrategy {
	case "least_used":
		rotator = NewLeastUsedMbs()
	default:
		rotator = NewRoundRobinMbs()
	}

	totalContacts := campaign.TotalContacts
	dispatched := campaign.SentCount // resume from where we left off
	batchSize := int32(100)
	lastProgress := time.Now()
	startTime := time.Now()

	for {
		if ctx.Err() != nil {
			return
		}

		contacts, err := e.store.GetPendingContacts(ctx, campaignID, batchSize)
		if err != nil {
			e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("mbs: failed to fetch pending contacts")
			return
		}

		if len(contacts) == 0 {
			// Dispatch done: all contacts drained from 'pending' to 'queued'.
			// Bug-2 fix: do NOT mark the campaign 'completed' here. Completion
			// now means "no pending AND no queued" and is owned by the result
			// consumer (HandleMbsResult), which re-checks after each terminal
			// write-back. The safety reaper guarantees 'queued' always drains,
			// so the campaign can't hang in 'running' forever.
			e.log.Info().Str("campaign_id", campaignID).Int32("dispatched", dispatched).
				Msg("mbs: dispatch complete, awaiting send results")
			return
		}

		for _, contact := range contacts {
			if ctx.Err() != nil {
				return
			}

			// Refresh session states each iteration so rotation decisions
			// reflect concurrent updates (other dispatches, ops manual
			// status flips). Mirrors WA path exactly.
			sessions, err := e.store.GetActiveCampaignMbsSessions(ctx, campaignID)
			if err != nil {
				e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("mbs: failed to fetch active sessions")
				return
			}

			infos := make([]MbsSessionInfo, 0, len(sessions))
			for _, s := range sessions {
				infos = append(infos, MbsSessionInfo{
					UID:       s.UID,
					SentToday: s.SentCount,
					Status:    s.Status,
				})
			}

			uid, ok := rotator.Next(infos, campaign.DailyCapPerNum)
			if !ok {
				// No live sender available (all burned/exhausted). Bug-1
				// follow-through: pause the campaign with an operator-visible
				// reason instead of silently returning and leaving contacts
				// stuck 'pending' forever. Resumable once a healthy sender is
				// added — contacts are untouched (still 'pending').
				e.log.Warn().Str("campaign_id", campaignID).Msg("mbs: no active sessions, pausing campaign")
				if _, err := e.store.UpdateCampaignStatus(ctx, campaignID, "paused", false, false); err != nil {
					e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("mbs: failed to pause campaign")
				}
				e.publishStatusEvent(tenantID, workspaceID, campaignID,
					hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING,
					hermesv1.CampaignStatus_CAMPAIGN_STATUS_PAUSED, "no active MBS senders")
				return
			}

			// Build variables map — same shape as WA (name, phone, custom_fields).
			vars := map[string]string{
				"name":  contact.Name,
				"phone": contact.Phone,
			}
			for k, v := range contact.CustomFields {
				vars[k] = v
			}

			resolvedBody := spintax.Resolve(tmpl.Body)
			resolvedBody = spintax.SubstituteVariables(resolvedBody, vars)

			_, _ = json.Marshal(vars) // future: include in MbsCampaignSendTask if proto adds the field

			// E.164 minus leading "+" per events.proto:431 — the consumer
			// passes this verbatim to BizInboxWhatsAppCustomerMutation
			// which expects unprefixed digits.
			recipientPhone := strings.TrimPrefix(contact.Phone, "+")

			task := &hermesv1.MbsCampaignSendTask{
				Meta: &hermesv1.EventMeta{
					EventId:   uuid.New().String(),
					TenantId:  tenantID,
					Timestamp: timestamppb.Now(),
					Source:    "hermes-campaign",
				},
				CampaignId:     campaignID,
				ContactId:      contact.ContactID,
				Uid:            uid,
				ThreadId:       "", // let hermes-mbs resolve via BizInboxWhatsAppCustomerMutation
				RecipientPhone: recipientPhone,
				ResolvedBody:   resolvedBody,
				PageIdOverride: "", // future: per-campaign page override
				IdempotencyKey: campaignID + ":" + contact.ContactID,
			}

			if e.js != nil {
				data, err := proto.Marshal(task)
				if err != nil {
					e.log.Error().Err(err).Msg("mbs: failed to marshal send task")
					continue
				}
				subject := fmt.Sprintf("hermes.mbs.send.campaign.%s", tenantID)
				if _, err := e.js.Publish(subject, data, natsgo.MsgId(task.Meta.EventId)); err != nil {
					e.log.Error().Err(err).
						Str("contact_id", contact.ContactID).
						Int64("uid", uid).
						Msg("mbs: failed to publish send task")
					continue
				}
			}

			// Bug-2 fix (close-the-loop): mark 'queued', NOT 'sent'. The
			// contact only becomes 'sent' (or 'failed') when the result
			// consumer receives the MbsOutboundEvent for this send. Counters
			// (campaigns.sent_count, campaign_senders.sent_count) likewise
			// move to the result consumer — incrementing them here would be
			// the same fire-and-forget lie we're fixing. The 'queued' write
			// is what keeps GetPendingContacts from re-pulling this contact
			// (no double-send). Guard is status='pending' inside the store
			// method for idempotency on redelivery/resume.
			//
			// sent_at is set to now() here as the dispatch timestamp; the
			// stuck-queued reaper keys off it. (Semantic overload of sent_at
			// as "last state change / dispatched_at" — documented in plan S7.)
			if err := e.store.UpdateContactQueuedMbs(ctx, campaignID, contact.ContactID, uid); err != nil {
				e.log.Error().Err(err).
					Str("contact_id", contact.ContactID).
					Int64("uid", uid).
					Msg("mbs: failed to mark contact queued")
			}

			dispatched++

			// Publish progress every 10 sends or 5 seconds — same cadence as WA.
			if dispatched%10 == 0 || time.Since(lastProgress) >= 5*time.Second {
				e.publishProgress(tenantID, workspaceID, campaignID, totalContacts, dispatched, startTime)
				lastProgress = time.Now()
			}
		}
	}
}
