// Package config reads the API's settings from environment variables.
//
// WHY environment variables: the same binary runs on your laptop, in Docker
// and in CI without code changes; only the environment differs
// (the "12-factor app" rule).
package config

import "os"

type Config struct {
	Port        string // HTTP port the API listens on
	DatabaseURL string // Postgres connection string
}

func Load() Config {
	return Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://launchpad:launchpad@localhost:5432/launchpad?sslmode=disable"),
	}
}

// getenv returns the variable's value, or fallback when it is unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
