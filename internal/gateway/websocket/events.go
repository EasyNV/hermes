package websocket

import (
	"encoding/json"
	"fmt"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// EventSubscriber — bridges NATS events to WebSocket clients via the Hub
// ---------------------------------------------------------------------------

// Broadcaster is the subset of *Hub the EventSubscriber needs to fan
// out messages. Defined as an interface so tests can inject a recorder
// without spinning up a real WebSocket hub. *Hub satisfies it by
// virtue of having all three methods; production callers (cmd/gateway/
// main.go) pass *Hub and Go's structural typing handles the rest.
type Broadcaster interface {
	Broadcast(tenantID, workspaceID string, data []byte)
	BroadcastToUser(userID string, data []byte)
	BroadcastToConversation(conversationID string, data []byte)
	BroadcastConversationScoped(tenantID, workspaceID, assignedTo string, data []byte)
}

// EventSubscriber subscribes to NATS JetStream subjects and fans out events
// to WebSocket clients through the Hub.
type EventSubscriber struct {
	hub  Broadcaster
	js   natsgo.JetStreamContext
	log  zerolog.Logger
	subs []*natsgo.Subscription
}

// NewEventSubscriber creates a new subscriber that translates NATS events
// into JSON WebSocket messages and pushes them through the hub.
//
// `hub` is typed as *Hub at the constructor signature (production
// behavior) but stored as the Broadcaster interface so test code can
// inject a recorder via the unexported field.
func NewEventSubscriber(hub *Hub, js natsgo.JetStreamContext, log zerolog.Logger) *EventSubscriber {
	return &EventSubscriber{
		hub: hub,
		js:  js,
		log: log.With().Str("component", "ws-events").Logger(),
	}
}

// ---------------------------------------------------------------------------
// Start — subscribe to all NATS subjects the gateway consumes
// ---------------------------------------------------------------------------

// Start subscribes to all relevant NATS JetStream subjects and starts
// processing events. Call this once during gateway startup.
func (s *EventSubscriber) Start() error {
	type sub struct {
		subject  string
		durable  string
		handler  natsgo.MsgHandler
		maxDeliv int
		ackWait  time.Duration
	}

	subscriptions := []sub{
		{
			subject:  "hermes.wa.message.inbound.*",
			durable:  "gateway-inbound",
			handler:  s.handleInboundMessage,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.wa.message.outbound.*",
			durable:  "gateway-outbound",
			handler:  s.handleOutboundStatus,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.wa.connection.*",
			durable:  "gateway-connection",
			handler:  s.handleConnection,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.wa.ban.*",
			durable:  "gateway-ban",
			handler:  s.handleBan,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.campaign.status.*",
			durable:  "gateway-campaign-status",
			handler:  s.handleCampaignStatus,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.campaign.progress.*",
			durable:  "gateway-campaign-progress",
			handler:  s.handleCampaignProgress,
			maxDeliv: 1,
			ackWait:  5 * time.Second,
		},
		{
			subject:  "hermes.contacts.import.done.*",
			durable:  "gateway-import-done",
			handler:  s.handleImportDone,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.wa.presence.*",
			durable:  "gateway-presence",
			handler:  s.handlePresence,
			maxDeliv: 1,
			ackWait:  5 * time.Second,
		},
		// ─── MBS subscriptions (chunk E2.3) ──────────────────────────
		// Publish subjects from internal/mbs/handler/events.go:
		//   hermes.mbs.message.inbound.<tenant>   (4 tokens)
		//   hermes.mbs.message.outbound.<tenant>  (4 tokens)
		//   hermes.mbs.session.<state>.<tenant>   (4 tokens)
		// The session subject needs TWO wildcards to match state+tenant.
		{
			subject:  "hermes.mbs.message.inbound.*",
			durable:  "gateway-mbs-inbound",
			handler:  s.handleMbsInboundMessage,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		{
			subject:  "hermes.mbs.message.outbound.*",
			durable:  "gateway-mbs-outbound",
			handler:  s.handleMbsOutboundStatus,
			// Status updates are idempotent and replayable by the client
			// asking for fresh state; don't waste deliveries on retries.
			maxDeliv: 1,
			ackWait:  5 * time.Second,
		},
		{
			subject:  "hermes.mbs.session.*.*",
			durable:  "gateway-mbs-session",
			handler:  s.handleMbsSessionLifecycle,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
		// ─── Inbox enriched inbound (RBAC WS scoping) ────────────────
		// hermes.inbox.message.new.<tenant> carries conversation ownership
		// (conversation_id, workspace_id, assigned_to) so message fan-out can
		// be scoped per the conversation ownership model. This SUPERSEDES the
		// tenant-wide "new_message"/"mbs_new_message" fan-out from the raw
		// wa/mbs inbound handlers, which now only drive non-message UX.
		{
			subject:  "hermes.inbox.message.new.*",
			durable:  "gateway-inbox-scoped",
			handler:  s.handleInboxMessageNew,
			maxDeliv: 3,
			ackWait:  10 * time.Second,
		},
	}

	for _, cfg := range subscriptions {
		natsSub, err := s.js.Subscribe(
			cfg.subject,
			cfg.handler,
			natsgo.Durable(cfg.durable),
			natsgo.ManualAck(),
			natsgo.AckWait(cfg.ackWait),
			natsgo.MaxDeliver(cfg.maxDeliv),
		)
		if err != nil {
			return fmt.Errorf("subscribing to %s: %w", cfg.subject, err)
		}
		s.subs = append(s.subs, natsSub)
		s.log.Info().Str("subject", cfg.subject).Str("durable", cfg.durable).Msg("subscribed")
	}

	return nil
}

// Stop drains all NATS subscriptions.
func (s *EventSubscriber) Stop() {
	for _, sub := range s.subs {
		_ = sub.Drain()
	}
}

// ---------------------------------------------------------------------------
// NATS handlers — unmarshal proto, build JSON, fan out via hub
// ---------------------------------------------------------------------------

// handleInboundMessage handles hermes.wa.message.inbound.{tenant_id}.
// Publishes "new_message" WS event to all workspace clients.
func (s *EventSubscriber) handleInboundMessage(msg *natsgo.Msg) {
	var ev hermesv1.WaInboundMessageEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal WaInboundMessageEvent")
		_ = msg.Ack()
		return
	}

	// RBAC WS scoping: the "new_message" frame is now emitted by
	// handleInboxMessageNew (hermes.inbox.message.new.*), which carries the
	// conversation ownership fields needed to scope delivery. Broadcasting the
	// raw event tenant-wide here would (a) leak other agents' messages to a
	// cs_agent and (b) double-deliver. The raw handler now only ACKs; it is
	// retained as a JetStream consumer so the WA stream keeps draining.
	// (Reference a getter, not `_ = ev`, to avoid copying the proto lock — vet.)
	_ = ev.GetMeta()

	_ = msg.Ack()
}

// handleOutboundStatus handles hermes.wa.message.outbound.{tenant_id}.
// Publishes "message_status_updated" WS event.
func (s *EventSubscriber) handleOutboundStatus(msg *natsgo.Msg) {
	var ev hermesv1.WaOutboundStatusEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal WaOutboundStatusEvent")
		_ = msg.Ack()
		return
	}

	tenantID := extractTenantID(ev.GetMeta())

	payload := map[string]any{
		"wa_number_id":  ev.GetWaNumberId(),
		"wa_message_id": ev.GetWaMessageId(),
		"recipient_jid": ev.GetRecipientJid(),
		"status":        ev.GetStatus().String(),
		"error":         ev.GetError(),
	}
	if ev.GetWaTimestamp() != nil {
		payload["updated_at"] = ev.GetWaTimestamp().AsTime().UTC().Format(time.RFC3339)
	}

	data := marshalWSEvent("message_status_updated", payload)
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// handleConnection handles hermes.wa.connection.{tenant_id}.
// Publishes "number_status_changed" WS event to all tenant clients.
func (s *EventSubscriber) handleConnection(msg *natsgo.Msg) {
	var ev hermesv1.WaConnectionEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal WaConnectionEvent")
		_ = msg.Ack()
		return
	}

	tenantID := extractTenantID(ev.GetMeta())

	payload := map[string]any{
		"wa_number_id": ev.GetWaNumberId(),
		"phone":        ev.GetPhone(),
		"state":        ev.GetState().String(),
		"pod_id":       ev.GetPodId(),
		"reason":       ev.GetReason(),
		"timestamp":    extractTimestamp(ev.GetMeta()),
	}

	data := marshalWSEvent("number_status_changed", payload)
	// Connection events are tenant-scoped (all workspaces).
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// handleBan handles hermes.wa.ban.{tenant_id}.
// Publishes "ban_detected" WS event to all tenant clients.
func (s *EventSubscriber) handleBan(msg *natsgo.Msg) {
	var ev hermesv1.WaBanEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal WaBanEvent")
		_ = msg.Ack()
		return
	}

	tenantID := extractTenantID(ev.GetMeta())

	payload := map[string]any{
		"wa_number_id": ev.GetWaNumberId(),
		"phone":        ev.GetPhone(),
		"proxy_id":     ev.GetProxyId(),
		"pod_id":       ev.GetPodId(),
		"wa_reason":    ev.GetWaReason(),
	}
	if ev.GetDetectedAt() != nil {
		payload["detected_at"] = ev.GetDetectedAt().AsTime().UTC().Format(time.RFC3339)
	}

	data := marshalWSEvent("ban_detected", payload)
	// Ban events are tenant-scoped.
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// handleCampaignStatus handles hermes.campaign.status.{tenant_id}.
// Publishes "campaign_status_changed" WS event to workspace clients.
func (s *EventSubscriber) handleCampaignStatus(msg *natsgo.Msg) {
	var ev hermesv1.CampaignStatusEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal CampaignStatusEvent")
		_ = msg.Ack()
		return
	}

	tenantID := extractTenantID(ev.GetMeta())

	payload := map[string]any{
		"campaign_id":     ev.GetCampaignId(),
		"previous_status": ev.GetPreviousStatus().String(),
		"new_status":      ev.GetNewStatus().String(),
		"reason":          ev.GetReason(),
		"timestamp":       extractTimestamp(ev.GetMeta()),
	}

	data := marshalWSEvent("campaign_status_changed", payload)
	// Campaign events are workspace-scoped.
	s.hub.Broadcast(tenantID, ev.GetWorkspaceId(), data)

	_ = msg.Ack()
}

// handleCampaignProgress handles hermes.campaign.progress.{tenant_id}.
// Publishes "campaign_progress" WS event to workspace clients.
func (s *EventSubscriber) handleCampaignProgress(msg *natsgo.Msg) {
	var ev hermesv1.CampaignProgressEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal CampaignProgressEvent")
		_ = msg.Ack()
		return
	}

	tenantID := extractTenantID(ev.GetMeta())

	// Build per-number progress slice.
	numProgress := make([]map[string]any, 0, len(ev.GetNumberProgress()))
	for _, np := range ev.GetNumberProgress() {
		numProgress = append(numProgress, map[string]any{
			"wa_number_id": np.GetWaNumberId(),
			"phone":        np.GetPhone(),
			"status":       np.GetStatus().String(),
			"sent_count":   np.GetSentCount(),
			"failed_count": np.GetFailedCount(),
		})
	}

	payload := map[string]any{
		"campaign_id":      ev.GetCampaignId(),
		"total_contacts":   ev.GetTotalContacts(),
		"sent_count":       ev.GetSentCount(),
		"delivered_count":  ev.GetDeliveredCount(),
		"failed_count":     ev.GetFailedCount(),
		"replied_count":    ev.GetRepliedCount(),
		"banned_count":     ev.GetBannedCount(),
		"progress_percent": ev.GetProgressPercent(),
		"send_rate_per_min": ev.GetSendRatePerMin(),
		"eta_seconds":      ev.GetEtaSeconds(),
		"number_progress":  numProgress,
	}

	data := marshalWSEvent("campaign_progress", payload)
	s.hub.Broadcast(tenantID, ev.GetWorkspaceId(), data)

	_ = msg.Ack()
}

