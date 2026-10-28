package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port               string
	DatabaseURL        string
	RedisURL           string
	JWTSecret          string
	SeedProducts       int
	SeedCustomers      int
	FulfilmentInterval time.Duration
}

func Load() *Config {
	return &Config{
		Port:               env("PORT", "8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://localhost/optiq?sslmode=disable"),
		RedisURL:           env("REDIS_URL", "redis://localhost:6379"),
		JWTSecret:          env("JWT_SECRET", "change-me-in-production"),
		SeedProducts:       envInt("SEED_PRODUCTS", 1000),
		SeedCustomers:      envInt("SEED_CUSTOMERS", 200),
		FulfilmentInterval: envDuration("FULFILMENT_INTERVAL", 30*time.Second),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}
