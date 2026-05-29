// Package config loads hermes-mbs configuration from environment variables.
// Matches the convention used by internal/wa/config — bare struct + Load()
// using helpers from pkg/config. No envconfig library; consistent with the
// rest of the monorepo.
package config

import (
	"os"
	"strconv"
	"time"

	pkgconfig "github.com/hermes-waba/hermes/pkg/config"
)

// Config is the full set of tunables for hermes-mbs. Defaults are
// dev/compose-friendly; prod overrides come from env or K8s ConfigMap+Secret.
type Config struct {
	// ─── Server ────────────────────────────────────────────────────────
	Port        int
	MetricsPort int
	PodID       string

	// ─── Database ──────────────────────────────────────────────────────
	DatabaseURL   string
	DBSSLMode     string        // disable | prefer | require | verify-ca | verify-full
	DBSSLRootCert string        // CA bundle path (managed DB)
	DBMaxConns    int           // 0 = pgx default
	DBConnMaxLife time.Duration // 0 = no recycle (pgx default)

	// ─── NATS ──────────────────────────────────────────────────────────
	NatsURL       string
	NatsCredsFile string

	// ─── DEK ───────────────────────────────────────────────────────────
	// cmd/mbs/main.go probes file first, then env hex; fails closed if neither.
	DEKFile string
	DEKHex  string

	// ─── Cookie refresh cron ───────────────────────────────────────────
	RefreshInterval    time.Duration
	RefreshThreshold   time.Duration
	RefreshConcurrency int

	// ─── Bridge embedded driver ────────────────────────────────────────
	BridgeOverallTimeout time.Duration
	Bridge2FATimeout     time.Duration
	BridgeMaxConcurrent  int // semaphore — OOM blast-radius cap

	// MautrixDisableTLS, when true, disables TLS verification in
	// mautrix-meta's HTTP transport. **Process-wide and unrecoverable
	// until restart** (mautrix's API is a package-level global). Use
	// ONLY for mitmproxy capture in a single-tenant dev pod — never
	// run multi-tenant in this mode. The chunk-5 bridge hostile audit
	// (F1) documents the blast radius. Boot logs a stark WARN when
	// this is true.
	MautrixDisableTLS bool

	// ─── Legacy importer (dev only — prod uses cmd/mbs-import) ─────────
	ImportLegacyOnStartup bool
	ImportLegacyDir       string
	ImportLegacyTenantID  string

	// ─── One-shot encryption rewrite (gate; drop after first deploy) ───
	EncryptRewriteOnStartup bool

	// ─── Graceful shutdown ─────────────────────────────────────────────
	ShutdownDrainTimeout time.Duration

	// ─── NATS stream replicas (1 for compose, 3 for K8s cluster) ───────
	StreamReplicas int

	// ─── gRPC server keepalive ─────────────────────────────────────────
	GRPCMaxConcurrentStreams uint32
	GRPCKeepaliveTime        time.Duration
	GRPCKeepaliveTimeout     time.Duration

	// ─── Debug ─────────────────────────────────────────────────────────
	EnablePprof bool
}

// Load reads env vars into a Config. Missing values get defaults; nothing
// is validated here (validation lives in main.go where the error budget
// is "fail fast at startup").
func Load() Config {
	return Config{
		Port:        pkgconfig.GetEnvInt("PORT", 8082),
		MetricsPort: pkgconfig.GetEnvInt("METRICS_PORT", 9092),
		PodID:       pkgconfig.GetEnv("POD_ID", "hermes-mbs"),

		DatabaseURL:   pkgconfig.GetEnv("DATABASE_URL", ""),
		DBSSLMode:     pkgconfig.GetEnv("DB_SSLMODE", "prefer"),
		DBSSLRootCert: pkgconfig.GetEnv("DB_SSLROOTCERT", ""),
		DBMaxConns:    pkgconfig.GetEnvInt("DB_MAX_CONNS", 20),
		DBConnMaxLife: getEnvDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute),

		NatsURL:       pkgconfig.GetEnv("NATS_URL", "nats://nats:4222"),
		NatsCredsFile: pkgconfig.GetEnv("NATS_CREDS_FILE", ""),

		DEKFile: pkgconfig.GetEnv("HERMES_MBS_DEK_FILE", ""),
		DEKHex:  pkgconfig.GetEnv("HERMES_MBS_DEK_HEX", ""),

		RefreshInterval:    getEnvDuration("MBS_REFRESH_INTERVAL", time.Hour),
		RefreshThreshold:   getEnvDuration("MBS_REFRESH_THRESHOLD", 30*24*time.Hour),
		RefreshConcurrency: pkgconfig.GetEnvInt("MBS_REFRESH_CONCURRENCY", 5),

		BridgeOverallTimeout: getEnvDuration("MBS_BRIDGE_TIMEOUT", 180*time.Second),
		Bridge2FATimeout:     getEnvDuration("MBS_BRIDGE_2FA_TIMEOUT", 120*time.Second),
		BridgeMaxConcurrent:  pkgconfig.GetEnvInt("MBS_BRIDGE_MAX_CONCURRENT", 10),

		MautrixDisableTLS: getEnvBool("HERMES_MBS_DISABLE_TLS", false),

		ImportLegacyOnStartup: getEnvBool("MBS_IMPORT_LEGACY_ON_STARTUP", false),
		ImportLegacyDir:       pkgconfig.GetEnv("MBS_IMPORT_LEGACY_DIR", ""),
		ImportLegacyTenantID:  pkgconfig.GetEnv("MBS_IMPORT_LEGACY_TENANT_ID", ""),

		EncryptRewriteOnStartup: getEnvBool("MBS_ENCRYPT_REWRITE_ON_STARTUP", false),

		ShutdownDrainTimeout: getEnvDuration("MBS_SHUTDOWN_DRAIN_TIMEOUT", 30*time.Second),
		StreamReplicas:       pkgconfig.GetEnvInt("MBS_STREAM_REPLICAS", 1),

		GRPCMaxConcurrentStreams: uint32(pkgconfig.GetEnvInt("GRPC_MAX_CONCURRENT_STREAMS", 1000)),
		GRPCKeepaliveTime:        getEnvDuration("GRPC_KEEPALIVE_TIME", 30*time.Second),
		GRPCKeepaliveTimeout:     getEnvDuration("GRPC_KEEPALIVE_TIMEOUT", 10*time.Second),

		EnablePprof: getEnvBool("MBS_ENABLE_PPROF", false),
	}
}

// getEnvDuration parses a time.Duration env var. If the value is empty
// or invalid, returns the default. If this proves useful elsewhere we
// promote to pkg/config.
func getEnvDuration(key string, def time.Duration) time.Duration {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// getEnvBool parses a bool env var. Accepts the strconv.ParseBool set
// ("true", "TRUE", "1", "0", etc.). Invalid values fall back to default.
func getEnvBool(key string, def bool) bool {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return b
}
