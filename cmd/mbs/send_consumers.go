package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/handler"
)

// ─── Consumer tunables (chunk-6 hard-coded; promote to env if pain) ──
//
// AckWait caps how long a consumer instance has to call Ack/Nak before
// NATS redelivers. 60s is the hermes-wa convention and comfortably
// covers our worst-case SendMessage path (warmup + bootstrap + send,
// typically <5s). If we observe consistent redelivery on slow days,
// promote to env.
const (
	consumerAckWait     = 60 * time.Second
	consumerMaxDeliver  = 5
	consumerSendTimeout = 30 * time.Second
)

// jetStreamSubscriber is the slice of natsgo.JetStreamContext that
// our consumer wiring uses. Defined narrow so tests can inject a
// fake. Real production passes a *natsgo.JetStreamContext.
type jetStreamSubscriber interface {
	Subscribe(subject string, cb natsgo.MsgHandler, opts ...natsgo.SubOpt) (*natsgo.Subscription, error)
}

const (
	campaignSendSubject = "hermes.mbs.send.campaign.*"
	manualSendSubject   = "hermes.mbs.send.manual.*"
)

// startCampaignConsumer subscribes to hermes.mbs.send.campaign.* and
// dispatches each task by calling handler.SendMessage directly.
//
// Rationale: we already have the RPC. Reusing it from the NATS
// consumer keeps tenant cross-check, dedupe cache, metrics, and
// resolver logic in one place. The interceptor doesn't fire (no
// real gRPC stream), so we inject the tenant on the context
// manually — the tenant comes from a trusted NATS subject suffix,
// not from an attacker-controlled metadata header.
//
// Subject shape: hermes.mbs.send.campaign.<tenant_id>
//
// Per-message flow:
//  1. Parse tenant from subject. Malformed subject → Ack (drop poison).
//  2. proto-unmarshal MbsCampaignSendTask. Bad proto → Ack (drop poison).
//  3. Build MbsSendMessageRequest (oneof Recipient = thread_id or phone).
//  4. Inject tenant on ctx, call h.SendMessage with a bounded timeout.
//  5. On error → Nak (will redeliver up to MaxDeliver). On ok → Ack.
//
// Idempotency: chunk-4's dedupe cache keys on (uid, client_dedupe_id).
// We pass task.IdempotencyKey as the dedupe id so a redelivered
// message short-circuits and returns the prior result without
// double-sending.
func startCampaignConsumer(js jetStreamSubscriber, h *handler.Handler, log zerolog.Logger) error {
	if _, err := js.Subscribe(
		campaignSendSubject,
		makeSendHandler("campaign", h, log),
		natsgo.Durable("mbs-campaign-send"),
		natsgo.ManualAck(),
		natsgo.AckWait(consumerAckWait),
		natsgo.MaxDeliver(consumerMaxDeliver),
	); err != nil {
		return fmt.Errorf("subscribe campaign send: %w", err)
	}
	log.Info().
		Str("subject", campaignSendSubject).
		Str("durable", "mbs-campaign-send").
		Msg("campaign consumer started")
	return nil
}

// startManualConsumer is the symmetric impl for inbox-agent-initiated
// sends. Subject: hermes.mbs.send.manual.<tenant>. Same task type for
// v1 — gateway/inbox emit MbsCampaignSendTask with campaign_id="".
// When manual sends grow agent-attribution fields, add a typed
// MbsManualSendTask in the proto schema.
func startManualConsumer(js jetStreamSubscriber, h *handler.Handler, log zerolog.Logger) error {
	if _, err := js.Subscribe(
		manualSendSubject,
		makeSendHandler("manual", h, log),
		natsgo.Durable("mbs-manual-send"),
		natsgo.ManualAck(),
		natsgo.AckWait(consumerAckWait),
		natsgo.MaxDeliver(consumerMaxDeliver),
	); err != nil {
		return fmt.Errorf("subscribe manual send: %w", err)
	}
	log.Info().
		Str("subject", manualSendSubject).
		Str("durable", "mbs-manual-send").
		Msg("manual consumer started")
	return nil
}

