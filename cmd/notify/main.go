package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/notify/config"
	"github.com/hermes-waba/hermes/internal/notify/dispatch"
	"github.com/hermes-waba/hermes/internal/notify/handler"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
)

func main() {
	log := logger.New("hermes-notify")

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Database
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()
	log.Info().Msg("connected to database")

	// NATS
	js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to NATS")
	}
	defer nc.Close()
	log.Info().Msg("connected to NATS")

	// Ensure stream exists
	if err := ensureStream(js); err != nil {
		log.Fatal().Err(err).Msg("failed to ensure HERMES_NOTIFY stream")
	}

	// Dependencies
	store := handler.NewPGStore(pool)
	disp := dispatch.New(nil, nc, log)
	h := handler.New(store, disp, log)

	// NATS consumer for notify.dispatch events
	sub, err := startConsumer(js, store, disp, log)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start NATS consumer")
	}
	defer func() {
		if err := sub.Drain(); err != nil {
			log.Error().Err(err).Msg("draining NATS subscription")
		}
	}()
	log.Info().Msg("NATS consumer started for hermes.notify.dispatch.*")

	// gRPC server
	srv := grpc.NewServer()
	hermesv1.RegisterHermesNotifyServer(srv, h)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	go func() {
		<-ctx.Done()
		log.Info().Msg("shutting down gRPC server")
		srv.GracefulStop()
	}()

	log.Info().Int("port", cfg.Port).Msg("gRPC server listening")
	if err := srv.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}
}

func ensureStream(js natsgo.JetStreamContext) error {
	_, err := js.StreamInfo("HERMES_NOTIFY")
	if err == nil {
		return nil // stream already exists
	}
	_, err = js.AddStream(&natsgo.StreamConfig{
		Name:       "HERMES_NOTIFY",
		Subjects:   []string{"hermes.notify.>"},
		Storage:    natsgo.FileStorage,
		Retention:  natsgo.WorkQueuePolicy,
		MaxAge:     time.Hour,
		MaxBytes:   500 * 1024 * 1024, // 500 MB
		MaxMsgSize: 64 * 1024,         // 64 KB
	})
	if err != nil {
		return fmt.Errorf("creating HERMES_NOTIFY stream: %w", err)
	}
	return nil
}

func startConsumer(js natsgo.JetStreamContext, store handler.Store, disp *dispatch.Dispatcher, log zerolog.Logger) (*natsgo.Subscription, error) {
	return js.Subscribe("hermes.notify.dispatch.*", func(msg *natsgo.Msg) {
		var event hermesv1.NotifyDispatchEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal dispatch event")
			_ = msg.Ack() // don't retry on unmarshal error
			return
		}

		configs, err := store.ListEnabledConfigs(context.Background(), event.WorkspaceId)
		if err != nil {
			log.Error().Err(err).Str("workspace_id", event.WorkspaceId).Msg("failed to list enabled configs")
			_ = msg.Nak() // retry
			return
		}

		tenantID := ""
		if event.Meta != nil {
			tenantID = event.Meta.TenantId
		}

		for _, cfg := range configs {
			target := dispatch.Target{
				Type:        cfg.Type,
				WebhookURL:  cfg.WebhookURL,
				WebhookType: cfg.WebhookType,
			}
			result := disp.Dispatch(context.Background(), target, event.Title, event.Body, tenantID)
			if result.Err != nil {
				log.Error().Err(result.Err).
					Str("config_id", cfg.ID).
					Str("type", cfg.Type).
					Msg("notification dispatch failed")
			} else {
				log.Info().
					Str("config_id", cfg.ID).
					Str("type", cfg.Type).
					Int("http_status", result.HTTPStatus).
					Msg("notification dispatched")
			}
		}

		_ = msg.Ack()
	},
		natsgo.Durable("notify-dispatch"),
		natsgo.ManualAck(),
		natsgo.MaxDeliver(5),
		natsgo.AckWait(30*time.Second),
	)
}
