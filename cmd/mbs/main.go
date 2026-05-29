// Command hermes-mbs is the gRPC microservice that owns Meta Business
// Suite session lifecycle, bridge-login orchestration, phone
// resolution, send, and inbound streaming for the Hermès platform.
//
// Wires together chunks 1-5:
//
//   - chunk 1: pkg/crypto DEK loading
//   - chunk 2: internal/mbs/store + observability HTTP server +
//              internal/mbs/config env loading
//   - chunk 3: internal/mbs/session manager
//   - chunk 4: internal/mbs/handler (7 gRPC RPCs + tenant interceptor)
//   - chunk 5: internal/mbs/bridge mautrix driver factory
//
// Boots in this order:
//
//  1. signal context (SIGINT/SIGTERM)
//  2. DEK (file > env > fatal)
//  3. Postgres pool + Ping
//  4. NATS JetStream + ensure streams
//  5. Event publisher + session manager + bridge factory + handler
//  6. Diagnostic HTTP server (bound synchronously, served on goroutine)
//  7. gRPC server (tenant interceptors + keepalive + reflection + health)
//  8. NATS send consumers (campaign + manual)
//  9. Background reconnect of pod-owned sessions
//  10. SetReady(true) — /readyz starts returning 200
//
// Shuts down in reverse:
//
//   signal → SetReady(false) → flip gRPC health NOT_SERVING →
//   NATS Drain (stop consuming, finish in-flight) → mgr.Drain
//   (refuse new connects) → grpcSrv.GracefulStop (or .Stop on
//   drain timeout) → mgr.Shutdown (disconnect every uid +
//   release every claim) → diag server shutdown
//
// Fail-closed at boot: missing DEK, DB connect failure, NATS connect
// failure, or stream-ensure failure all log.Fatal.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	grpchealth "google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	natsgo "github.com/nats-io/nats.go"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/mbs/bridge"
	mbsconfig "github.com/hermes-waba/hermes/internal/mbs/config"
	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/importer"
	"github.com/hermes-waba/hermes/internal/mbs/observability"
	"github.com/hermes-waba/hermes/internal/mbs/refresh"
	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
)

