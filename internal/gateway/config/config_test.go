package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultsAreApplied(t *testing.T) {
	// Make sure neither prod nor dev posture sneaks in via the test env.
	for _, k := range []string{
		"JWT_SECRET", "JWT_SECRET_FILE",
		"PORT", "DATABASE_URL", "NATS_URL",
		"WA_ADDR", "CAMPAIGN_ADDR", "INBOX_ADDR", "CONTACTS_ADDR",
		"PROXY_ADDR", "NOTIFY_ADDR", "MBS_ADDR",
	} {
		os.Unsetenv(k)
	}
	cfg := Load()
	if cfg.JWTSecret != "hermes-dev-jwt-secret-change-in-prod" {
		t.Fatalf("expected dev default for JWTSecret; got %q", cfg.JWTSecret)
	}
	if cfg.Port != 8080 {
		t.Fatalf("expected default port 8080; got %d", cfg.Port)
	}
	if cfg.MbsAddr != "localhost:8082" {
		t.Fatalf("expected default mbs addr; got %q", cfg.MbsAddr)
	}
}

func TestLoad_JWTSecretInlineWinsOverFile(t *testing.T) {
	t.Setenv("JWT_SECRET", "inline-secret")
	t.Setenv("JWT_SECRET_FILE", "/nonexistent")
	cfg := Load()
	if cfg.JWTSecret != "inline-secret" {
		t.Fatalf("expected inline JWT_SECRET to win; got %q", cfg.JWTSecret)
	}
}

func TestLoad_JWTSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt-key")
	const want = "0123456789abcdef-supersecret"
	if err := os.WriteFile(path, []byte(want+"\n"), 0o400); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_SECRET_FILE", path)
	cfg := Load()
	if cfg.JWTSecret != want {
		t.Fatalf("expected file-loaded secret %q; got %q", want, cfg.JWTSecret)
	}
}

func TestLoad_JWTSecretFileMissingFallsBackToDevDefault(t *testing.T) {
	// If the operator points JWT_SECRET_FILE at a non-existent file and
	// leaves JWT_SECRET empty, Load returns the dev default (which the
	// gateway boot logic can then treat as the dev fallback).
	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_SECRET_FILE", "/path/that/does/not/exist")
	cfg := Load()
	if cfg.JWTSecret != "hermes-dev-jwt-secret-change-in-prod" {
		t.Fatalf("expected dev fallback on missing file; got %q", cfg.JWTSecret)
	}
}
