package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/contacts/config"
	"github.com/hermes-waba/hermes/internal/contacts/handler"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
	"github.com/hermes-waba/hermes/pkg/observability"
)

func main() {
	log := logger.New("hermes-contacts")

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to NATS")
	}
	defer nc.Close()

	// ── Diagnostic HTTP server (Stage F chunk 4).
	// Pre-bind so a port collision surfaces synchronously, then Serve
	// on a goroutine. /readyz returns 503 until SetReady(true) below.
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

	store := handler.NewPgxStore(pool)
	h := handler.New(store, js, log)

	srv := grpc.NewServer()
	hermesv1.RegisterHermesContactsServer(srv, h)
	reflection.Register(srv)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	// Mark ready: gRPC listener is bound, deps are reachable.
	diagSrv.SetReady(true)

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Info().Msg("shutting down")
		// Drop /readyz to 503 BEFORE gRPC stops accepting — gives any
		// upstream `depends_on: service_healthy` graph time to drain.
		diagSrv.SetReady(false)
		srv.GracefulStop()
		cancel()
	}()

	log.Info().Int("port", cfg.Port).Int("metrics_port", cfg.MetricsPort).Msg("starting hermes-contacts")
	if err := srv.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("server failed")
	}

	// Diag last — operators see /livez until the very end.
	diagCancel()
	if err := <-diagErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn().Err(err).Msg("diagnostic HTTP server exited with error")
	}
}
