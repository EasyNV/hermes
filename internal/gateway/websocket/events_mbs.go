package websocket

import (
	"strconv"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ─────────────────────────────────────────────────────────────────────
// MBS event subscribers
//
// Three NATS subjects, three handlers, three WS frame types:
//
//	hermes.mbs.message.inbound.<tenant>   → "mbs_new_message"
//	hermes.mbs.message.outbound.<tenant>  → "mbs_outbound_status"
//	hermes.mbs.session.<state>.<tenant>   → "mbs_session_lifecycle"
//
// All MBS events are tenant-scoped — there's no workspace dimension
// (MBS sessions belong to a tenant). Broadcast with workspaceID="" so
// every WS client in the tenant receives the update, regardless of
// which workspace they're currently viewing.
//
// Uid is serialized as a decimal STRING on the wire to defend against
// JavaScript's 2^53 safe-integer limit. Meta uids are int64; we treat
// them as opaque strings end-to-end. Chunk-4 TS types declare
// `uid: string` to match.
//
// Frame type names match what chunk-4 frontend types expect — single
// source of truth lives here in the gateway WS layer.
// ─────────────────────────────────────────────────────────────────────

// handleMbsInboundMessage handles hermes.mbs.message.inbound.{tenant}.
// Publishes "mbs_new_message" WS event to tenant clients.
//
// Frame schema (matches chunk-4 WsMbsNewMessagePayload):
//
//	{
//	  "uid":          "1674772559",
//	  "pageId":       "...",
//	  "wecMailboxId": "...",
//	  "threadId":     "...",
//	  "mid":          "mid.$cAAAA...",
//	  "senderPhone":  "62812...",
//	  "text":         "hello",
//	  "receivedAt":   "2026-05-29T12:00:00Z"
//	}
func (s *EventSubscriber) handleMbsInboundMessage(msg *natsgo.Msg) {
	var ev hermesv1.MbsInboundMessageEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal MbsInboundMessageEvent")
		_ = msg.Ack()
		return
	}
	tenantID := extractTenantID(ev.GetMeta())
	if tenantID == "" {
		s.log.Warn().Str("subject", msg.Subject).Msg("mbs inbound: empty tenant_id (publisher misconfig)")
	}

	payload := map[string]any{
		"uid":          strconv.FormatInt(ev.GetUid(), 10),
		"pageId":       ev.GetPageId(),
		"wecMailboxId": ev.GetWecMailboxId(),
		"threadId":     ev.GetThreadId(),
		"mid":          ev.GetMid(),
		"senderPhone":  ev.GetSenderPhone(),
		"text":         ev.GetText(),
		"receivedAt":   protoToISO(ev.GetMetaTimestamp(), extractTimestamp(ev.GetMeta())),
	}
	data := marshalWSEvent("mbs_new_message", payload)
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// handleMbsOutboundStatus handles hermes.mbs.message.outbound.{tenant}.
// Publishes "mbs_outbound_status" WS event for the inbox composer's
// optimistic-state reconciliation (success → green check; failure →
// red exclamation + error message).
//
// Frame schema (matches chunk-4 WsMbsOutboundStatusPayload):
//
//	{
//	  "uid":       "1674772559",
//	  "threadId":  "...",
//	  "mid":       "mid.$cAAAA...",
//	  "otid":      "1717938947123456789",
//	  "latencyMs": 423,
//	  "ok":        true,
//	  "error":     "",
//	  "sentAt":    "2026-05-29T12:00:00Z"
//	}
func (s *EventSubscriber) handleMbsOutboundStatus(msg *natsgo.Msg) {
	var ev hermesv1.MbsOutboundEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal MbsOutboundEvent")
		_ = msg.Ack()
		return
	}
	tenantID := extractTenantID(ev.GetMeta())
	if tenantID == "" {
		s.log.Warn().Str("subject", msg.Subject).Msg("mbs outbound: empty tenant_id (publisher misconfig)")
	}

	payload := map[string]any{
		"uid":       strconv.FormatInt(ev.GetUid(), 10),
		"threadId":  ev.GetThreadId(),
		"mid":       ev.GetMid(),
		"otid":      ev.GetOtid(),
		"latencyMs": ev.GetLatencyMs(),
		"ok":        ev.GetOk(),
		"error":     ev.GetError(),
		"sentAt":    protoToISO(ev.GetSentAt(), extractTimestamp(ev.GetMeta())),
	}
	data := marshalWSEvent("mbs_outbound_status", payload)
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// handleMbsSessionLifecycle handles hermes.mbs.session.{state}.{tenant}.
// Publishes "mbs_session_lifecycle" WS event so the Pages list and any
// open session-detail drawer can reflect state transitions in real time.
//
// The {state} subject token mirrors `reason` for routability, but
// the canonical state values are in the proto fields (`previousState`,
// `newState`). The frontend reads those, not the subject.
//
// Frame schema (matches chunk-4 WsMbsSessionLifecyclePayload):
//
//	{
//	  "uid":           "1674772559",
//	  "previousState": "MBS_SESSION_STATE_ACTIVE",
//	  "newState":      "MBS_SESSION_STATE_BURNED",
//	  "reason":        "checkpoint_required",
//	  "lastConnackRc": 0,
//	  "podId":         "hermes-mbs-0",
//	  "timestamp":     "2026-05-29T12:00:00Z"
//	}
func (s *EventSubscriber) handleMbsSessionLifecycle(msg *natsgo.Msg) {
	var ev hermesv1.MbsSessionLifecycleEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal MbsSessionLifecycleEvent")
		_ = msg.Ack()
		return
	}
	tenantID := extractTenantID(ev.GetMeta())
	if tenantID == "" {
		s.log.Warn().Str("subject", msg.Subject).Msg("mbs lifecycle: empty tenant_id (publisher misconfig)")
	}

	payload := map[string]any{
		"uid":           strconv.FormatInt(ev.GetUid(), 10),
		"previousState": ev.GetPreviousState().String(),
		"newState":      ev.GetNewState().String(),
		"reason":        ev.GetReason(),
		"lastConnackRc": ev.GetLastConnackRc(),
		"podId":         ev.GetPodId(),
		"timestamp":     extractTimestamp(ev.GetMeta()),
	}
	data := marshalWSEvent("mbs_session_lifecycle", payload)
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// protoToISO formats a protobuf timestamp as ISO 8601 (RFC 3339, UTC),
// falling back to the supplied default when the timestamp is nil.
//
// Used because MBS events carry event-specific timestamps in dedicated
// fields (`meta_timestamp` on inbound, `sent_at` on outbound) rather
// than always relying on the EventMeta.timestamp. When the
// event-specific field is absent (older publishers, partial data) we
// fall back to the meta timestamp, which extractTimestamp already
// guards with time.Now() of last resort.
func protoToISO(ts *timestamppb.Timestamp, fallback string) string {
	if ts == nil {
		return fallback
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}
