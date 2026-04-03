package logger

import (
	"os"

	"github.com/rs/zerolog"
)

// New creates a zerolog.Logger configured for the given service name.
// Log level is controlled by the LOG_LEVEL environment variable (default: info).
func New(service string) zerolog.Logger {
	level := zerolog.InfoLevel
	if l := os.Getenv("LOG_LEVEL"); l != "" {
		if parsed, err := zerolog.ParseLevel(l); err == nil {
			level = parsed
		}
	}

	return zerolog.New(os.Stdout).
		Level(level).
		With().
		Timestamp().
		Str("service", service).
		Logger()
}
