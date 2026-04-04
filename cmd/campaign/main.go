package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	campaignconfig "github.com/hermes-waba/hermes/internal/campaign/config"
	"github.com/hermes-waba/hermes/internal/campaign/engine"
	"github.com/hermes-waba/hermes/internal/campaign/handler"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func main() {
	cfg := campaignconfig.Load()
	log := logger.New("hermes-campaign")

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

	// Campaign dispatch engine
	eng := engine.NewEngine(store, js, log)

	// NATS consumers
	if err := startBanConsumer(js, store, eng, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start ban consumer")
	}
	if err := startInboundConsumer(js, store, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start inbound consumer")
	}

	// gRPC server
	h := handler.New(store, eng, log)
	grpcServer := grpc.NewServer()
	hermesv1.RegisterHermesCampaignServer(grpcServer, h)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info().Msg("shutting down hermes-campaign")
		grpcServer.GracefulStop()
	}()

	log.Info().Int("port", cfg.Port).Msg("hermes-campaign started")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}
}

// ensureStreams creates the NATS streams the campaign service needs.
func ensureStreams(js natsgo.JetStreamContext) error {
	// HERMES_WA stream (ban events, inbound messages come from here)
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_WA",
		Subjects: []string{"hermes.wa.message.>", "hermes.wa.ban.>", "hermes.wa.connection.>", "hermes.wa.presence.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	}); err != nil {
		return fmt.Errorf("ensuring HERMES_WA stream: %w", err)
	}

	// HERMES_CAMPAIGN stream (send tasks, status events, progress events)
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_CAMPAIGN",
		Subjects: []string{"hermes.campaign.>", "hermes.wa.send.campaign.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   30 * 24 * time.Hour,
	}); err != nil {
		return fmt.Errorf("ensuring HERMES_CAMPAIGN stream: %w", err)
	}

	return nil
}

// startBanConsumer handles WaBanEvent — removes banned numbers from active campaigns,
// checks ban threshold, and auto-pauses if needed.
func startBanConsumer(js natsgo.JetStreamContext, store handler.Store, eng *engine.Engine, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.ban.*", func(msg *natsgo.Msg) {
		var event hermesv1.WaBanEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal ban event")
			msg.Ack()
			return
		}

		ctx := context.Background()
		waNumberID := event.WaNumberId

		// Find all active campaigns using this number.
		campaigns, err := store.GetCampaignsUsingNumber(ctx, waNumberID, []string{"running", "paused"})
		if err != nil {
			log.Error().Err(err).Str("wa_number_id", waNumberID).Msg("failed to find campaigns using banned number")
			msg.Nak()
			return
		}

		for _, campaign := range campaigns {
			// Mark number as banned in campaign.
			if err := store.UpdateCampaignNumberStatus(ctx, campaign.ID, waNumberID, "banned"); err != nil {
				log.Error().Err(err).Str("campaign_id", campaign.ID).Msg("failed to ban number in campaign")
			}

			// Increment campaign banned count.
			bannedCount, err := store.IncrementBannedCount(ctx, campaign.ID)
			if err != nil {
				log.Error().Err(err).Str("campaign_id", campaign.ID).Msg("failed to increment banned count")
				continue
			}

			// Check ban threshold — auto-pause if exceeded.
			if campaign.BanPauseThreshold > 0 && bannedCount >= campaign.BanPauseThreshold && campaign.Status == "running" {
				eng.Stop(campaign.ID)
				if _, err := store.UpdateCampaignStatus(ctx, campaign.ID, "paused", false, false); err != nil {
					log.Error().Err(err).Str("campaign_id", campaign.ID).Msg("failed to auto-pause campaign")
				} else {
					log.Warn().
						Str("campaign_id", campaign.ID).
						Int32("banned_count", bannedCount).
						Int32("threshold", campaign.BanPauseThreshold).
						Msg("campaign auto-paused due to ban threshold")
				}
			}
		}

		msg.Ack()
	},
		natsgo.Durable("campaign-ban"),
		natsgo.ManualAck(),
		natsgo.AckWait(30*time.Second),
		natsgo.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("subscribing to ban events: %w", err)
	}

	log.Info().Str("subject", "hermes.wa.ban.*").Msg("ban event consumer started")
	return nil
}

// startInboundConsumer handles WaInboundMessageEvent — tracks replies from contacts
// that are in active campaigns.
func startInboundConsumer(js natsgo.JetStreamContext, store handler.Store, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.message.inbound.*", func(msg *natsgo.Msg) {
		var event hermesv1.WaInboundMessageEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal inbound message event")
			msg.Ack()
			return
		}

		ctx := context.Background()

		// Check if sender matches any contact in active campaigns.
		matches, err := store.FindContactInActiveCampaigns(ctx, event.SenderPhone)
		if err != nil {
			log.Error().Err(err).Str("sender_phone", event.SenderPhone).Msg("failed to find contact in campaigns")
			msg.Nak()
			return
		}

		for _, match := range matches {
			if err := store.IncrementRepliedCount(ctx, match.CampaignID); err != nil {
				log.Error().Err(err).Str("campaign_id", match.CampaignID).Msg("failed to increment replied count")
			}
		}

		msg.Ack()
	},
		natsgo.Durable("campaign-inbound"),
		natsgo.ManualAck(),
		natsgo.AckWait(30*time.Second),
		natsgo.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("subscribing to inbound events: %w", err)
	}

	log.Info().Str("subject", "hermes.wa.message.inbound.*").Msg("inbound message consumer started")
	return nil
}
