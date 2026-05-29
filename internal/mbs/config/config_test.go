package config

import (
	"testing"
)

// TestLoad_DefaultsDeterministic pins the documented defaults so that
// future drift surfaces in CI. Each case sets nothing (clean env via
// t.Setenv with empty string per key) and asserts the documented
// default. We don't iterate every field — just the chunk-6-relevant
// surface so the table stays readable.
func TestLoad_DefaultsDeterministic(t *testing.T) {
	// Clear every env var we read in Load(). t.Setenv automatically
	// restores on test exit and works with parallel tests run later.
	for _, k := range []string{
		"PORT", "METRICS_PORT", "POD_ID",
		"DATABASE_URL", "DB_SSLMODE", "DB_SSLROOTCERT",
		"DB_MAX_CONNS", "DB_CONN_MAX_LIFETIME",
		"NATS_URL", "NATS_CREDS_FILE",
		"HERMES_MBS_DEK_FILE", "HERMES_MBS_DEK_HEX",
		"MBS_REFRESH_INTERVAL", "MBS_REFRESH_THRESHOLD", "MBS_REFRESH_CONCURRENCY",
		"MBS_BRIDGE_TIMEOUT", "MBS_BRIDGE_2FA_TIMEOUT", "MBS_BRIDGE_MAX_CONCURRENT",
		"HERMES_MBS_DISABLE_TLS",
		"MBS_IMPORT_LEGACY_ON_STARTUP", "MBS_IMPORT_LEGACY_DIR", "MBS_IMPORT_LEGACY_TENANT_ID",
		"MBS_ENCRYPT_REWRITE_ON_STARTUP",
		"MBS_SHUTDOWN_DRAIN_TIMEOUT", "MBS_STREAM_REPLICAS",
		"GRPC_MAX_CONCURRENT_STREAMS", "GRPC_KEEPALIVE_TIME", "GRPC_KEEPALIVE_TIMEOUT",
		"MBS_ENABLE_PPROF",
	} {
		t.Setenv(k, "")
	}

	cfg := Load()
	if cfg.Port != 8082 {
		t.Errorf("Port default: got %d want 8082", cfg.Port)
	}
	if cfg.MetricsPort != 9092 {
		t.Errorf("MetricsPort default: got %d want 9092", cfg.MetricsPort)
	}
	if cfg.PodID != "hermes-mbs" {
		t.Errorf("PodID default: got %q want hermes-mbs", cfg.PodID)
	}
	if cfg.BridgeMaxConcurrent != 10 {
		t.Errorf("BridgeMaxConcurrent default: got %d want 10", cfg.BridgeMaxConcurrent)
	}
	if cfg.MautrixDisableTLS != false {
		t.Errorf("MautrixDisableTLS default: got %v want false (fail-closed)", cfg.MautrixDisableTLS)
	}
	if cfg.StreamReplicas != 1 {
		t.Errorf("StreamReplicas default: got %d want 1 (compose)", cfg.StreamReplicas)
	}
	if cfg.ShutdownDrainTimeout.Seconds() != 30 {
		t.Errorf("ShutdownDrainTimeout default: got %v want 30s", cfg.ShutdownDrainTimeout)
	}
}

// TestLoad_MautrixDisableTLS_AcceptsTrueAndFalse pins the env-bool
// parsing path so a typo ("yes", "Y", etc.) doesn't silently fall
// back to the safe default without a visible test signal.
func TestLoad_MautrixDisableTLS_AcceptsTrueAndFalse(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"", false},        // unset
		{"garbage", false}, // invalid → default
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("HERMES_MBS_DISABLE_TLS", c.env)
			got := Load().MautrixDisableTLS
			if got != c.want {
				t.Errorf("env=%q: got %v want %v", c.env, got, c.want)
			}
		})
	}
}
