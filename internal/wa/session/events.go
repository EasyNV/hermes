package session

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EventPublisher publishes WA events to NATS JetStream.
type EventPublisher interface {
	PublishInbound(waNumberID, senderJID, senderPhone, waMessageID string, contentType hermesv1.ContentType, body, mediaURL, mimeType, senderName string)
	PublishOutboundStatus(waNumberID, waMessageID, recipientJID string, status hermesv1.MessageStatus, errMsg string)
	PublishBan(waNumberID, jid, phone, proxyID, podID, reason string)
	PublishConnection(waNumberID, jid, phone string, state hermesv1.WaConnectionState, podID, reason string)
}

// NatsEventPublisher publishes events via NATS JetStream.
type NatsEventPublisher struct {
	js       natsgo.JetStreamContext
	tenantFn func(waNumberID string) string // resolves waNumberID → tenantID
	log      zerolog.Logger
}

// NewEventPublisher creates a publisher that uses the given JetStream context.
// tenantFn resolves a wa_number_id to its tenant_id (for NATS subject routing).
func NewEventPublisher(js natsgo.JetStreamContext, tenantFn func(string) string, log zerolog.Logger) EventPublisher {
	return &NatsEventPublisher{js: js, tenantFn: tenantFn, log: log}
}

func (p *NatsEventPublisher) meta(waNumberID string) *hermesv1.EventMeta {
	return &hermesv1.EventMeta{
		EventId:   uuid.New().String(),
		TenantId:  p.tenantFn(waNumberID),
		Timestamp: timestamppb.Now(),
		Source:    "hermes-wa",
	}
}

func (p *NatsEventPublisher) publish(subject string, eventID string, msg proto.Message) {
	data, err := proto.Marshal(msg)
	if err != nil {
		p.log.Error().Err(err).Str("subject", subject).Msg("failed to marshal event")
		return
	}
	if _, err := p.js.Publish(subject, data, natsgo.MsgId(eventID)); err != nil {
		p.log.Error().Err(err).Str("subject", subject).Msg("failed to publish event")
	}
}

func (p *NatsEventPublisher) PublishInbound(waNumberID, senderJID, senderPhone, waMessageID string, contentType hermesv1.ContentType, body, mediaURL, mimeType, senderName string) {
	meta := p.meta(waNumberID)
	event := &hermesv1.WaInboundMessageEvent{
		Meta:        meta,
		WaNumberId:  waNumberID,
		SenderJid:   senderJID,
		SenderPhone: senderPhone,
		WaMessageId: waMessageID,
		ContentType: contentType,
		Body:        body,
		MediaUrl:    mediaURL,
		MimeType:    mimeType,
		SenderName:  senderName,
		WaTimestamp: timestamppb.Now(),
	}
	p.publish(fmt.Sprintf("hermes.wa.message.inbound.%s", meta.TenantId), meta.EventId, event)
}

func (p *NatsEventPublisher) PublishOutboundStatus(waNumberID, waMessageID, recipientJID string, status hermesv1.MessageStatus, errMsg string) {
	meta := p.meta(waNumberID)
	event := &hermesv1.WaOutboundStatusEvent{
		Meta:         meta,
		WaNumberId:   waNumberID,
		WaMessageId:  waMessageID,
		RecipientJid: recipientJID,
		Status:       status,
		Error:        errMsg,
		WaTimestamp:  timestamppb.Now(),
	}
	p.publish(fmt.Sprintf("hermes.wa.message.outbound.%s", meta.TenantId), meta.EventId, event)
}

func (p *NatsEventPublisher) PublishBan(waNumberID, jid, phone, proxyID, podID, reason string) {
	meta := p.meta(waNumberID)
	event := &hermesv1.WaBanEvent{
		Meta:       meta,
		WaNumberId: waNumberID,
		Jid:        jid,
		Phone:      phone,
		ProxyId:    proxyID,
		PodId:      podID,
		WaReason:   reason,
		DetectedAt: timestamppb.Now(),
	}
	p.publish(fmt.Sprintf("hermes.wa.ban.%s", meta.TenantId), meta.EventId, event)
}

func (p *NatsEventPublisher) PublishConnection(waNumberID, jid, phone string, state hermesv1.WaConnectionState, podID, reason string) {
	meta := p.meta(waNumberID)
	event := &hermesv1.WaConnectionEvent{
		Meta:       meta,
		WaNumberId: waNumberID,
		Jid:        jid,
		Phone:      phone,
		State:      state,
		PodId:      podID,
		Reason:     reason,
	}
	p.publish(fmt.Sprintf("hermes.wa.connection.%s", meta.TenantId), meta.EventId, event)
}

// makeEventHandler returns a whatsmeow event handler function for the given session.
func (m *realManager) makeEventHandler(waNumberID string) func(interface{}) {
	return func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			m.handleInboundMessage(waNumberID, v)
		case *events.Receipt:
			m.handleReceipt(waNumberID, v)
		case *events.Connected:
			m.handleConnected(waNumberID)
		case *events.Disconnected:
			m.handleDisconnected(waNumberID)
		case *events.LoggedOut:
			m.handleLoggedOut(waNumberID, v)
		}
	}
}

