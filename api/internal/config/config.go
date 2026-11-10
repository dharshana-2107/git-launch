// Package config reads the API's settings from environment variables.
//
// WHY environment variables: the same binary runs on your laptop, in Docker
// and in CI without code changes; only the environment differs
// (the "12-factor app" rule).
package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port        string // HTTP port the API listens on
	DatabaseURL string // Postgres connection string
	Workers     int    // how many builds may run at the same time
	AppsDomain  string // apps are served at <subdomain>.<AppsDomain>
	AppsNetwork string // Docker network shared by Traefik and the apps
	RoutesDir   string // folder where route files for Traefik are written
}

func Load() Config {
	return Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://launchpad:launchpad@localhost:5432/launchpad?sslmode=disable"),
		Workers:     getenvInt("BUILD_WORKERS", 2),
		AppsDomain:  getenv("APPS_DOMAIN", "localhost"),
		AppsNetwork: getenv("APPS_NETWORK", "launchpad-apps"),
		// Relative to where the API runs (the api/ folder), this is
		// launchpad/data/routes, the folder docker-compose mounts into Traefik.
		RoutesDir: getenv("ROUTES_DIR", "../data/routes"),
	}
}

// getenv returns the variable's value, or fallback when it is unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getenvInt is getenv for whole numbers; a missing or invalid value uses fallback.
func getenvInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return fallback
}
