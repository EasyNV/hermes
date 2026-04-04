package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver for database/sql (whatsmeow)

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	waconfig "github.com/hermes-waba/hermes/internal/wa/config"
	"github.com/hermes-waba/hermes/internal/wa/handler"
	"github.com/hermes-waba/hermes/internal/wa/sender"
	"github.com/hermes-waba/hermes/internal/wa/session"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func main() {
	cfg := waconfig.Load()
	log := logger.New("hermes-wa")

	ctx := context.Background()

	// PostgreSQL.
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	store := handler.NewPgStore(pool)

	// whatsmeow sqlstore (creates its own tables for session persistence).
	container, err := sqlstore.New(ctx, "pgx", cfg.DatabaseURL, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create whatsmeow sqlstore")
	}

	// NATS JetStream.
	js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to NATS")
	}
	defer nc.Close()

	if err := ensureStreams(js); err != nil {
		log.Fatal().Err(err).Msg("failed to ensure NATS streams")
	}

	// Proxy gRPC client (optional — won't fail if proxy service is down).
	var proxyClient hermesv1.HermesProxyClient
	proxyConn, err := grpc.NewClient(cfg.ProxyServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Warn().Err(err).Str("addr", cfg.ProxyServiceAddr).Msg("proxy service unavailable, sessions will connect without proxy")
	} else {
		proxyClient = hermesv1.NewHermesProxyClient(proxyConn)
		defer proxyConn.Close()
	}

	// Tenant ID cache for NATS event publishing.
	tenantCache := newTenantCache(store)

	// Event publisher.
	eventPub := session.NewEventPublisher(js, tenantCache.Get, log)

	// Session manager.
	mgr := session.NewManager(container, cfg.PodID, store, eventPub, log)
	defer mgr.Close()

	// Sender.
	snd := sender.New()

	// gRPC handler.
	h := handler.NewHandler(store, mgr, snd, proxyClient, cfg.PodID, log)
	grpcServer := grpc.NewServer()
	hermesv1.RegisterHermesWaServer(grpcServer, h)

	// NATS consumers for campaign and manual sends.
	if err := startCampaignConsumer(js, mgr, snd, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start campaign send consumer")
	}
	if err := startManualConsumer(js, mgr, snd, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start manual send consumer")
	}

	// Reconnect sessions assigned to this pod.
	go reconnectSessions(ctx, store, mgr, proxyClient, cfg.PodID, log)

	// Start gRPC server.
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info().Msg("shutting down hermes-wa")
		grpcServer.GracefulStop()
	}()

	log.Info().Int("port", cfg.Port).Str("pod_id", cfg.PodID).Msg("hermes-wa started")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}
}

// ensureStreams creates the NATS streams that hermes-wa publishes to and consumes from.
func ensureStreams(js natsgo.JetStreamContext) error {
	_, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_WA",
		Subjects: []string{"hermes.wa.message.>", "hermes.wa.ban.>", "hermes.wa.connection.>", "hermes.wa.presence.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("ensuring HERMES_WA stream: %w", err)
	}

	_, err = js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_INBOX",
		Subjects: []string{"hermes.wa.send.manual.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("ensuring HERMES_INBOX stream: %w", err)
	}

	return nil
}

// startCampaignConsumer subscribes to campaign send tasks.
// Flow: typing indicator → send message → post-send delay → ACK.
func startCampaignConsumer(js natsgo.JetStreamContext, mgr session.Manager, snd sender.Sender, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.send.campaign.*", func(msg *natsgo.Msg) {
		var task hermesv1.CampaignSendTask
		if err := proto.Unmarshal(msg.Data, &task); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal campaign send task")
			msg.Ack()
			return
		}

		client, ok := mgr.GetClient(task.WaNumberId)
		if !ok {
			// Not our session — ACK so it doesn't redelivery forever.
			msg.Ack()
			return
		}

		ctx := context.Background()

		// Typing indicator.
		if task.TypingDurationMs > 0 {
			if err := snd.SendTypingIndicator(ctx, client, task.RecipientJid, task.TypingDurationMs); err != nil {
				log.Error().Err(err).Str("campaign_id", task.CampaignId).Msg("typing indicator failed")
			}
		}

		// Send message.
		contentType := hermesv1.ContentType_CONTENT_TYPE_TEXT
		if task.MediaUrl != "" {
			contentType = hermesv1.ContentType_CONTENT_TYPE_IMAGE
		}

		_, _, err := snd.SendMessage(ctx, client, task.RecipientJid, contentType, task.ResolvedBody, task.MediaUrl, "", "")
		if err != nil {
			log.Error().Err(err).Str("campaign_id", task.CampaignId).Str("contact_id", task.ContactId).Msg("campaign send failed")
			msg.Nak()
			return
		}

		// Increment sent count.
		if err := store.IncrementSentCount(ctx, task.WaNumberId); err != nil {
			log.Error().Err(err).Str("wa_number_id", task.WaNumberId).Msg("failed to increment sent count")
		}

		// Post-send delay.
		if task.PostSendDelayMs > 0 {
			time.Sleep(time.Duration(task.PostSendDelayMs) * time.Millisecond)
		}

		msg.Ack()
	},
		natsgo.Durable("wa-campaign-send"),
		natsgo.ManualAck(),
		natsgo.AckWait(120*time.Second),
		natsgo.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("subscribing to campaign sends: %w", err)
	}
	log.Info().Str("subject", "hermes.wa.send.campaign.*").Msg("campaign send consumer started")
	return nil
}

