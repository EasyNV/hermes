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
	proxyconfig "github.com/hermes-waba/hermes/internal/proxy/config"
	"github.com/hermes-waba/hermes/internal/proxy/handler"
	"github.com/hermes-waba/hermes/internal/proxy/health"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func main() {
	cfg := proxyconfig.Load()
	log := logger.New("hermes-proxy")

	ctx := context.Background()

	// PostgreSQL
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	store := handler.NewPgStore(pool)
	checker := health.NewChecker(10 * time.Second)

	// NATS JetStream
	js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to NATS")
	}
	defer nc.Close()

	if err := ensureStream(js); err != nil {
		log.Fatal().Err(err).Msg("failed to ensure NATS stream")
	}

	if err := startBanConsumer(js, store, cfg.BanFlagThreshold, log); err != nil {
		log.Fatal().Err(err).Msg("failed to start ban consumer")
	}

	// gRPC server
	h := handler.NewHandler(store, checker, log)
	grpcServer := grpc.NewServer()
	hermesv1.RegisterHermesProxyServer(grpcServer, h)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info().Msg("shutting down hermes-proxy")
		grpcServer.GracefulStop()
	}()

	log.Info().Int("port", cfg.Port).Msg("hermes-proxy started")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}
}

// ensureStream creates the HERMES_WA stream if it doesn't already exist.
func ensureStream(js natsgo.JetStreamContext) error {
	_, err := js.AddStream(&natsgo.StreamConfig{
		Name:     "HERMES_WA",
		Subjects: []string{"hermes.wa.message.>", "hermes.wa.ban.>", "hermes.wa.connection.>", "hermes.wa.presence.>"},
		Storage:  natsgo.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("ensuring HERMES_WA stream: %w", err)
	}
	return nil
}

// startBanConsumer subscribes to wa.ban events and auto-flags proxies when
// their ban count exceeds the configured threshold.
func startBanConsumer(js natsgo.JetStreamContext, store handler.Store, threshold int, log zerolog.Logger) error {
	_, err := js.Subscribe("hermes.wa.ban.*", func(msg *natsgo.Msg) {
		var event hermesv1.WaBanEvent
		if err := proto.Unmarshal(msg.Data, &event); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal ban event")
			msg.Ack()
			return
		}

		if event.ProxyId == "" {
			msg.Ack()
			return
		}

		newCount, err := store.IncrementBanCount(context.Background(), event.ProxyId)
		if err != nil {
			log.Error().Err(err).Str("proxy_id", event.ProxyId).Msg("failed to increment ban count")
			msg.Nak()
			return
		}

		if threshold > 0 && int(newCount) >= threshold {
			if _, err := store.FlagProxy(context.Background(), event.ProxyId); err != nil {
				log.Error().Err(err).Str("proxy_id", event.ProxyId).Msg("failed to auto-flag proxy")
			} else {
				log.Warn().
					Str("proxy_id", event.ProxyId).
					Int32("ban_count", newCount).
					Msg("proxy auto-flagged due to high ban count")
			}
		}

		msg.Ack()
	},
		natsgo.Durable("proxy-ban"),
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
