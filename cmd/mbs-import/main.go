// Command mbs-import migrates ~/.mbs-native/sessions/ legacy JSON
// archives into the hermes-mbs encrypted Postgres rows.
//
// Operator workflow (typical):
//
//  1. Deploy hermes-mbs once with chunk 2 migrations applied:
//     migrate -path migrations/mbs -database "$DATABASE_URL" up
//
//  2. Copy the legacy session directory onto the host where this
//     binary runs (or mount it). The importer NEVER writes back to
//     the JSON files — they become read-only archives.
//
//  3. Run with the SAME DEK + DATABASE_URL the live service uses:
//
//     HERMES_MBS_DEK_FILE=/run/secrets/mbs_dek \
//     DATABASE_URL=postgres://... \
//     ./mbs-import \
//     --sessions-dir /var/lib/mbs-native/sessions \
//     --tenant 01HXYZK4M2... \
//     [--dry-run] [--force]
//
//  4. Inspect exit code:
//
//     0  every session imported (or skipped under !force)
//     1  partial — some sessions failed; the run completed
//     2  abort — invalid flags / DB unreachable / DEK missing
//
//  5. Remove --sessions-dir from the host once you're confident.
//
// Idempotency: re-running over the same directory is safe —
// existing rows are skipped unless --force is set. Cross-tenant
// collisions are NEVER overwritten, even with --force.
//
// One-tenant-per-run: multi-tenant imports = multiple invocations.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	mbsconfig "github.com/hermes-waba/hermes/internal/mbs/config"
	"github.com/hermes-waba/hermes/internal/mbs/handler"
	"github.com/hermes-waba/hermes/internal/mbs/importer"
	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/pkg/crypto"
	"github.com/hermes-waba/hermes/pkg/db"
	"github.com/hermes-waba/hermes/pkg/logger"
	hermesnats "github.com/hermes-waba/hermes/pkg/nats"
)

// Exit code constants — match the documented contract above.
const (
	exitOK      = 0
	exitPartial = 1 // >0 failed sessions but run completed
	exitAbort   = 2 // could not start at all
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the testable entry point — returns the exit code instead of
// calling os.Exit. Lets the unit tests assert flag-validation paths
// without spawning subprocesses.
func run(args []string) int {
	fs := flag.NewFlagSet("mbs-import", flag.ContinueOnError)
	var (
		sessionsDir = fs.String("sessions-dir", "", "directory containing legacy <uid>.json files (required)")
		tenantID    = fs.String("tenant", "", "tenant_id to associate with every imported session (required)")
		dryRun      = fs.Bool("dry-run", false, "parse + encrypt without DB writes")
		force       = fs.Bool("force", false, "overwrite existing same-tenant rows (cross-tenant always refused)")
		noPublish   = fs.Bool("no-publish", false, "skip NATS lifecycle events even when NATS_URL is set")
	)
	if err := fs.Parse(args); err != nil {
		// flag already printed usage; ContinueOnError gives us this.
		return exitAbort
	}

	if *sessionsDir == "" {
		fmt.Fprintln(os.Stderr, "error: --sessions-dir is required")
		fs.Usage()
		return exitAbort
	}
	if *tenantID == "" {
		fmt.Fprintln(os.Stderr, "error: --tenant is required")
		fs.Usage()
		return exitAbort
	}

	log := logger.New("mbs-import")

	// SIGINT/SIGTERM-aware context so a long import can be canceled
	// without leaving torn rows. Run validates the cancel.
	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── DEK (mirrors cmd/mbs/main.go loadDEK).
	cfg := mbsconfig.Load()
	dek, err := loadDEK(cfg)
	if err != nil {
		log.Error().Err(err).Msg("DEK load failed")
		return exitAbort
	}
	log.Info().Msg("DEK loaded")

	// ── Postgres.
	if cfg.DatabaseURL == "" {
		log.Error().Msg("DATABASE_URL is required")
		return exitAbort
	}
	pool, err := db.NewPoolWithOpts(ctx, cfg.DatabaseURL, db.PoolOpts{
		SSLMode:         cfg.DBSSLMode,
		SSLRootCert:     cfg.DBSSLRootCert,
		MaxConns:        int32(cfg.DBMaxConns),
		ConnMaxLifetime: cfg.DBConnMaxLife,
	})
	if err != nil {
		log.Error().Err(err).Msg("postgres connect failed")
		return exitAbort
	}
	defer pool.Close()
	st := store.NewPgStore(pool)
	if err := st.Ping(ctx); err != nil {
		log.Error().Err(err).Msg("postgres ping failed")
		return exitAbort
	}
	log.Info().Msg("postgres connected")

	// ── NATS publisher (optional).
	var pub handler.EventPublisher
	if !*noPublish && cfg.NatsURL != "" {
		js, nc, err := hermesnats.NewJetStream(cfg.NatsURL)
		if err != nil {
			log.Warn().Err(err).Msg("NATS connect failed; lifecycle events will be skipped")
		} else {
			defer nc.Close()
			pub = handler.NewNatsEventPublisher(js, log)
			log.Info().Msg("NATS connected; lifecycle events enabled")
		}
	} else if *noPublish {
		log.Info().Msg("--no-publish set; skipping NATS")
	} else {
		log.Info().Msg("NATS_URL unset; skipping lifecycle events")
	}

	// ── Run import.
	stats, err := importer.Run(ctx, importer.Options{
		SessionsDir: *sessionsDir,
		TenantID:    *tenantID,
		Store:       st,
		DEK:         dek,
		Publisher:   pub,
		Logger:      log,
		DryRun:      *dryRun,
		Force:       *force,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Warn().Msg("import canceled")
			// Partial result possible; treat as partial if any
			// sessions did persist before cancel.
			if stats != nil && stats.Imported > 0 {
				return exitPartial
			}
			return exitAbort
		}
		log.Error().Err(err).Msg("import failed to start")
		return exitAbort
	}

	log.Info().
		Int("total", stats.Total).
		Int("imported", stats.Imported).
		Int("forced", stats.Forced).
		Int("skipped", stats.Skipped).
		Int("failed", stats.Failed).
		Bool("dry_run", stats.DryRun).
		Msg("import complete")

	if stats.Failed > 0 {
		return exitPartial
	}
	return exitOK
}

// loadDEK mirrors cmd/mbs/main.go::loadDEK so the importer pulls
// from the exact same DEK source the live service does. Drift here
// produces silently-undecryptable rows — keep these two implementations
// in lockstep. Refactor into a shared helper only if a third cmd needs
// it (see plan §C8.4 pin).
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
