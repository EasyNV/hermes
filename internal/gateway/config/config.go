package config

import (
	pkgconfig "github.com/hermes-waba/hermes/pkg/config"
)

type Config struct {
	Port        int
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
	return Config{
		Port:         pkgconfig.GetEnvInt("PORT", 8080),
		DatabaseURL:  pkgconfig.GetEnv("DATABASE_URL", "postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable"),
		NatsURL:      pkgconfig.GetEnv("NATS_URL", "nats://localhost:4222"),
		JWTSecret:    pkgconfig.GetEnv("JWT_SECRET", "hermes-dev-jwt-secret-change-in-prod"),
		WaAddr:       pkgconfig.GetEnv("WA_ADDR", "localhost:9104"),
		CampaignAddr: pkgconfig.GetEnv("CAMPAIGN_ADDR", "localhost:9105"),
		InboxAddr:    pkgconfig.GetEnv("INBOX_ADDR", "localhost:9106"),
		ContactsAddr: pkgconfig.GetEnv("CONTACTS_ADDR", "localhost:9102"),
		ProxyAddr:    pkgconfig.GetEnv("PROXY_ADDR", "localhost:9101"),
		NotifyAddr:   pkgconfig.GetEnv("NOTIFY_ADDR", "localhost:9103"),
		MbsAddr:      pkgconfig.GetEnv("MBS_ADDR", "localhost:8082"),
	}
}
