// Mpesa service config — same env-var conventions as the rest of the
// platform. The Daraja-specific knobs are scoped under MPESA_DARAJA_*
// so operators can flip sandbox/production without touching code.

package config

import (
	"encoding/hex"
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

	// Daraja base URL + a kill-switch that pins every paybill to
	// sandbox regardless of its stored environment value. The
	// kill-switch exists so a misconfigured production paybill in a
	// dev environment can't accidentally hit live Safaricom.
	DarajaBaseURL string
	ForceSandbox  bool

	// 32-byte AES key for the credential envelope. Stored as hex in
	// env so it survives copy/paste through dev shells. Phase 6 swaps
	// this for a real KMS-managed key, but the envelope header
	// already carries a key id so rotation is non-disruptive.
	KMSMasterKey   []byte
	KMSMasterKeyID string

	// Comma-separated IPs / CIDRs the webhook handlers will accept
	// callbacks from. Empty in sandbox (logged as a warning at
	// startup); MUST be non-empty when MPESA_ENV=production or the
	// service refuses to start.
	TrustedIPs string

	ReadHeaderTimeout time.Duration
}

func Load() (*Config, error) {
	keyHex := must("MPESA_KMS_MASTER_KEY")
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("MPESA_KMS_MASTER_KEY must be hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("MPESA_KMS_MASTER_KEY must decode to 32 bytes, got %d", len(key))
	}

	cfg := &Config{
		Env:               getEnv("MPESA_ENV", "development"),
		HTTPAddr:          getEnv("MPESA_HTTP_ADDR", ":8087"),
		LogLevel:          getEnv("MPESA_LOG_LEVEL", "info"),
		DatabaseURL:       must("DATABASE_URL"),
		AppDomain:         getEnv("APP_DOMAIN", "nexussacco.local"),
		JWTSecret:         []byte(must("JWT_SECRET")),
		JWTIssuer:         getEnv("JWT_ISSUER", "nexus-identity"),
		DarajaBaseURL:     getEnv("MPESA_DARAJA_BASE_URL", "https://sandbox.safaricom.co.ke"),
		ForceSandbox:      getEnv("MPESA_FORCE_SANDBOX", "true") == "true",
		KMSMasterKey:      key,
		KMSMasterKeyID:    getEnv("MPESA_KMS_MASTER_KEY_ID", "kms-dev-001"),
		TrustedIPs:        getEnv("MPESA_TRUSTED_IPS", ""),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, errors.New("JWT_SECRET must be at least 32 bytes")
	}
	// Production must pin an allow list — there's no plausible
	// reason for a live SACCO webhook to accept callbacks from any
	// IP, and Daraja publishes a small fixed list. Sandbox can run
	// empty (warned about in the middleware).
	if cfg.Env == "production" && cfg.TrustedIPs == "" {
		return nil, errors.New("MPESA_TRUSTED_IPS must be set in production")
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
