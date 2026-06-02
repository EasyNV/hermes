package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
	"github.com/hermes-waba/hermes/pkg/observability"
	natsgo "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
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
	if err := startMbsInboundConsumer(js, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start MBS inbound consumer")
	}
	if err := startMbsOutboundConsumer(js, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start MBS outbound consumer")
	}

	// ── Diagnostic HTTP server (Stage F chunk 4).
	diagSrv := observability.NewHTTPServer(observability.Options{
		Addr:       fmt.Sprintf(":%d", cfg.MetricsPort),
		Registerer: prometheus.DefaultGatherer,
		ReadinessFn: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("db: %w", err)
			}
			if !nc.IsConnected() {
				return errors.New("nats: not connected")
			}
			return nil
		},
	})
	diagListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.MetricsPort))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.MetricsPort).Msg("diagnostic HTTP listen failed")
	}
	diagCtx, diagCancel := context.WithCancel(context.Background())
	diagErrCh := make(chan error, 1)
	go func() { diagErrCh <- diagSrv.Serve(diagCtx, diagListener) }()

	// gRPC server
	h := handler.New(store, js, log)
	grpcServer := grpc.NewServer()
	hermesv1.RegisterHermesInboxServer(grpcServer, h)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	diagSrv.SetReady(true)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info().Msg("shutting down hermes-inbox")
		diagSrv.SetReady(false)
		grpcServer.GracefulStop()
	}()

	log.Info().Int("port", cfg.Port).Int("metrics_port", cfg.MetricsPort).Msg("hermes-inbox started")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}

	diagCancel()
	if err := <-diagErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn().Err(err).Msg("diagnostic HTTP server exited with error")
	}
}

