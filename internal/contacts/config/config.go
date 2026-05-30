package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds environment-based configuration for hermes-contacts.
type Config struct {
	Port        int
	MetricsPort int
	DatabaseURL string
	NatsURL     string
}

// Load reads configuration from environment variables.
func Load() (Config, error) {
	port := 8084
	if p := os.Getenv("PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return Config{}, fmt.Errorf("invalid PORT: %w", err)
		}
		port = v
	}

	// METRICS_PORT (Stage F chunk 4): diagnostic HTTP port for
	// /livez+/readyz+/metrics. Default 9112 = contacts gRPC (9102) + 10.
	metricsPort := 9112
	if p := os.Getenv("METRICS_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			return Config{}, fmt.Errorf("invalid METRICS_PORT: %w", err)
		}
		metricsPort = v
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	return Config{
		Port:        port,
		MetricsPort: metricsPort,
		DatabaseURL: dbURL,
		NatsURL:     natsURL,
	}, nil
}
