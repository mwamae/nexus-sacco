// Savings service configuration. Sourced from env vars (same .env as identity).

package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type Config struct {
	Env      string
	HTTPAddr string
	LogLevel string

	DatabaseURL string
	AppDomain   string

	// JWT verification — must match the secret + issuer used by identity.
	JWTSecret []byte
	JWTIssuer string

	ReadHeaderTimeout time.Duration

	// Workflow integration — used for large-withdrawal / dividend / interest approvals.
	WorkflowURL  string
	SavingsURL   string
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:               getEnv("SAVINGS_ENV", "development"),
		HTTPAddr:          getEnv("SAVINGS_HTTP_ADDR", ":8084"),
		LogLevel:          getEnv("SAVINGS_LOG_LEVEL", "info"),
		DatabaseURL:       must("DATABASE_URL"),
		AppDomain:         getEnv("APP_DOMAIN", "nexussacco.local"),
		JWTSecret:         []byte(must("JWT_SECRET")),
		JWTIssuer:         getEnv("JWT_ISSUER", "nexus-identity"),
		ReadHeaderTimeout: 5 * time.Second,
		WorkflowURL:       getEnv("WORKFLOW_SERVICE_URL", "http://localhost:8083"),
		SavingsURL:        getEnv("SAVINGS_SELF_URL", "http://localhost:8084"),
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, errors.New("JWT_SECRET must be at least 32 bytes")
	}
	return cfg, nil
}

func must(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