// ensureStreams creates NATS streams needed by the inbox service.
//
// Idempotent: queries StreamInfo first and only calls AddStream if the
// stream doesn't already exist. NATS rejects AddStream-with-different-config
// as "name already in use" — without this guard, two services racing to
// boot (e.g. notify owns HERMES_NOTIFY as WorkQueuePolicy, inbox here
// defines it as default LimitsPolicy) will crash whichever loses the race.
// Same pattern as cmd/notify/main.go::ensureStream, cmd/gateway/main.go::
// ensureStreams, cmd/mbs/nats_streams.go::ensureStreams.
func ensureStreams(js natsgo.JetStreamContext) error {
	streams := []struct {
		name     string
		subjects []string
		maxAge   time.Duration
	}{
		// HERMES_WA — consumed for inbound/outbound events.
		{"HERMES_WA", []string{"hermes.wa.message.>", "hermes.wa.ban.>", "hermes.wa.connection.>", "hermes.wa.presence.>"}, 7 * 24 * time.Hour},
		// HERMES_INBOX — manual send tasks published by this service.
		{"HERMES_INBOX", []string{"hermes.wa.send.manual.>"}, 24 * time.Hour},
		// HERMES_NOTIFY — notification dispatch events.
		{"HERMES_NOTIFY", []string{"hermes.notify.>"}, time.Hour},
		// HERMES_MBS — MBS inbound/outbound/session events. Gateway and
		// mbs also ensure this stream on their boot. Subjects must match
		// across services so the StreamInfo-check skips correctly.
		{"HERMES_MBS", []string{"hermes.mbs.message.>", "hermes.mbs.session.>"}, 7 * 24 * time.Hour},
	}
	for _, s := range streams {
		if _, err := js.StreamInfo(s.name); err == nil {
			continue
		}
		if _, err := js.AddStream(&natsgo.StreamConfig{
			Name:     s.name,
			Subjects: s.subjects,
			Storage:  natsgo.FileStorage,
			MaxAge:   s.maxAge,
		}); err != nil {
			return fmt.Errorf("ensuring %s stream: %w", s.name, err)
		}
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
		// Normalize: strip '+' prefix for matching (whatsmeow sends without +, DB may have +).
		senderPhone := strings.TrimPrefix(event.SenderPhone, "+")

		// Try exact match, then with + prefix.
		contact, _, err := store.FindContactByPhone(ctx, senderPhone)
		if err != nil {
			contact, _, err = store.FindContactByPhone(ctx, "+"+senderPhone)
		}
		if err != nil {
			// Auto-create contact for unknown senders.
			_, tenantID, tenantErr := store.GetWorkspaceIDForWaNumber(ctx, event.WaNumberId)
			if tenantErr != nil {
				log.Warn().
					Str("phone", senderPhone).
					Err(tenantErr).
					Msg("cannot resolve tenant for auto-create contact, skipping")
				msg.Ack()
				return
			}
			contact, err = store.AutoCreateContact(ctx, tenantID, senderPhone, event.SenderName)
			if err != nil {
				log.Error().
					Str("phone", senderPhone).
					Err(err).
					Msg("failed to auto-create contact")
				msg.Nak()
				return
			}
			log.Info().
				Str("phone", senderPhone).
				Str("name", event.SenderName).
				Str("contact_id", contact.ID).
				Msg("auto-created contact for unknown sender")
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

		// 2b. Check contact allowlist — only create conversations for allowed contacts.
		log.Debug().
			Str("phone", senderPhone).
			Str("workspace_id", workspaceID).
			Msg("allowlist check: looking up phone")
		allowed, alErr := store.IsPhoneAllowlisted(ctx, workspaceID, senderPhone)
		if alErr != nil {
			log.Warn().Err(alErr).Str("phone", senderPhone).Msg("allowlist check failed, allowing message")
			allowed = true // fail open — don't lose messages on DB errors
		}
		if !allowed {
			log.Debug().
				Str("phone", senderPhone).
				Str("workspace_id", workspaceID).
				Msg("inbound message from non-allowlisted contact, dropping")
			msg.Ack()
			return
		}

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

// startMbsInboundConsumer subscribes to MBS inbound messages and creates/updates
// conversations with channel='mbs'. Mirrors startInboundConsumer line-for-line
// for diff reviewability. Differences vs WA:
//
//   - decodes MbsInboundMessageEvent
//   - tenant from event.Meta.TenantId; workspace from GetWorkspaceIDForMbsUid(uid)
//   - no allowlist (see plan §C3-K5)
//   - synthetic phone "mbs:thread:<id>" when senderPhone is empty (Messenger user)
//   - uses FindOrCreateMbsConversation / CreateMbsMessage; mbs_mid keyed
func startMbsInboundConsumer(js natsgo.JetStreamContext, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.mbs.message.inbound.*", func(msg *natsgo.Msg) {
		var event hermesv1.MbsInboundMessageEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal MBS inbound message event")
			msg.Ack() // poison pill
			return
		}

		ack := processMbsInbound(context.Background(), store, js, log, &event)
		if ack {
			msg.Ack()
		} else {
			msg.Nak()
		}
	},
		natsgo.Durable("inbox-mbs-inbound"),
		natsgo.ManualAck(),
		natsgo.AckWait(30*time.Second),
		natsgo.MaxDeliver(5),
	)
	if err != nil {
		return fmt.Errorf("subscribing to MBS inbound events: %w", err)
	}

	log.Info().Str("subject", "hermes.mbs.message.inbound.*").Msg("MBS inbound message consumer started")
	return nil
}

