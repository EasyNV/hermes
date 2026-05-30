package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
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
	"github.com/hermes-waba/hermes/pkg/observability"
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
		// /readyz → 503 before gRPC stops accepting.
		diagSrv.SetReady(false)
		srv.GracefulStop()
	}()

	// Mark ready: gRPC listener + diag listener + deps are all up.
	diagSrv.SetReady(true)

	log.Info().Int("port", cfg.Port).Int("metrics_port", cfg.MetricsPort).Msg("gRPC server listening")
	if err := srv.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}

	// Diag last — operators see /livez until shutdown completes.
	diagCancel()
	if err := <-diagErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn().Err(err).Msg("diagnostic HTTP server exited with error")
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
