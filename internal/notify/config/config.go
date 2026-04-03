package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port        int
	DatabaseURL string
	NatsURL     string
}

func Load() (Config, error) {
	cfg := Config{
		Port:    8086,
		NatsURL: "nats://localhost:4222",
	}

	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid PORT: %w", err)
		}
		cfg.Port = p
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