// processMbsInbound performs the actual DB write side of the MBS inbound
// consumer. Returns true → caller should ACK (success, or terminal drop),
// false → caller should NAK (transient, retry).
//
// Split from the subscription closure so unit tests can drive it
// without a real NATS subscription.
func processMbsInbound(
	ctx context.Context,
	store handler.Store,
	js natsgo.JetStreamContext,
	log zerolog.Logger,
	event *hermesv1.MbsInboundMessageEvent,
) bool {
	tenantID := event.Meta.GetTenantId()
	if tenantID == "" {
		log.Warn().
			Int64("uid", event.Uid).
			Str("mid", event.Mid).
			Msg("MBS inbound: missing tenant_id, dropping")
		return true
	}

	// Un-keyable guard: the conversation is keyed on (workspace, uid,
	// thread_id) and the synthetic contact slug needs a stable id too.
	// When BOTH thread_id and sender_phone are empty there is no way to
	// key the conversation — this happens for non-message deltas or a
	// payload the extractor couldn't resolve. ACK-drop (return true)
	// rather than NAK: retrying can't conjure a key, and a NAK loop
	// pins the consumer redelivering the same un-keyable event forever.
	if event.ThreadId == "" && strings.TrimPrefix(event.SenderPhone, "+") == "" {
		log.Warn().
			Int64("uid", event.Uid).
			Str("mid", event.Mid).
			Msg("MBS inbound: no thread_id and no sender_phone — un-keyable, dropping")
		return true
	}

	// 1. Resolve workspace from MBS uid (joins mbs_sessions ↔ workspaces).
	workspaceID, resolvedTenantID, err := store.GetWorkspaceIDForMbsUid(ctx, event.Uid)
	if err != nil {
		log.Warn().Err(err).
			Int64("uid", event.Uid).
			Str("tenant_id", tenantID).
			Msg("MBS inbound: workspace lookup miss (session burned or missing), dropping")
		return true
	}
	if resolvedTenantID != "" {
		tenantID = resolvedTenantID
	}

	// 2. Resolve or auto-create contact.
	senderPhone := strings.TrimPrefix(event.SenderPhone, "+")
	// Identity enrichment: mbs publishes no sender_phone for inbound
	// (the snapshot payload doesn't carry it), but the send path recorded
	// (uid, thread_id) -> phone in mbs_phone_threads. Reverse-resolve it so
	// the contact shows the real customer phone and UNIFIES with any
	// outbound conversation, instead of a synthetic mbs:thread:<id> slug.
	if senderPhone == "" && event.ThreadId != "" {
		if phone, perr := store.GetPhoneByMbsThread(ctx, event.Uid, event.ThreadId); perr == nil && phone != "" {
			senderPhone = strings.TrimPrefix(phone, "+")
		}
	}
	var (
		lookupKey string
		autoName  string
	)
	if senderPhone == "" {
		// Synthetic stable slug per MBS thread (customer-first thread the
		// send path never populated — phone fills in on first outbound).
		lookupKey = "mbs:thread:" + event.ThreadId
		tail := event.ThreadId
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		autoName = "MBS thread " + tail
	} else {
		lookupKey = senderPhone
		autoName = ""
	}

	contact, _, lookupErr := store.FindContactByPhone(ctx, lookupKey)
	if lookupErr != nil && senderPhone != "" {
		// WA-parity: try with + prefix.
		contact, _, lookupErr = store.FindContactByPhone(ctx, "+"+senderPhone)
	}
	if lookupErr != nil {
		created, createErr := store.AutoCreateContact(ctx, tenantID, lookupKey, autoName)
		if createErr != nil {
			log.Error().Err(createErr).
				Str("lookup_key", lookupKey).
				Int64("uid", event.Uid).
				Msg("MBS inbound: failed to auto-create contact")
			return false
		}
		contact = created
		log.Info().
			Str("lookup_key", lookupKey).
			Str("name", autoName).
			Str("contact_id", contact.ID).
			Msg("MBS inbound: auto-created contact")
	}

	// 3. Find or create MBS conversation.
	uidStr := strconv.FormatInt(event.Uid, 10)
	// Defensive guard + diagnostic: the conversation upsert binds
	// workspace_id and contact_id as NOT-NULL uuid columns. If either is
	// empty we'd hit "invalid input syntax for type uuid" and NAK-loop
	// forever. Surface the exact runtime values and ACK-drop instead.
	contactID := ""
	if contact != nil {
		contactID = contact.ID
	}
	if workspaceID == "" || contactID == "" {
		log.Error().
			Int64("uid", event.Uid).
			Str("thread_id", event.ThreadId).
			Str("workspace_id", workspaceID).
			Str("contact_id", contactID).
			Str("sender_phone", senderPhone).
			Str("lookup_key", lookupKey).
			Bool("contact_nil", contact == nil).
			Msg("MBS inbound: empty workspace_id or contact_id before upsert — dropping")
		return true
	}
	conv, _, err := store.FindOrCreateMbsConversation(
		ctx, workspaceID, contactID, uidStr, event.ThreadId, event.PageId,
	)
	if err != nil {
		log.Error().Err(err).
			Int64("uid", event.Uid).
			Str("thread_id", event.ThreadId).
			Msg("MBS inbound: failed to find/create conversation")
		return false
	}

	// 4. Reopen if closed.
	if conv.Status == "closed" {
		newStatus := conversation.StatusAfterInbound(conv.Status)
		if newStatus != conv.Status {
			if err := store.ReopenConversation(ctx, conv.ID); err != nil {
				log.Error().Err(err).Str("conv_id", conv.ID).Msg("MBS inbound: failed to reopen conversation")
			}
			conv.Status = newStatus
		}
	}

	// 5. Store the message. wasInserted is false on a re-poll/redelivery
	// hitting the existing row (mbs_mid conflict) — the snapshot is re-read
	// every 10s, so without this guard every cycle would re-stamp
	// last_message_at to "now" (collapsing all cards to the same time) and
	// re-fire the unassigned notification for messages already seen.
	msgRow, wasInserted, err := store.CreateMbsMessage(ctx, conv.ID, "inbound", event.Text, event.Mid)
	if err != nil {
		log.Error().Err(err).
			Str("conv_id", conv.ID).
			Str("mid", event.Mid).
			Msg("MBS inbound: failed to store message")
		return false
	}
	if !wasInserted {
		// Already-seen message re-surfaced by the poll. Idempotent ACK:
		// nothing to update, nothing to notify.
		log.Debug().
			Str("conv_id", conv.ID).
			Str("mid", event.Mid).
			Msg("MBS inbound: message already stored (re-poll), skipping side-effects")
		return true
	}
	_ = msgRow

	// 6. Update last_message_at + preview (new message only).
	preview := event.Text
	if len(preview) > 100 {
		preview = preview[:100]
	}
	_ = store.UpdateLastMessage(ctx, conv.ID, preview)

	// 7. Notify on unassigned (new message only).
	if conv.Status == "unassigned" && js != nil {
		publishNotification(js, log, tenantID, workspaceID, contact, event.Text)
	}

	log.Debug().
		Str("conv_id", conv.ID).
		Str("thread_id", event.ThreadId).
		Str("mid", event.Mid).
		Int64("uid", event.Uid).
		Msg("processed MBS inbound message")
	return true
}