// makeSendHandler returns a natsgo.MsgHandler closure that drives one
// MbsCampaignSendTask through h.SendMessage. The `source` label is
// "campaign" or "manual" — included in every log line so operators
// can grep one consumer's behavior in isolation.
func makeSendHandler(source string, h *handler.Handler, log zerolog.Logger) natsgo.MsgHandler {
	return func(msg *natsgo.Msg) {
		tenant, err := tenantFromSubject(msg.Subject)
		if err != nil {
			log.Error().Err(err).
				Str("source", source).
				Str("subject", msg.Subject).
				Msg("send consumer: bad subject — dropping poison")
			_ = msg.Ack()
			return
		}

		var task hermesv1.MbsCampaignSendTask
		if err := proto.Unmarshal(msg.Data, &task); err != nil {
			log.Error().Err(err).
				Str("source", source).
				Str("tenant", tenant).
				Msg("send consumer: bad proto — dropping poison")
			_ = msg.Ack()
			return
		}

		req, err := buildSendRequestFromTask(&task)
		if err != nil {
			log.Error().Err(err).
				Str("source", source).
				Str("tenant", tenant).
				Int64("uid", task.Uid).
				Msg("send consumer: task validation failed — dropping poison")
			_ = msg.Ack()
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), consumerSendTimeout)
		defer cancel()
		ctx = handler.WithTenantForTest(ctx, tenant)

		if _, err := h.SendMessage(ctx, req); err != nil {
			// Bug-3 fix (close-the-loop): classify before redelivering. The
			// handler ALREADY published an MbsOutboundEvent(ok=false) for this
			// attempt (rpc_send_message.go step 5), so the campaign engine
			// learns of the failure regardless of Ack/Nak/Term — redelivery is
			// purely about whether a RETRY could plausibly succeed.
			//
			//   Permanent (banned session, bad creds, validation, tenant
			//   mismatch, not-found) → Term(): no point hammering a banned
			//   account 5×. This kills the NAK storm + OPSEC fingerprint.
			//   Transient (network, timeout, MQTToT close, 5xx) → Nak():
			//   bounded by MaxDeliver=5.
			if permanentSendErr(err) {
				log.Warn().Err(err).
					Str("source", source).
					Str("tenant", tenant).
					Int64("uid", task.Uid).
					Str("campaign_id", task.CampaignId).
					Msg("send consumer: permanent SendMessage failure — Term (no redelivery)")
				_ = msg.Term()
				return
			}
			log.Warn().Err(err).
				Str("source", source).
				Str("tenant", tenant).
				Int64("uid", task.Uid).
				Str("campaign_id", task.CampaignId).
				Msg("send consumer: transient SendMessage failure — NAK for redelivery")
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	}
}

// permanentSendErr classifies a SendMessage error as permanent (no retry can
// help) vs transient (worth a bounded redelivery). Keys off the gRPC status
// code set by the handler's mapSendErr/mapStoreErr/mapClientErr:
//
//   - InvalidArgument, NotFound, PermissionDenied, Unauthenticated,
//     FailedPrecondition, Unimplemented → permanent. A banned/burned session
//     surfaces as FailedPrecondition/PermissionDenied (OAuthException 190/464);
//     a deleted session as NotFound; a malformed task as InvalidArgument.
//   - Unavailable, DeadlineExceeded, Internal, Aborted, ResourceExhausted, and
//     unknown/non-status errors → transient (default to retry; the bounded
//     MaxDeliver cap + reaper prevent runaway).
func permanentSendErr(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument,
		codes.NotFound,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition,
		codes.Unimplemented:
		return true
	default:
		return false
	}
}

// buildSendRequestFromTask projects a MbsCampaignSendTask into a
// MbsSendMessageRequest, picking the right Recipient oneof and
// wiring task.IdempotencyKey as ClientDedupeId so the handler's
// dedupe cache short-circuits redelivered tasks.
//
// Returns error if the task is structurally invalid (missing uid,
// missing both thread_id and recipient_phone, missing body).
func buildSendRequestFromTask(task *hermesv1.MbsCampaignSendTask) (*hermesv1.MbsSendMessageRequest, error) {
	if task.Uid == 0 {
		return nil, errors.New("task: uid is required")
	}
	if task.ResolvedBody == "" {
		return nil, errors.New("task: resolved_body is empty")
	}

	req := &hermesv1.MbsSendMessageRequest{
		Uid:            task.Uid,
		Text:           task.ResolvedBody,
		PageIdOverride: task.PageIdOverride,
	}
	if task.IdempotencyKey != "" {
		req.ClientDedupeId = []byte(task.IdempotencyKey)
	}

	switch {
	case task.ThreadId != "":
		req.Recipient = &hermesv1.MbsSendMessageRequest_ThreadId{ThreadId: task.ThreadId}
	case task.RecipientPhone != "":
		req.Recipient = &hermesv1.MbsSendMessageRequest_Phone{Phone: task.RecipientPhone}
	default:
		return nil, errors.New("task: neither thread_id nor recipient_phone set")
	}
	return req, nil
}

// tenantFromSubject parses the trailing tenant token from a NATS
// subject of the form "hermes.mbs.send.<kind>.<tenant>". Returns
// error on malformed input — callers drop the message (Ack) instead
// of looping via Nak.
//
// We accept any non-empty tenant token; multi-tenant deployments
// stamp UUIDs or slugs here, neither of which we want to validate
// against a hard regex (operators occasionally test with
// "dev-shared", etc.).
func tenantFromSubject(subject string) (string, error) {
	const prefix = "hermes.mbs.send."
	if !strings.HasPrefix(subject, prefix) {
		return "", fmt.Errorf("subject %q missing prefix %q", subject, prefix)
	}
	rest := strings.TrimPrefix(subject, prefix)
	// rest = "<kind>.<tenant>"
	parts := strings.SplitN(rest, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("subject %q missing kind or tenant token", subject)
	}
	if strings.Contains(parts[1], ".") {
		// tenant must not contain dots (NATS hierarchical subjects)
		return "", fmt.Errorf("subject %q tenant token contains %q", subject, ".")
	}
	return parts[1], nil
}
