// Package config loads runtime configuration from the environment.
// No magic: required vars panic at startup, optional vars have defaults.

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

	JWTSecret     []byte
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	JWTIssuer     string

	AppDomain     string // bare apex, e.g. "nexussacco.local"
	CookieDomain  string // e.g. ".nexussacco.local"
	CookieSecure  bool

	// WebBaseURL is a template like "http://{slug}.nexussacco.local:5173"
	// used to build links in transactional emails. {slug} is replaced with
	// the tenant slug (or "platform" for platform-admin links).
	WebBaseURL string

	// PasswordResetTTL is how long a password-reset link is valid.
	PasswordResetTTL time.Duration

	// InviteTTL is how long a staff invite link is valid.
	InviteTTL time.Duration

	// StorageDir is the LocalDisk root for tenant-side uploads (logos, etc).
	StorageDir string
	// MaxUploadBytes caps logo + other tenant uploads. Default 2 MiB.
	MaxUploadBytes int64

	// Bootstrap — used only by the -seed CLI flag.
	PlatformAdminEmail    string
	PlatformAdminPassword string

	// SMTP — when SMTPHost is empty, email features fail closed.
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	SMTPFromName string
	SMTPUseTLS   bool
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:      getEnv("IDENTITY_ENV", "development"),
		HTTPAddr: getEnv("IDENTITY_HTTP_ADDR", ":8081"),
		LogLevel: getEnv("IDENTITY_LOG_LEVEL", "info"),

		DatabaseURL: must("DATABASE_URL"),

		JWTSecret: []byte(must("JWT_SECRET")),
		JWTIssuer: getEnv("JWT_ISSUER", "nexus-identity"),

		AppDomain:    getEnv("APP_DOMAIN", "nexussacco.local"),
		CookieDomain: getEnv("COOKIE_DOMAIN", ""),
		CookieSecure: getBool("COOKIE_SECURE", false),

		WebBaseURL: getEnv("WEB_BASE_URL", ""),

		PlatformAdminEmail:    os.Getenv("PLATFORM_ADMIN_EMAIL"),
		PlatformAdminPassword: os.Getenv("PLATFORM_ADMIN_PASSWORD"),

		SMTPHost:     getEnv("SMTP_HOST", ""),
		SMTPUser:     os.Getenv("SMTP_USER"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:     getEnv("SMTP_FROM", "no-reply@nexussacco.local"),
		SMTPFromName: getEnv("SMTP_FROM_NAME", "nexusSacco"),
		SMTPUseTLS:   getBool("SMTP_USE_TLS", false),
	}

	if p := os.Getenv("SMTP_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			cfg.SMTPPort = n
		}
	}
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 1025
	}

	if len(cfg.JWTSecret) < 32 {
		return nil, errors.New("JWT_SECRET must be at least 32 bytes")
	}

	var err error
	if cfg.JWTAccessTTL, err = parseDuration("JWT_ACCESS_TTL", "15m"); err != nil {
		return nil, err
	}
	if cfg.JWTRefreshTTL, err = parseDuration("JWT_REFRESH_TTL", "720h"); err != nil {
		return nil, err
	}
	if cfg.PasswordResetTTL, err = parseDuration("PASSWORD_RESET_TTL", "30m"); err != nil {
		return nil, err
	}
	if cfg.InviteTTL, err = parseDuration("INVITE_TTL", "168h"); err != nil {
		return nil, err
	}

	cfg.StorageDir = getEnv("IDENTITY_STORAGE_DIR", "./data/identity-storage")
	if v := os.Getenv("IDENTITY_MAX_UPLOAD_BYTES"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("IDENTITY_MAX_UPLOAD_BYTES: %w", perr)
		}
		cfg.MaxUploadBytes = n
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 2 << 20
	}

	// Default WEB_BASE_URL for local dev. Override in any non-dev env.
	if cfg.WebBaseURL == "" {
		cfg.WebBaseURL = "http://{slug}." + cfg.AppDomain + ":5173"
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

func getBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func parseDuration(key, def string) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		v = def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