func (m *realManager) handleInboundMessage(waNumberID string, msg *events.Message) {
	m.mu.RLock()
	sess, ok := m.sessions[waNumberID]
	m.mu.RUnlock()
	if !ok {
		m.log.Warn().Str("wa_number_id", waNumberID).Msg("inbound message: session not found")
		return
	}
	sess.messagesRecvd.Add(1)

	// Skip messages from self (history sync, status broadcasts, etc.)
	if msg.Info.IsFromMe {
		return
	}

	m.log.Info().
		Str("wa_number_id", waNumberID).
		Str("sender", msg.Info.Sender.String()).
		Str("sender_phone", msg.Info.Sender.User).
		Str("wa_msg_id", msg.Info.ID).
		Str("push_name", msg.Info.PushName).
		Bool("from_me", msg.Info.IsFromMe).
		Msg("inbound message received")

	if m.eventPub == nil {
		m.log.Warn().Msg("inbound message: eventPub is nil, cannot publish")
		return
	}

	senderJID := msg.Info.Sender.String()
	senderPhone := msg.Info.Sender.User
	waMessageID := msg.Info.ID
	senderName := msg.Info.PushName

	var contentType hermesv1.ContentType
	var body, mediaURL, mimeType string

	if msg.Message.GetConversation() != "" {
		contentType = hermesv1.ContentType_CONTENT_TYPE_TEXT
		body = msg.Message.GetConversation()
	} else if msg.Message.GetExtendedTextMessage() != nil {
		contentType = hermesv1.ContentType_CONTENT_TYPE_TEXT
		body = msg.Message.GetExtendedTextMessage().GetText()
	} else if msg.Message.GetImageMessage() != nil {
		contentType = hermesv1.ContentType_CONTENT_TYPE_IMAGE
		mimeType = msg.Message.GetImageMessage().GetMimetype()
		body = msg.Message.GetImageMessage().GetCaption()
	} else if msg.Message.GetDocumentMessage() != nil {
		contentType = hermesv1.ContentType_CONTENT_TYPE_DOCUMENT
		mimeType = msg.Message.GetDocumentMessage().GetMimetype()
		body = msg.Message.GetDocumentMessage().GetCaption()
	} else if msg.Message.GetAudioMessage() != nil {
		contentType = hermesv1.ContentType_CONTENT_TYPE_AUDIO
		mimeType = msg.Message.GetAudioMessage().GetMimetype()
	} else if msg.Message.GetVideoMessage() != nil {
		contentType = hermesv1.ContentType_CONTENT_TYPE_VIDEO
		mimeType = msg.Message.GetVideoMessage().GetMimetype()
		body = msg.Message.GetVideoMessage().GetCaption()
	}

	m.eventPub.PublishInbound(waNumberID, senderJID, senderPhone, waMessageID, contentType, body, mediaURL, mimeType, senderName)
}

func (m *realManager) handleReceipt(waNumberID string, receipt *events.Receipt) {
	if m.eventPub == nil {
		return
	}

	var status hermesv1.MessageStatus
	switch receipt.Type {
	case types.ReceiptTypeDelivered:
		status = hermesv1.MessageStatus_MESSAGE_STATUS_DELIVERED
	case types.ReceiptTypeRead:
		status = hermesv1.MessageStatus_MESSAGE_STATUS_READ
	default:
		return
	}

	senderJID := receipt.Chat.String()
	for _, msgID := range receipt.MessageIDs {
		m.eventPub.PublishOutboundStatus(waNumberID, msgID, senderJID, status, "")
	}
}

func (m *realManager) handleConnected(waNumberID string) {
	m.mu.Lock()
	sess, ok := m.sessions[waNumberID]
	if ok {
		sess.state = hermesv1.SessionState_SESSION_STATE_CONNECTED
		sess.connectedAt = time.Now()
		if sess.client.Store.ID != nil {
			sess.jid = sess.client.Store.ID.String()
		}
	}
	m.mu.Unlock()

	if ok {
		m.onConnected(context.Background(), sess)
	}
}

func (m *realManager) handleDisconnected(waNumberID string) {
	m.mu.Lock()
	sess, ok := m.sessions[waNumberID]
	if ok {
		sess.state = hermesv1.SessionState_SESSION_STATE_RECONNECTING
	}
	m.mu.Unlock()
	if ok {
		m.log.Info().Str("wa_number_id", waNumberID).Msg("session disconnected, will auto-reconnect")
	}
}

func (m *realManager) handleLoggedOut(waNumberID string, evt *events.LoggedOut) {
	m.mu.Lock()
	sess, ok := m.sessions[waNumberID]
	if !ok {
		m.mu.Unlock()
		return
	}

	sess.state = hermesv1.SessionState_SESSION_STATE_BANNED
	sess.client.Disconnect()
	proxyID := sess.proxyID
	jid := sess.jid
	phone := sess.phone
	delete(m.sessions, waNumberID)
	m.mu.Unlock()

	reason := "logged_out"
	if evt.Reason != 0 {
		reason = fmt.Sprintf("reason_%d", evt.Reason)
	}

	m.log.Warn().Str("wa_number_id", waNumberID).Str("reason", reason).Msg("session banned/logged out")

	if m.updater != nil {
		_ = m.updater.SetWaNumberBanned(context.Background(), waNumberID)
	}
	if m.eventPub != nil {
		m.eventPub.PublishBan(waNumberID, jid, phone, proxyID, m.podID, reason)
		m.eventPub.PublishConnection(waNumberID, jid, phone, hermesv1.WaConnectionState_WA_CONNECTION_STATE_LOGGED_OUT, m.podID, reason)
	}
}
