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

	// X-Internal-Token gates the /v1/mpesa/b2c/requests enqueue
	// endpoint. Required when MPESA_ENV=production. The enqueue
	// caller (savings disburse path today; future loan/refund
	// flows) sends this header from MPESA_INTERNAL_TOKEN.
	InternalToken string

	// Base URL for the savings service. mpesa's B2C result handler
	// posts here to finalize a loan disbursement once Daraja
	// confirms. Empty disables the auto-finalize path (the
	// reconciler still picks up the row).
	SavingsBaseURL string

	// PEM-encoded initiator certificate (or PKIX public key). The
	// b2c-dispatcher uses this to RSA-OAEP-encrypt the initiator
	// password. Required for any B2C call — the dispatcher errors
	// at startup when empty AND MPESA_ENV=production. In sandbox
	// the dispatcher logs a warning + keeps queued rows pending.
	InitiatorCertPEM []byte

	// Public callback URLs Safaricom hits with B2C result/timeout.
	// Each paybill submits these as part of its B2C request.
	B2CResultURL  string
	B2CTimeoutURL string

	// STKCallbackBase — public origin (scheme+host) we hand to Daraja
	// as the CallBackURL on STK Push. The path is constructed per
	// request: {base}/v1/c2b/{paybill_id}/stk-callback?token=…
	STKCallbackBase string

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
		InternalToken:     getEnv("MPESA_INTERNAL_TOKEN", ""),
		SavingsBaseURL:    getEnv("MPESA_SAVINGS_URL", ""),
		InitiatorCertPEM:  []byte(getEnv("MPESA_INITIATOR_CERT_PEM", "")),
		B2CResultURL:      getEnv("MPESA_B2C_RESULT_URL", ""),
		B2CTimeoutURL:     getEnv("MPESA_B2C_TIMEOUT_URL", ""),
		STKCallbackBase:   getEnv("MPESA_STK_CALLBACK_BASE", ""),
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
	// Phase-4 production-mode checks. Sandbox is lenient — the
	// B2C dispatcher just skips signing rows when the cert is
	// missing, and the enqueue endpoint stays unauth'd until an
	// operator sets MPESA_INTERNAL_TOKEN.
	if cfg.Env == "production" {
		if cfg.InternalToken == "" {
			return nil, errors.New("MPESA_INTERNAL_TOKEN must be set in production")
		}
		if len(cfg.InitiatorCertPEM) == 0 {
			return nil, errors.New("MPESA_INITIATOR_CERT_PEM must be set in production for B2C signing")
		}
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
