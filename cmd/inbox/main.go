package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	inboxconfig "github.com/hermes-waba/hermes/internal/inbox/config"
	"github.com/hermes-waba/hermes/internal/inbox/conversation"
	"github.com/hermes-waba/hermes/internal/inbox/handler"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	cfg := inboxconfig.Load()
	log := logger.New("hermes-inbox")

	ctx := context.Background()

	// PostgreSQL
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	store := handler.NewPgStore(pool)

	// NATS JetStream
	js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to NATS")
	}
	defer nc.Close()

	if err := ensureStreams(js); err != nil {
		log.Fatal().Err(err).Msg("failed to ensure NATS streams")
	}

	if err := startInboundConsumer(js, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start inbound consumer")
	}
	if err := startOutboundConsumer(js, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start outbound consumer")
	}

	// gRPC server
	h := handler.New(store, js, log)
	grpcServer := grpc.NewServer()
	hermesv1.RegisterHermesInboxServer(grpcServer, h)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info().Msg("shutting down hermes-inbox")
		grpcServer.GracefulStop()
	}()

	log.Info().Int("port", cfg.Port).Msg("hermes-inbox started")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}
}

// ensureStreams creates NATS streams needed by the inbox service.
func ensureStreams(js natsgo.JetStreamContext) error {
	// HERMES_WA — consumed for inbound/outbound events.
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_WA",
		Subjects: []string{"hermes.wa.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	}); err != nil {
		return fmt.Errorf("ensuring HERMES_WA stream: %w", err)
	}

	// HERMES_INBOX — manual send tasks published by this service.
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_INBOX",
		Subjects: []string{"hermes.wa.send.manual.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   24 * time.Hour,
	}); err != nil {
		return fmt.Errorf("ensuring HERMES_INBOX stream: %w", err)
	}

	// HERMES_NOTIFY — notification dispatch events.
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_NOTIFY",
		Subjects: []string{"hermes.notify.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   1 * time.Hour,
	}); err != nil {
		return fmt.Errorf("ensuring HERMES_NOTIFY stream: %w", err)
	}

	return nil
}

// startInboundConsumer subscribes to inbound WA messages and creates/updates conversations.
func startInboundConsumer(js natsgo.JetStreamContext, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.message.inbound.*", func(msg *natsgo.Msg) {
		var event hermesv1.WaInboundMessageEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal inbound message event")
			msg.Ack()
			return
		}

		ctx := context.Background()

		// 1. Look up contact by sender phone.
		contact, _, err := store.FindContactByPhone(ctx, event.SenderPhone)
		if err != nil {
			log.Warn().
				Str("phone", event.SenderPhone).
				Msg("contact not found for inbound message, skipping")
			msg.Ack()
			return
		}

		// 2. Resolve workspace from WA number.
		workspaceID, tenantID, err := store.GetWorkspaceIDForWaNumber(ctx, event.WaNumberId)
		if err != nil {
			log.Error().Err(err).
				Str("wa_number_id", event.WaNumberId).
				Msg("failed to resolve workspace for WA number")
			msg.Nak()
			return
		}
		_ = tenantID

		// 3. Find or create conversation.
		conv, _, err := store.FindOrCreateConversation(ctx, workspaceID, contact.ID, event.WaNumberId, nil)
		if err != nil {
			log.Error().Err(err).Msg("failed to find/create conversation")
			msg.Nak()
			return
		}

		// 4. Reopen if closed.
		if conv.Status == "closed" {
			newStatus := conversation.StatusAfterInbound(conv.Status)
			if newStatus != conv.Status {
				if err := store.ReopenConversation(ctx, conv.ID); err != nil {
					log.Error().Err(err).Str("conv_id", conv.ID).Msg("failed to reopen conversation")
				}
				conv.Status = newStatus
			}
		}

		// 5. Store the message.
		body := event.Body
		var bodyPtr, mediaPtr *string
		if body != "" {
			bodyPtr = &body
		}
		if event.MediaUrl != "" {
			mediaPtr = &event.MediaUrl
		}
		ct := contentTypeEventToStr(event.ContentType)
		_, err = store.CreateMessage(ctx, conv.ID, "inbound", ct, bodyPtr, mediaPtr, event.WaMessageId)
		if err != nil {
			log.Error().Err(err).Str("conv_id", conv.ID).Msg("failed to store inbound message")
			msg.Nak()
			return
		}

		// 6. Update last_message_at.
		preview := body
		if len(preview) > 100 {
			preview = preview[:100]
		}
		_ = store.UpdateLastMessage(ctx, conv.ID, preview)

		// 7. If unassigned, publish notification.
		if conv.Status == "unassigned" {
			publishNotification(js, log, event.Meta.GetTenantId(), workspaceID, contact, body)
		}

		msg.Ack()
		log.Debug().
			Str("conv_id", conv.ID).
			Str("phone", event.SenderPhone).
			Msg("processed inbound message")

	},
		natsgo.Durable("inbox-inbound"),
		natsgo.ManualAck(),
		natsgo.AckWait(30*time.Second),
		natsgo.MaxDeliver(5),
	)
	if err != nil {
		return fmt.Errorf("subscribing to inbound events: %w", err)
	}

	log.Info().Str("subject", "hermes.wa.message.inbound.*").Msg("inbound message consumer started")
	return nil
}

