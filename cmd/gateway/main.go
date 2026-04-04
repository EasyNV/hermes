package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	gwconfig "github.com/hermes-waba/hermes/internal/gateway/config"
	"github.com/hermes-waba/hermes/internal/gateway/handler"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
	gwrest "github.com/hermes-waba/hermes/internal/gateway/rest"
	gwws "github.com/hermes-waba/hermes/internal/gateway/websocket"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
	natsgo "github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	cfg := gwconfig.Load()
	log := logger.New("hermes-gateway")
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

	// Backend gRPC client connections
	var (
		waClient       hermesv1.HermesWaClient
		proxyClient    hermesv1.HermesProxyClient
		contactsClient hermesv1.HermesContactsClient
		campaignClient hermesv1.HermesCampaignClient
		inboxClient    hermesv1.HermesInboxClient
		notifyClient   hermesv1.HermesNotifyClient
	)
	if c := dialService(cfg.WaAddr); c != nil {
		waClient = hermesv1.NewHermesWaClient(c)
	}
	if c := dialService(cfg.ProxyAddr); c != nil {
		proxyClient = hermesv1.NewHermesProxyClient(c)
	}
	if c := dialService(cfg.ContactsAddr); c != nil {
		contactsClient = hermesv1.NewHermesContactsClient(c)
	}
	if c := dialService(cfg.CampaignAddr); c != nil {
		campaignClient = hermesv1.NewHermesCampaignClient(c)
	}
	if c := dialService(cfg.InboxAddr); c != nil {
		inboxClient = hermesv1.NewHermesInboxClient(c)
	}
	if c := dialService(cfg.NotifyAddr); c != nil {
		notifyClient = hermesv1.NewHermesNotifyClient(c)
	}

	// Handler
	h := handler.New(
		store, []byte(cfg.JWTSecret), log,
		waClient, proxyClient, contactsClient, campaignClient, inboxClient, notifyClient,
	)

	// gRPC server with auth + RBAC interceptors
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.AuthInterceptor([]byte(cfg.JWTSecret)),
			middleware.RBACInterceptor(),
		),
	)
	hermesv1.RegisterHermesGatewayServer(grpcServer, h)

	// WebSocket hub
	hub := gwws.NewHub([]byte(cfg.JWTSecret), log)
	eventSub := gwws.NewEventSubscriber(hub, js, log)
	if err := eventSub.Start(); err != nil {
		log.Warn().Err(err).Msg("failed to start NATS event subscriber (WebSocket events will not work)")
	}

	// REST adapter — JSON-over-HTTP for the frontend
	// WA HTTP addr is WA gRPC addr with port+1 (e.g. wa:9104 → wa:9105)
	waHTTPAddr := strings.Replace(cfg.WaAddr, "9104", "9105", 1)
	restAdapter := gwrest.New(h, []byte(cfg.JWTSecret), log, waHTTPAddr)

	// HTTP server for REST API + WebSocket endpoint
	mux := http.NewServeMux()
	restAdapter.Register(mux)
	mux.Handle("/ws", hub)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port+1),
		Handler: restAdapter.CORS(mux),
	}
	go func() {
		log.Info().Int("port", cfg.Port+1).Msg("WebSocket server started")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("WebSocket server failed")
		}
	}()

	// gRPC listener
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("failed to listen")
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Info().Msg("shutting down hermes-gateway")
		grpcServer.GracefulStop()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutCtx)
		eventSub.Stop()
	}()

	log.Info().Int("port", cfg.Port).Msg("hermes-gateway started")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server failed")
	}
}

func dialService(addr string) grpc.ClientConnInterface {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil
	}
	return conn
}

func ensureStreams(js natsgo.JetStreamContext) error {
	streams := []struct {
		name     string
		subjects []string
		maxAge   time.Duration
	}{
		{"HERMES_WA", []string{"hermes.wa.message.>", "hermes.wa.ban.>", "hermes.wa.connection.>", "hermes.wa.presence.>"}, 7 * 24 * time.Hour},
		{"HERMES_CAMPAIGN", []string{"hermes.campaign.>", "hermes.wa.send.campaign.>"}, 30 * 24 * time.Hour},
		{"HERMES_INBOX", []string{"hermes.wa.send.manual.>"}, 24 * time.Hour},
		{"HERMES_CONTACTS", []string{"hermes.contacts.>"}, 24 * time.Hour},
		{"HERMES_NOTIFY", []string{"hermes.notify.>"}, time.Hour},
	}

	for _, s := range streams {
		_, err := js.AddStream(&natsgo.StreamConfig{
			Name:     s.name,
			Subjects: s.subjects,
			Storage:  natsgo.FileStorage,
			MaxAge:   s.maxAge,
		})
		if err != nil {
			return fmt.Errorf("ensuring %s stream: %w", s.name, err)
		}
	}
	return nil
}
