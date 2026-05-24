// Accounting service config — same env-var conventions as the other
// platform services.

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

	// Shared secret that internal callers (savings, member, …) send in
	// X-Internal-Token when calling /internal/v1/post — needed once
	// auto-posting integration lands. Foundation phase ships the
	// scaffolding for it without requiring callers yet.
	InternalToken string

	// PR #6 — workflow integration for the year-end close gate.
	// WorkflowURL is the workflow service base (used to POST a new
	// instance); AccountingSelfURL is what we register as the
	// callback_url on the wf_instance so the engine can call back
	// here; WorkflowInternalToken is the shared secret the engine
	// includes in its callback header.
	WorkflowURL           string
	AccountingSelfURL     string
	WorkflowInternalToken string

	ReadHeaderTimeout time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:               getEnv("ACCOUNTING_ENV", "development"),
		HTTPAddr:          getEnv("ACCOUNTING_HTTP_ADDR", ":8086"),
		LogLevel:          getEnv("ACCOUNTING_LOG_LEVEL", "info"),
		DatabaseURL:       must("DATABASE_URL"),
		AppDomain:         getEnv("APP_DOMAIN", "nexussacco.local"),
		JWTSecret:         []byte(must("JWT_SECRET")),
		JWTIssuer:         getEnv("JWT_ISSUER", "nexus-identity"),
		InternalToken:         getEnv("ACCOUNTING_INTERNAL_TOKEN", ""),
		WorkflowURL:           getEnv("WORKFLOW_SERVICE_URL", "http://localhost:8083"),
		AccountingSelfURL:     getEnv("ACCOUNTING_SELF_URL", "http://localhost:8086"),
		WorkflowInternalToken: getEnv("WORKFLOW_INTERNAL_TOKEN", ""),
		ReadHeaderTimeout:     5 * time.Second,
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
