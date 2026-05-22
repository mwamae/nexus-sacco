// Member service configuration. Sourced from env vars (same .env as identity).

package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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

	// Filesystem root for uploaded documents.
	StorageDir string

	// Max upload size in bytes. Default 5 MiB.
	MaxUploadBytes int64

	// Read header / connection timeouts (apply to the HTTP server).
	ReadHeaderTimeout time.Duration

	// Workflow integration — used for sensitive status changes.
	WorkflowURL         string
	MemberSelfURL       string
	WorkflowProcessKind string
	DefaultDormancyDays int

	// Notification integration (Stage 8) — central notification service.
	NotificationURL           string
	NotificationInternalToken string

	// Accounting integration (Phase 12/D) — used by the activation
	// pipeline to post the registration-fee journal entry.
	AccountingURL           string
	AccountingInternalToken string
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:               getEnv("MEMBER_ENV", "development"),
		HTTPAddr:          getEnv("MEMBER_HTTP_ADDR", ":8082"),
		LogLevel:          getEnv("MEMBER_LOG_LEVEL", "info"),
		DatabaseURL:       must("DATABASE_URL"),
		AppDomain:         getEnv("APP_DOMAIN", "nexussacco.local"),
		JWTSecret:         []byte(must("JWT_SECRET")),
		JWTIssuer:         getEnv("JWT_ISSUER", "nexus-identity"),
		StorageDir:        getEnv("MEMBER_STORAGE_DIR", "./data/member-storage"),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, errors.New("JWT_SECRET must be at least 32 bytes")
	}
	if v := os.Getenv("MEMBER_MAX_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MEMBER_MAX_UPLOAD_BYTES: %w", err)
		}
		cfg.MaxUploadBytes = n
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 5 << 20 // 5 MiB
	}

	cfg.WorkflowURL = getEnv("WORKFLOW_SERVICE_URL", "http://localhost:8083")
	cfg.MemberSelfURL = getEnv("MEMBER_SELF_URL", "http://localhost:8082")
	cfg.WorkflowProcessKind = getEnv("MEMBER_STATUS_WORKFLOW_KIND", "member_status_change")
	if v := os.Getenv("MEMBER_DEFAULT_DORMANCY_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DefaultDormancyDays = n
		}
	}
	if cfg.DefaultDormancyDays <= 0 {
		cfg.DefaultDormancyDays = 365
	}

	cfg.NotificationURL = getEnv("NOTIFICATION_SERVICE_URL", "http://localhost:8085")
	cfg.NotificationInternalToken = getEnv("NOTIFICATION_INTERNAL_TOKEN", "")

	cfg.AccountingURL = getEnv("ACCOUNTING_SERVICE_URL", "http://localhost:8086")
	cfg.AccountingInternalToken = getEnv("ACCOUNTING_INTERNAL_TOKEN", "")
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
