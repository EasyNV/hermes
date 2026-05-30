package config

import (
	pkgconfig "github.com/hermes-waba/hermes/pkg/config"
)

type Config struct {
	Port             int
	MetricsPort      int
	DatabaseURL      string
	NatsURL          string
	BanFlagThreshold int
}

func Load() Config {
	return Config{
		Port:             pkgconfig.GetEnvInt("PORT", 8086),
		MetricsPort:      pkgconfig.GetEnvInt("METRICS_PORT", 9111),
		DatabaseURL:      pkgconfig.GetEnv("DATABASE_URL", "postgres://hermes:hermes_dev@localhost:5433/hermes?sslmode=disable"),
		NatsURL:          pkgconfig.GetEnv("NATS_URL", "nats://localhost:4222"),
		BanFlagThreshold: pkgconfig.GetEnvInt("BAN_FLAG_THRESHOLD", 3),
	}
}