func main() {
	cfg := mbsconfig.Load()
	log := logger.New("hermes-mbs").With().Str("pod_id", cfg.PodID).Logger()

	rootCtx, rootCancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer rootCancel()

	// ── 1. DEK (file > env > fatal)
	dek, err := loadDEK(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("DEK load failed (fail-closed)")
	}
	log.Info().Msg("DEK loaded")

	// ── 2. Postgres pool with managed-DB-friendly options
	if cfg.DatabaseURL == "" {
		log.Fatal().Msg("DATABASE_URL is required")
	}
	pool, err := db.NewPoolWithOpts(rootCtx, cfg.DatabaseURL, db.PoolOpts{
		SSLMode:         cfg.DBSSLMode,
		SSLRootCert:     cfg.DBSSLRootCert,
		MaxConns:        int32(cfg.DBMaxConns),
		ConnMaxLifetime: cfg.DBConnMaxLife,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("postgres connect failed")
	}
	defer pool.Close()
	st := store.NewPgStore(pool)
	log.Info().Msg("postgres connected")

	// ── 3. NATS JetStream
	js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("NATS connect failed")
	}
	defer nc.Close()
	if err := ensureStreams(js, cfg.StreamReplicas); err != nil {
		log.Fatal().Err(err).Msg("ensure NATS streams failed")
	}
	log.Info().Msg("NATS streams ensured")

	// ── 3a. Legacy importer (one-shot, env-triggered).
	// Runs BEFORE the event publisher is constructed so a fresh
	// deploy can: (1) boot, (2) ingest the JSON archive, (3) start
	// serving in one process restart. Operator MUST unset the env
	// after the first deploy — chunk 8 README documents this.
	//
	// Publisher is intentionally nil for the bootstrap path: lifecycle
	// events would race the gRPC server coming online (subscribers
	// haven't connected yet). Downstream services discover the new
	// rows via their next ListMbsSessions call, or naturally when
	// GetOrConnect lands and emits its own "activated" event.
	if cfg.ImportLegacyOnStartup {
		if cfg.ImportLegacyDir == "" || cfg.ImportLegacyTenantID == "" {
			log.Fatal().Msg("MBS_IMPORT_LEGACY_ON_STARTUP=true requires " +
				"MBS_IMPORT_LEGACY_DIR and MBS_IMPORT_LEGACY_TENANT_ID")
		}
		log.Warn().
			Str("legacy_dir", cfg.ImportLegacyDir).
			Str("tenant_id", cfg.ImportLegacyTenantID).
			Msg("MBS_IMPORT_LEGACY_ON_STARTUP=true — remember to unset after this deploy")
		stats, err := importer.Run(rootCtx, importer.Options{
			SessionsDir: cfg.ImportLegacyDir,
			TenantID:    cfg.ImportLegacyTenantID,
			Store:       st,
			DEK:         dek,
			Publisher:   nil, // see comment above
			Logger:      log,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("legacy import failed (boot aborted)")
		}
		log.Info().
			Int("total", stats.Total).
			Int("imported", stats.Imported).
			Int("forced", stats.Forced).
			Int("skipped", stats.Skipped).
			Int("failed", stats.Failed).
			Msg("legacy import complete")
		if stats.Failed > 0 {
			log.Warn().
				Int("failed", stats.Failed).
				Msg("legacy import had failures — review logs before clearing the env flag")
		}
	}

	// ── 4. Event publisher (NATS-backed)
	pub := handler.NewNatsEventPublisher(js, log)

	// ── 5. Session manager
	mgr := session.NewManager(session.Opts{
		Store:  st,
		DEK:    dek,
		PodID:  cfg.PodID,
		Logger: log,
	})
	log.Info().Msg("session manager ready")

	// ── 6. Bridge driver factory (chunk 5)
	if cfg.MautrixDisableTLS {
		log.Warn().
			Bool("process_wide", true).
			Bool("unrecoverable_until_restart", true).
			Msg("HERMES_MBS_DISABLE_TLS=true — mautrix-meta TLS verification " +
				"disabled process-wide. Do NOT run multi-tenant in this mode. " +
				"Audit ref: chunk-5 F1.")
	}
	driverFactory := bridge.NewDriverFactory(bridge.Deps{
		Logger:           log,
		DisableTLSVerify: cfg.MautrixDisableTLS,
		Timeout:          cfg.BridgeOverallTimeout,
		Await2FATimeout:  cfg.Bridge2FATimeout,
	})

	// ── 7. Handler
	h, err := handler.NewHandler(handler.Options{
		Store:                     st,
		Manager:                   mgr,
		Publisher:                 pub,
		DriverFactory:             driverFactory,
		DEK:                       dek,
		PodID:                     cfg.PodID,
		Logger:                    log,
		MaxConcurrentBridgeLogins: cfg.BridgeMaxConcurrent,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("handler construct failed")
	}

	// ── 8. Diagnostic HTTP server: bind synchronously so a port
	// collision surfaces at boot, then Serve on a goroutine.
	// (Chunk-5 audit R2: don't hide bind errors until shutdown.)
	diagSrv := observability.NewHTTPServer(observability.Options{
		Addr:       fmt.Sprintf(":%d", cfg.MetricsPort),
		Registerer: prometheus.DefaultGatherer,
		ReadinessFn: func(ctx context.Context) error {
			return st.Ping(ctx)
		},
		EnablePprof: cfg.EnablePprof,
	})
	diagListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.MetricsPort))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.MetricsPort).
			Msg("diagnostic HTTP listen failed")
	}
	diagDoneCtx, diagDoneCancel := context.WithCancel(context.Background())
	diagErrCh := make(chan error, 1)
	go func() {
		diagErrCh <- serveDiag(diagDoneCtx, diagSrv, diagListener)
	}()

	// ── 9. gRPC server with tenant interceptors + keepalive
	grpcSrv := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    cfg.GRPCKeepaliveTime,
			Timeout: cfg.GRPCKeepaliveTimeout,
		}),
		grpc.MaxConcurrentStreams(cfg.GRPCMaxConcurrentStreams),
		grpc.ChainUnaryInterceptor(handler.TenantUnaryInterceptor()),
		grpc.ChainStreamInterceptor(handler.TenantStreamInterceptor()),
	)
	hermesv1.RegisterHermesMbsServer(grpcSrv, h)

	healthSrv := grpchealth.NewServer()
	healthSrv.SetServingStatus("hermes.v1.HermesMbs", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	reflection.Register(grpcSrv)

	// ── 10. NATS send consumers (campaign + manual)
	if err := startCampaignConsumer(js, h, log); err != nil {
		log.Fatal().Err(err).Msg("start campaign consumer failed")
	}
	if err := startManualConsumer(js, h, log); err != nil {
		log.Fatal().Err(err).Msg("start manual consumer failed")
	}

	// ── 11. Reconnect sessions assigned to this pod (background)
	go reconnectPodSessions(rootCtx, st, mgr, cfg.PodID, log)

	// ── 11b. Refresh ticker (chunk 7) — keeps cookies fresh against
	// Meta's 30d expiry. One goroutine; bounded concurrent fan-out
	// per tick. Pod-startup jitter spreads fleet-bounce load.
	refreshMetrics := refresh.NewMetrics(prometheus.DefaultRegisterer)
	refreshTicker, err := refresh.New(refresh.Options{
		Store:       st,
		DEK:         dek,
		Publisher:   pub,
		PodID:       cfg.PodID,
		Interval:    cfg.RefreshInterval,
		Threshold:   cfg.RefreshThreshold,
		Concurrency: cfg.RefreshConcurrency,
		JitterCap:   5 * time.Minute, // explicit; zero would disable
		Logger:      log,
		Metrics:     refreshMetrics,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("refresh ticker construct failed")
	}
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		if err := refreshTicker.Run(rootCtx); err != nil {
			log.Error().Err(err).Msg("refresh ticker exited with error")
		}
	}()

	// ── 12. Flip ready, start gRPC listener
	diagSrv.SetReady(true)
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal().Err(err).Int("port", cfg.Port).Msg("gRPC listen failed")
	}
	log.Info().
		Int("port", cfg.Port).
		Int("metrics_port", cfg.MetricsPort).
		Str("pod_id", cfg.PodID).
		Msg("hermes-mbs ready")

	grpcErrCh := make(chan error, 1)
	go func() { grpcErrCh <- grpcSrv.Serve(lis) }()

	// ── 13. Wait for shutdown signal or gRPC failure
	select {
	case <-rootCtx.Done():
		log.Info().Msg("shutdown signal received; draining")
	case err := <-grpcErrCh:
		if err != nil {
			log.Error().Err(err).Msg("gRPC server exited unexpectedly")
		} else {
			log.Info().Msg("gRPC server stopped")
		}
	}

	// ── 14. Drain phase. Order matters (chunk-5 audit R3):
	//
	//   a) flip /readyz to 503 so K8s removes us from svc endpoints
	//   b) flip gRPC health to NOT_SERVING so peers stop routing
	//   c) Drain NATS subs — finish in-flight, refuse new (this is
	//      what stops send consumers from picking up new tasks
	//      while we're tearing down the manager)
	//   d) mgr.Drain — refuse new GetOrConnect
	//   e) grpc.GracefulStop with timeout fallback to Stop()
	//   f) mgr.Shutdown — disconnect every uid + release every claim
	//   g) diag server shutdown last (operators get /healthz till
	//      the very end of the lifecycle)
	diagSrv.SetReady(false)
	healthSrv.SetServingStatus("hermes.v1.HermesMbs",
		grpc_health_v1.HealthCheckResponse_NOT_SERVING)

	drainCtx, drainCancel := context.WithTimeout(
		context.Background(), cfg.ShutdownDrainTimeout)
	defer drainCancel()

	// (c) NATS first — stop consumers from picking up new tasks.
	// nc.Drain() returns immediately; the actual drain runs in a
	// background goroutine inside nats.go. Wait synchronously for
	// the connection to reach CLOSED (drain complete) OR for our
	// drainCtx to fire — whichever comes first. This ensures NATS
	// consumer SendMessage calls have finished before mgr.Drain
	// closes the door on new GetOrConnect.
	if err := nc.Drain(); err != nil {
		log.Warn().Err(err).Msg("NATS Drain returned error (consumers may still be active)")
	} else {
		statusCh := nc.StatusChanged(natsgo.CLOSED)
		select {
		case <-statusCh:
			log.Info().Msg("NATS connection drained")
		case <-drainCtx.Done():
			log.Warn().Msg("NATS drain wait exceeded drainCtx; continuing shutdown")
		}
	}

	// Wait for refresh ticker to exit. rootCtx already canceled by
	// the shutdown signal; the ticker should observe and exit
	// within 100ms. Bounded by drainCtx.
	select {
	case <-refreshDone:
		log.Info().Msg("refresh ticker drained")
	case <-drainCtx.Done():
		log.Warn().Msg("refresh ticker drain wait exceeded drainCtx; continuing shutdown")
	}

	_ = mgr.Drain(drainCtx)

	stopDone := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		log.Info().Msg("gRPC GracefulStop complete")
	case <-drainCtx.Done():
		log.Warn().Msg("drain timeout exceeded; force-stopping gRPC")
		grpcSrv.Stop()
	}

	if err := mgr.Shutdown(drainCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Warn().Err(err).Msg("manager Shutdown returned error")
	}

	// Diag last — operators see /healthz until the very end.
	diagDoneCancel()
	if err := <-diagErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn().Err(err).Msg("diagnostic HTTP server exited with error")
	}

	log.Info().Msg("shutdown complete")
}

