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
			// All contacts processed — mark completed.
			if _, err := e.store.UpdateCampaignStatus(ctx, campaignID, "completed", false, true); err != nil {
				e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("mbs: failed to mark campaign completed")
			}
			e.publishStatusEvent(tenantID, workspaceID, campaignID,
				hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING,
				hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED, "completed")
			e.log.Info().Str("campaign_id", campaignID).Int32("dispatched", dispatched).Msg("mbs: campaign completed")
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
				e.log.Warn().Str("campaign_id", campaignID).Msg("mbs: all sessions exhausted, stopping dispatch")
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

			// Update DB state (best-effort, mirrors WA behavior — errors
			// logged by the store layer, don't halt the loop).
			_ = e.store.UpdateContactSentMbs(ctx, campaignID, contact.ContactID, uid)
			_ = e.store.IncrementSentCount(ctx, campaignID)
			_ = e.store.IncrementMbsSessionSentCount(ctx, campaignID, uid)

			dispatched++

			// Publish progress every 10 sends or 5 seconds — same cadence as WA.
			if dispatched%10 == 0 || time.Since(lastProgress) >= 5*time.Second {
				e.publishProgress(tenantID, workspaceID, campaignID, totalContacts, dispatched, startTime)
				lastProgress = time.Now()
			}
		}
	}
}