// startManualConsumer subscribes to manual send tasks from inbox agents.
// Flow: send message immediately → ACK.
func startManualConsumer(js natsgo.JetStreamContext, mgr session.Manager, snd sender.Sender, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.send.manual.*", func(msg *natsgo.Msg) {
		var task hermesv1.ManualSendTask
		if err := proto.Unmarshal(msg.Data, &task); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal manual send task")
			msg.Ack()
			return
		}

		client, ok := mgr.GetClient(task.WaNumberId)
		if !ok {
			msg.Nak()
			return
		}

		ctx := context.Background()
		_, _, err := snd.SendMessage(ctx, client, task.RecipientJid, task.ContentType, task.Body, task.MediaUrl, "", "")
		if err != nil {
			log.Error().Err(err).Str("message_id", task.MessageId).Msg("manual send failed")
			msg.Nak()
			return
		}

		if err := store.IncrementSentCount(ctx, task.WaNumberId); err != nil {
			log.Error().Err(err).Str("wa_number_id", task.WaNumberId).Msg("failed to increment sent count")
		}

		msg.Ack()
	},
		natsgo.Durable("wa-manual-send"),
		natsgo.ManualAck(),
		natsgo.AckWait(60*time.Second),
		natsgo.MaxDeliver(5),
	)
	if err != nil {
		return fmt.Errorf("subscribing to manual sends: %w", err)
	}
	log.Info().Str("subject", "hermes.wa.send.manual.*").Msg("manual send consumer started")
	return nil
}

// reconnectSessions reads wa_numbers assigned to this pod from DB and reconnects them.
func reconnectSessions(ctx context.Context, store handler.Store, mgr session.Manager, proxyClient hermesv1.HermesProxyClient, podID string, log zerolog.Logger) {
	rows, _, err := store.ListWaNumbersByPod(ctx, podID, "", 1, 10000)
	if err != nil {
		log.Error().Err(err).Msg("failed to list wa_numbers for reconnect")
		return
	}

	log.Info().Int("count", len(rows)).Str("pod_id", podID).Msg("reconnecting sessions")
	for _, row := range rows {
		var proxyCfg *session.ProxyConfig
		if proxyClient != nil && row.ProxyID != nil && *row.ProxyID != "" {
			resp, err := proxyClient.GetProxy(ctx, &hermesv1.ProxyGetRequest{Id: *row.ProxyID})
			if err == nil && resp.Proxy != nil {
				proxyCfg = &session.ProxyConfig{
					Host:     resp.Proxy.Host,
					Port:     resp.Proxy.Port,
					Username: resp.Proxy.Username,
					Password: resp.Proxy.Password,
					Type:     proxyTypeStr(resp.Proxy.Type),
				}
			}
		}

		proxyID := ""
		if row.ProxyID != nil {
			proxyID = *row.ProxyID
		}
		_, _, err := mgr.Connect(ctx, row.ID, row.Phone, row.JID, proxyID, proxyCfg)
		if err != nil {
			log.Error().Err(err).Str("wa_number_id", row.ID).Msg("failed to reconnect session")
		}
	}
}

func proxyTypeStr(t hermesv1.ProxyType) string {
	switch t {
	case hermesv1.ProxyType_PROXY_TYPE_HTTP:
		return "http"
	default:
		return "socks5"
	}
}

// tenantCache caches waNumberID → tenantID lookups.
type tenantCache struct {
	mu    sync.RWMutex
	cache map[string]string
	store handler.Store
}

func newTenantCache(s handler.Store) *tenantCache {
	return &tenantCache{cache: make(map[string]string), store: s}
}

func (c *tenantCache) Get(waNumberID string) string {
	c.mu.RLock()
	tid, ok := c.cache[waNumberID]
	c.mu.RUnlock()
	if ok {
		return tid
	}

	tid, err := c.store.GetTenantID(context.Background(), waNumberID)
	if err != nil {
		return "unknown"
	}

	c.mu.Lock()
	c.cache[waNumberID] = tid
	c.mu.Unlock()
	return tid
}
