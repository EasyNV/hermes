package config

import (
	pkgconfig "github.com/hermes-waba/hermes/pkg/config"
)

type Config struct {
	Port             int
	MetricsPort      int
	DatabaseURL      string
	NatsURL          string
	PodID            string
	ProxyServiceAddr string
}

func Load() Config {
	return Config{
		Port:             pkgconfig.GetEnvInt("PORT", 9104),
		MetricsPort:      pkgconfig.GetEnvInt("METRICS_PORT", 9114),
		DatabaseURL:      pkgconfig.GetEnv("DATABASE_URL", "postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable"),
		NatsURL:          pkgconfig.GetEnv("NATS_URL", "nats://localhost:4222"),
		PodID:            pkgconfig.GetEnv("POD_ID", "hermes-wa-0"),
		ProxyServiceAddr: pkgconfig.GetEnv("PROXY_SERVICE_ADDR", "localhost:9101"),
	}
}