// startOutboundConsumer subscribes to outbound status events and updates message status.
func startOutboundConsumer(js natsgo.JetStreamContext, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.message.outbound.*", func(msg *natsgo.Msg) {
		var event hermesv1.WaOutboundStatusEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal outbound status event")
			msg.Ack()
			return
		}

		ctx := context.Background()
		newStatus := messageStatusEventToStr(event.Status)

		// Find message by wa_message_id.
		existing, err := store.GetMessageByWaMessageID(ctx, event.WaMessageId)
		if err != nil {
			// Message not found — might be a campaign message, not inbox. ACK and move on.
			msg.Ack()
			return
		}

		// Only apply forward transitions.
		if !conversation.IsForwardTransition(existing.Status, newStatus) {
			msg.Ack()
			return
		}

		if err := store.UpdateMessageStatus(ctx, event.WaMessageId, newStatus); err != nil {
			log.Error().Err(err).
				Str("wa_message_id", event.WaMessageId).
				Str("new_status", newStatus).
				Msg("failed to update message status")
			msg.Nak()
			return
		}

		msg.Ack()
		log.Debug().
			Str("wa_message_id", event.WaMessageId).
			Str("status", newStatus).
			Msg("updated outbound message status")

	},
		natsgo.Durable("inbox-outbound"),
		natsgo.ManualAck(),
		natsgo.AckWait(30*time.Second),
		natsgo.MaxDeliver(5),
	)
	if err != nil {
		return fmt.Errorf("subscribing to outbound events: %w", err)
	}

	log.Info().Str("subject", "hermes.wa.message.outbound.*").Msg("outbound status consumer started")
	return nil
}

// publishNotification sends a NotifyDispatchEvent for a new unassigned message.
func publishNotification(js natsgo.JetStreamContext, log zerolog.Logger, tenantID, workspaceID string, contact *handler.ContactRow, body string) {
	eventID := uuid.New().String()
	title := fmt.Sprintf("New message from %s", contact.Phone)
	if contact.Name != "" {
		title = fmt.Sprintf("New message from %s (%s)", contact.Name, contact.Phone)
	}
	preview := body
	if len(preview) > 200 {
		preview = preview[:200]
	}

	event := &hermesv1.NotifyDispatchEvent{
		Meta: &hermesv1.EventMeta{
			EventId:   eventID,
			TenantId:  tenantID,
			Timestamp: timestamppb.Now(),
			Source:    "hermes-inbox",
		},
		WorkspaceId: workspaceID,
		Category:    hermesv1.NotifyCategory_NOTIFY_CATEGORY_NEW_MESSAGE,
		Title:       title,
		Body:        preview,
	}

	data, err := proto.Marshal(event)
	if err != nil {
		log.Error().Err(err).Msg("failed to marshal notify event")
		return
	}

	subject := "hermes.notify.dispatch." + tenantID
	if _, err := js.Publish(subject, data, natsgo.MsgId(eventID)); err != nil {
		log.Error().Err(err).Str("subject", subject).Msg("failed to publish notify event")
	}
}

func contentTypeEventToStr(ct hermesv1.ContentType) string {
	switch ct {
	case hermesv1.ContentType_CONTENT_TYPE_TEXT:
		return "text"
	case hermesv1.ContentType_CONTENT_TYPE_IMAGE:
		return "image"
	case hermesv1.ContentType_CONTENT_TYPE_DOCUMENT:
		return "document"
	case hermesv1.ContentType_CONTENT_TYPE_AUDIO:
		return "audio"
	case hermesv1.ContentType_CONTENT_TYPE_VIDEO:
		return "video"
	default:
		return "text"
	}
}

func messageStatusEventToStr(s hermesv1.MessageStatus) string {
	switch s {
	case hermesv1.MessageStatus_MESSAGE_STATUS_SENT:
		return "sent"
	case hermesv1.MessageStatus_MESSAGE_STATUS_DELIVERED:
		return "delivered"
	case hermesv1.MessageStatus_MESSAGE_STATUS_READ:
		return "read"
	case hermesv1.MessageStatus_MESSAGE_STATUS_FAILED:
		return "failed"
	default:
		return "pending"
	}
}