// startMbsOutboundConsumer subscribes to hermes.mbs.message.outbound.*
// and reconciles message status by client_dedupe_id (= local msg.ID
// stamped during SendMessage's MBS branch).
//
// Flow:
//   - On success (ok=true, mid set): SetMbsMID(otid=msg.ID, mid) then
//     UpdateMbsMessageStatus(mid, "sent"). Forward-transition guarded.
//   - On failure before mid (ok=false, mid==""): MarkOutboundFailedByID(msg.ID).
//   - On failure with mid (ok=false, mid set): SetMbsMID first, then
//     UpdateMbsMessageStatus(mid, "failed").
func startMbsOutboundConsumer(js natsgo.JetStreamContext, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.mbs.message.outbound.*", func(msg *natsgo.Msg) {
		var event hermesv1.MbsOutboundEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal MBS outbound event")
			msg.Ack() // poison pill
			return
		}

		if processMbsOutbound(context.Background(), store, log, &event) {
			msg.Ack()
		} else {
			msg.Nak()
		}
	},
		natsgo.Durable("inbox-mbs-outbound"),
		natsgo.ManualAck(),
		natsgo.AckWait(30*time.Second),
		natsgo.MaxDeliver(5),
	)
	if err != nil {
		return fmt.Errorf("subscribing to MBS outbound events: %w", err)
	}

	log.Info().Str("subject", "hermes.mbs.message.outbound.*").Msg("MBS outbound status consumer started")
	return nil
}

