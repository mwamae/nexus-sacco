// Workflow service config — same env-var conventions as the rest of
// the platform.

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

	JWTSecret []byte
	JWTIssuer string

	// Webhook delivery timeout when calling host modules on terminal state.
	CallbackTimeout time.Duration

	ReadHeaderTimeout time.Duration

	// Notification integration (Stage 8) — central notification service.
	NotificationURL           string
	NotificationInternalToken string
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:                       getEnv("WORKFLOW_ENV", "development"),
		HTTPAddr:                  getEnv("WORKFLOW_HTTP_ADDR", ":8083"),
		LogLevel:                  getEnv("WORKFLOW_LOG_LEVEL", "info"),
		DatabaseURL:               must("DATABASE_URL"),
		AppDomain:                 getEnv("APP_DOMAIN", "nexussacco.local"),
		JWTSecret:                 []byte(must("JWT_SECRET")),
		JWTIssuer:                 getEnv("JWT_ISSUER", "nexus-identity"),
		CallbackTimeout:           10 * time.Second,
		ReadHeaderTimeout:         5 * time.Second,
		NotificationURL:           getEnv("NOTIFICATION_SERVICE_URL", "http://localhost:8085"),
		NotificationInternalToken: getEnv("NOTIFICATION_INTERNAL_TOKEN", ""),
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
