package config

import (
	pkgconfig "github.com/hermes-waba/hermes/pkg/config"
)

type Config struct {
	Port        int
	MetricsPort int
	DatabaseURL string
	NatsURL     string
	JWTSecret   string

	// Backend service addresses
	WaAddr       string
	CampaignAddr string
	InboxAddr    string
	ContactsAddr string
	ProxyAddr    string
	NotifyAddr   string
	MbsAddr      string
}

func Load() Config {
	// JWT secret: prefer JWT_SECRET inline (dev posture), fall back to
	// JWT_SECRET_FILE (prod posture — file-based Docker secret at
	// /run/secrets/jwt_signing_key). Empty string is allowed at config-load
	// time; cmd/gateway/main.go decides whether empty is fatal at boot.
	jwtSecret, _ := pkgconfig.LoadSecret("JWT_SECRET", "JWT_SECRET_FILE")
	if jwtSecret == "" {
		// Preserve the pre-existing dev-default behaviour: if nobody set
		// either env, fall back to the dev placeholder. cmd/gateway/main.go
		// already runs in dev posture against this string, and prod compose
		// explicitly sets JWT_SECRET_FILE so this branch is unreachable
		// there.
		jwtSecret = "hermes-dev-jwt-secret-change-in-prod"
	}

	return Config{
		Port:         pkgconfig.GetEnvInt("PORT", 8080),
		MetricsPort:  pkgconfig.GetEnvInt("METRICS_PORT", 9100),
		DatabaseURL:  pkgconfig.GetEnv("DATABASE_URL", "postgres://hermes:***@localhost:5433/hermes?sslmode=disable"),
		NatsURL:      pkgconfig.GetEnv("NATS_URL", "nats://localhost:4222"),
		JWTSecret:    jwtSecret,
		WaAddr:       pkgconfig.GetEnv("WA_ADDR", "localhost:9104"),
		CampaignAddr: pkgconfig.GetEnv("CAMPAIGN_ADDR", "localhost:9105"),
		InboxAddr:    pkgconfig.GetEnv("INBOX_ADDR", "localhost:9106"),
		ContactsAddr: pkgconfig.GetEnv("CONTACTS_ADDR", "localhost:9102"),
		ProxyAddr:    pkgconfig.GetEnv("PROXY_ADDR", "localhost:9101"),
		NotifyAddr:   pkgconfig.GetEnv("NOTIFY_ADDR", "localhost:9103"),
		MbsAddr:      pkgconfig.GetEnv("MBS_ADDR", "localhost:8082"),
	}
}
