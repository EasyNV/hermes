package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port        int
	MetricsPort int
	DatabaseURL string
	NatsURL     string
}

func Load() (Config, error) {
	cfg := Config{
		Port:        8086,
		MetricsPort: 9113, // notify gRPC (9103) + 10
		NatsURL:     "nats://localhost:4222",
	}

	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid PORT: %w", err)
		}
		cfg.Port = p
	}

	// METRICS_PORT (Stage F chunk 4): diagnostic HTTP port for
	// /livez+/readyz+/metrics.
	if v := os.Getenv("METRICS_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid METRICS_PORT: %w", err)
		}
		cfg.MetricsPort = p
	}

	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("DATABASE_URL is required")
	}

	if v := os.Getenv("NATS_URL"); v != "" {
		cfg.NatsURL = v
	}

	return cfg, nil
}