// loadDEK reads cfg.DEKFile first (recommended for K8s tmpfs/projected
// volume), then falls back to cfg.DEKHex (env var, fine for compose/VPS).
// Fails closed if neither is usable.
//
// Surface returns a strong error message describing which source was
// tried — operators see "no DEK source: set HERMES_MBS_DEK_FILE or
// HERMES_MBS_DEK_HEX" instead of an opaque crypto error.
func loadDEK(cfg mbsconfig.Config) (crypto.DataEncryptionKey, error) {
	if cfg.DEKFile != "" {
		k, err := crypto.LoadDEKFromFile(cfg.DEKFile)
		if err != nil {
			return crypto.DataEncryptionKey{}, fmt.Errorf(
				"DEK file %q: %w", cfg.DEKFile, err)
		}
		return k, nil
	}
	if cfg.DEKHex != "" {
		k, err := crypto.LoadDEKFromHexEnv("HERMES_MBS_DEK_HEX")
		if err != nil {
			return crypto.DataEncryptionKey{}, fmt.Errorf(
				"DEK env HERMES_MBS_DEK_HEX: %w", err)
		}
		return k, nil
	}
	return crypto.DataEncryptionKey{}, errors.New(
		"no DEK source configured: set HERMES_MBS_DEK_FILE or HERMES_MBS_DEK_HEX")
}

// serveDiag drives the observability HTTP server on a pre-bound
// listener so port-collision errors surface synchronously at boot
// (chunk-5 audit R2). Returns nil on graceful ctx shutdown.
func serveDiag(ctx context.Context, srv *observability.HTTPServer, lis net.Listener) error {
	// Build an http.Server that reuses the observability HTTPServer's
	// handler. The observability.HTTPServer wraps its own internal
	// http.Server; we re-use the Handler() and drive Serve ourselves
	// against the listener we already bound in main.
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(lis) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}