// processMbsOutbound handles the DB side of MBS outbound reconciliation.
// Returns true → ACK; false → NAK.
//
// Correlation key is event.ClientDedupeId — set by SendMessage's MBS
// branch to msg.ID, threaded through MbsCampaignSendTask.IdempotencyKey
// → MbsSendMessageRequest.ClientDedupeId → MbsOutboundEvent.ClientDedupeId.
func processMbsOutbound(
	ctx context.Context,
	store handler.Store,
	log zerolog.Logger,
	event *hermesv1.MbsOutboundEvent,
) bool {
	otid := string(event.GetClientDedupeId())

	// Neither correlation key set → can't find the row. Could be a
	// campaign-only send that bypassed the inbox; ACK and move on.
	if otid == "" && event.Mid == "" {
		log.Warn().
			Int64("uid", event.Uid).
			Str("thread_id", event.ThreadId).
			Msg("MBS outbound: event has neither client_dedupe_id nor mid, dropping")
		return true
	}

	// Branch A: success. mid is the canonical status key going forward.
	if event.Ok {
		if event.Mid == "" {
			log.Warn().
				Str("otid", otid).
				Msg("MBS outbound: ok=true but mid empty, treating as no-op")
			return true
		}
		// Stamp MID on the local row first (idempotent).
		if otid != "" {
			if err := store.SetMbsMID(ctx, otid, event.Mid); err != nil {
				// ErrNotFound is non-fatal: either MID already stamped
				// (re-delivery) or the row was for a non-inbox send.
				log.Debug().Err(err).Str("otid", otid).Str("mid", event.Mid).
					Msg("MBS outbound: SetMbsMID no-op (row not found or already stamped)")
			}
		}
		// Transition status with forward-only guard.
		existing, gerr := store.GetMessageByMbsMID(ctx, event.Mid)
		if gerr != nil {
			// Row not found → either we just stamped it (eventual-consistency
			// race with our own SetMbsMID is impossible since it's same txn-less
			// pool) OR the row truly doesn't exist. ACK; if real, redelivery
			// would resolve via the otid path on re-entry.
			log.Debug().Str("mid", event.Mid).Msg("MBS outbound: row not found by mid, dropping")
			return true
		}
		if !conversation.IsForwardTransition(existing.Status, "sent") {
			log.Debug().
				Str("mid", event.Mid).
				Str("current", existing.Status).
				Msg("MBS outbound: skipping non-forward transition to sent")
			return true
		}
		if err := store.UpdateMbsMessageStatus(ctx, event.Mid, "sent"); err != nil {
			log.Error().Err(err).Str("mid", event.Mid).Msg("MBS outbound: failed to mark sent")
			return false
		}
		log.Debug().Str("mid", event.Mid).Str("otid", otid).Msg("MBS outbound: marked sent")
		return true
	}

	// Branch B: failure. Two sub-cases by whether Meta assigned a mid.
	if event.Mid == "" {
		// Failure before Meta assigned mid → patch by otid → failed.
		if otid == "" {
			log.Warn().Msg("MBS outbound: failure event with neither mid nor otid")
			return true
		}
		if err := store.MarkOutboundFailedByID(ctx, otid); err != nil {
			// ErrNotFound is non-fatal — row may already be in a terminal
			// state (failed/sent) from an earlier delivery.
			log.Debug().Err(err).Str("otid", otid).
				Msg("MBS outbound: MarkOutboundFailedByID no-op (not in pending)")
			return true
		}
		log.Debug().Str("otid", otid).Str("err", event.Error).Msg("MBS outbound: marked failed by otid")
		return true
	}
	// Failure WITH mid (rare: Meta assigned then delivery failed). Patch
	// the mid in then transition status.
	if otid != "" {
		_ = store.SetMbsMID(ctx, otid, event.Mid)
	}
	existing, gerr := store.GetMessageByMbsMID(ctx, event.Mid)
	if gerr != nil {
		log.Debug().Str("mid", event.Mid).Msg("MBS outbound (failure): row not found by mid")
		return true
	}
	if !conversation.IsForwardTransition(existing.Status, "failed") {
		return true
	}
	if err := store.UpdateMbsMessageStatus(ctx, event.Mid, "failed"); err != nil {
		log.Error().Err(err).Str("mid", event.Mid).Msg("MBS outbound: failed to mark failed")
		return false
	}
	log.Debug().Str("mid", event.Mid).Str("err", event.Error).Msg("MBS outbound: marked failed by mid")
	return true
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