// handleImportDone handles hermes.contacts.import.done.{tenant_id}.
// Publishes "import_complete" WS event to the user who initiated the import.
func (s *EventSubscriber) handleImportDone(msg *natsgo.Msg) {
	var ev hermesv1.ContactsImportDoneEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal ContactsImportDoneEvent")
		_ = msg.Ack()
		return
	}

	payload := map[string]any{
		"filename":       ev.GetFilename(),
		"imported_count": ev.GetImportedCount(),
		"skipped_count":  ev.GetSkippedCount(),
		"updated_count":  ev.GetUpdatedCount(),
		"failed_count":   ev.GetFailedCount(),
	}

	data := marshalWSEvent("import_complete", payload)
	// Import events go only to the user who initiated the import.
	s.hub.BroadcastToUser(ev.GetImportedBy(), data)

	_ = msg.Ack()
}

// handlePresence handles hermes.wa.presence.{tenant_id} (Phase 3 stub).
// Publishes "typing_indicator" WS event to clients subscribed to the conversation.
func (s *EventSubscriber) handlePresence(msg *natsgo.Msg) {
	var ev hermesv1.WaPresenceEvent
	if err := proto.Unmarshal(msg.Data, &ev); err != nil {
		s.log.Error().Err(err).Msg("unmarshal WaPresenceEvent")
		_ = msg.Ack()
		return
	}

	payload := map[string]any{
		"wa_number_id": ev.GetWaNumberId(),
		"contact_jid":  ev.GetContactJid(),
		"is_composing": ev.GetIsComposing(),
	}

	data := marshalWSEvent("typing_indicator", payload)
	// Presence events are broadcast to the tenant; the frontend filters by
	// active conversation view. In Phase 3 this can be narrowed to
	// BroadcastToConversation once conversation_id is included in the event.
	tenantID := extractTenantID(ev.GetMeta())
	s.hub.Broadcast(tenantID, "", data)

	_ = msg.Ack()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractTenantID returns the tenant_id from an EventMeta, or empty string.
func extractTenantID(meta *hermesv1.EventMeta) string {
	if meta != nil {
		return meta.GetTenantId()
	}
	return ""
}

// extractTimestamp returns the ISO 8601 timestamp string from EventMeta.
func extractTimestamp(meta *hermesv1.EventMeta) string {
	if meta != nil && meta.GetTimestamp() != nil {
		return meta.GetTimestamp().AsTime().UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// marshalWSEvent creates a JSON-encoded WebSocket message with the given type
// and payload. If marshaling fails, it returns a minimal error envelope.
func marshalWSEvent(eventType string, payload map[string]any) []byte {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		// Fallback: return a minimal envelope.
		fallback, _ := json.Marshal(wsMessage{Type: eventType})
		return fallback
	}
	envelope, err := json.Marshal(wsMessage{
		Type:    eventType,
		Payload: payloadBytes,
	})
	if err != nil {
		fallback, _ := json.Marshal(wsMessage{Type: eventType})
		return fallback
	}
	return envelope
}
