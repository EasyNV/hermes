package config

import (
	"os"
	"strconv"
)

// GetEnv returns the value of an environment variable, or a default if unset/empty.
func GetEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// GetEnvInt returns an environment variable parsed as int, or a default on failure.
func GetEnvInt(key string, defaultValue int) int {
	s := os.Getenv(key)
	if s == "" {
		return defaultValue
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultValue
	}
	return v
}
