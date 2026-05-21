// Notification service config — same env-var conventions as the rest
// of the platform.

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

	// Shared secret that internal callers (savings, member, etc.) send
	// in X-Internal-Token when posting to /internal/v1/notify.
	InternalToken string

	// Filesystem root for generated PDFs. Never publicly served.
	PDFStorageDir string

	ReadHeaderTimeout time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:               getEnv("NOTIFICATION_ENV", "development"),
		HTTPAddr:          getEnv("NOTIFICATION_HTTP_ADDR", ":8085"),
		LogLevel:          getEnv("NOTIFICATION_LOG_LEVEL", "info"),
		DatabaseURL:       must("DATABASE_URL"),
		AppDomain:         getEnv("APP_DOMAIN", "nexussacco.local"),
		JWTSecret:         []byte(must("JWT_SECRET")),
		JWTIssuer:         getEnv("JWT_ISSUER", "nexus-identity"),
		InternalToken:     getEnv("NOTIFICATION_INTERNAL_TOKEN", ""),
		PDFStorageDir:     getEnv("NOTIFICATION_PDF_DIR", "/tmp/sacco-pdfs"),
		ReadHeaderTimeout: 5 * time.Second,
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
