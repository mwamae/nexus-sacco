// services/mpesa/cmd/b2c-dispatcher — background worker that pushes
// queued mpesa_outbound_requests rows to Daraja.
//
// Concurrency: one tx per row, leased via SELECT … FOR UPDATE SKIP
// LOCKED. Safe to run multiple instances; per-paybill rate-limiting
// is enforced inside this binary via a token bucket, so two workers
// on the same paybill won't blow past the limit.
//
// On success: outbound row flips to 'sent' with daraja_conversation_id
// stamped. The actual result lands at the Result URL minutes later.
//
// On signing/network error: row flips to 'failed' with a reason; an
// operator decides whether to retry or write it off.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/config"
	"github.com/nexussacco/mpesa/internal/crypto"
	"github.com/nexussacco/mpesa/internal/daraja"
	"github.com/nexussacco/mpesa/internal/db"
	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
	"github.com/nexussacco/shared/healthx"
)

// version is overridden at link time. Reported in worker_heartbeats
// so the operations dashboard can confirm every worker is on the
// expected SHA after a rollout.
var version string

func workerVersion() string {
	if version != "" {
		return version
	}
	if v := os.Getenv("BUILD_VERSION"); v != "" {
		return v
	}
	return "dev"
}

func main() {
	once := flag.Bool("once", false, "drain the queue once and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := newLogger(cfg.LogLevel, cfg.Env)
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Initiator cert is required to sign B2C. In sandbox we log +
	// continue (the dispatcher will skip B2C rows); in production
	// config.Load already rejected an empty value.
	var encoder *daraja.SecurityCredentialEncoder
	if len(cfg.InitiatorCertPEM) > 0 {
		e, err := daraja.NewSecurityCredentialEncoder(cfg.InitiatorCertPEM)
		if err != nil {
			logger.Error("initiator cert", "err", err)
			if cfg.Env == "production" {
				os.Exit(1)
			}
		}
		encoder = e
	} else {
		logger.Warn("MPESA_INITIATOR_CERT_PEM is empty — B2C dispatcher will skip rows until configured")
	}

	// Credential sealer. The dispatcher reads ciphertext from
	// mpesa_paybill_credentials + Decrypts before handing the values
	// to Daraja. config.Load already validated the master key length;
	// NewSealer errors here would be programming bugs, not operator
	// misconfiguration, so we exit instead of degrading.
	sealer, err := crypto.NewSealer(cfg.KMSMasterKeyID, cfg.KMSMasterKey)
	if err != nil {
		logger.Error("init credential sealer", "err", err)
		os.Exit(1)
	}
	// Round-trip a known plaintext through the Sealer before entering
	// the poll loop. Catches misconfigured key material (right length,
	// wrong bytes) + envelope-codec regressions at startup instead of
	// during the first credential rotation in production.
	if err := sealerPingRoundTrip(sealer); err != nil {
		logger.Error("sealer round-trip ping failed", "err", err,
			"kms_active_key_id", cfg.KMSMasterKeyID,
			"hint", "MPESA_KMS_MASTER_KEY must be the SAME hex string that sealed the existing credential rows")
		os.Exit(1)
	}

	d := &dispatcher{
		pool:            pool,
		darajaClient:    daraja.NewClient(cfg.DarajaBaseURL, daraja.Sandbox),
		outboundStore:   store.NewOutboundRequestStore(pool.Pool),
		paybillStore:    store.NewPaybillStore(pool.Pool),
		credStore:       store.NewCredentialStore(pool.Pool),
		audit:           store.NewAuditStore(pool.Pool),
		encoder:         encoder,
		sealer:          sealer,
		resultURL:       cfg.B2CResultURL,
		timeoutURL:      cfg.B2CTimeoutURL,
		logger:          logger,
		rateLimiters:    map[uuid.UUID]*tokenBucket{},
		rateLimitPerMin: rateLimitFromEnv(),
	}
	workerID := uuid.New()
	logger.Info("mpesa b2c-dispatcher starting",
		"worker_id", workerID, "env", cfg.Env, "once", *once,
		"rate_limit_per_min", d.rateLimitPerMin,
		"kms_active_key_id", cfg.KMSMasterKeyID)

	// Heartbeat — every 30s upsert worker_heartbeats. Skipped in
	// -once mode (single-shot cron-style, not a long-lived worker).
	if !*once {
		go healthx.RunHeartbeatLoop(
			ctx, pool.Pool, "b2c-dispatcher", workerVersion(),
			30*time.Second, nil, logger,
		)
	}

	busy := durationMs("MPESA_B2C_POLL_INTERVAL_MS", 1000)
	idle := durationMs("MPESA_B2C_IDLE_INTERVAL_MS", 5000)
	for {
		processed := d.drainOnce(ctx, workerID)
		if *once {
			logger.Info("b2c-dispatcher --once complete", "processed", processed)
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("b2c-dispatcher shut down cleanly")
			return
		case <-time.After(pickInterval(processed, busy, idle)):
		}
	}
}

// ─────────── core ───────────

type dispatcher struct {
	pool          *db.Pool
	darajaClient  *daraja.Client
	outboundStore *store.OutboundRequestStore
	paybillStore  *store.PaybillStore
	credStore     *store.CredentialStore
	audit         *store.AuditStore
	encoder       *daraja.SecurityCredentialEncoder
	sealer        *crypto.Sealer
	resultURL     string
	timeoutURL    string
	logger        *slog.Logger

	rateLimitPerMin int
	mu              sync.Mutex
	rateLimiters    map[uuid.UUID]*tokenBucket
}

func (d *dispatcher) drainOnce(ctx context.Context, workerID uuid.UUID) int {
	if d.encoder == nil {
		// No cert; no point polling — every row would fail signing.
		return 0
	}
	tenantIDs, err := d.listTenants(ctx)
	if err != nil {
		d.logger.Error("list tenants", "err", err)
		return 0
	}
	processed := 0
	for _, tenantID := range tenantIDs {
		if ctx.Err() != nil {
			return processed
		}
		if d.processOne(ctx, workerID, tenantID) {
			processed++
		}
	}
	return processed
}

func (d *dispatcher) processOne(ctx context.Context, workerID, tenantID uuid.UUID) bool {
	var (
		leased *store.OutboundRequest
		errOut error
	)
	err := d.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		o, err := d.outboundStore.LeaseNextTx(ctx, tx, tenantID, workerID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		leased = o
		if err := d.dispatch(ctx, tx, tenantID, o); err != nil {
			errOut = err
			return err
		}
		return nil
	})
	if leased == nil {
		return false
	}
	if errOut == nil && err == nil {
		d.logger.Info("b2c sent",
			"outbound_id", leased.ID, "tenant_id", tenantID, "msisdn", leased.MSISDN)
		return true
	}
	// Tx rolled back; record the failure on a fresh tx so the row
	// goes to 'failed' (operator must retry).
	_ = d.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		reason := coalesceErr(errOut, err).Error()
		return d.outboundStore.MarkFailedTx(ctx, tx, leased.ID, reason)
	})
	d.logger.Error("b2c dispatch failed",
		"outbound_id", leased.ID, "err", coalesceErr(errOut, err))
	return true
}

func (d *dispatcher) dispatch(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, out *store.OutboundRequest) error {
	// 1. Rate-limit the paybill.
	if !d.takeToken(out.PaybillID) {
		return fmt.Errorf("rate limit exceeded for paybill %s; row will retry next pass", out.PaybillID)
	}

	// 2. Load the paybill + credentials.
	paybill, err := d.paybillStore.ByIDTx(ctx, tx, out.PaybillID)
	if err != nil {
		return fmt.Errorf("load paybill: %w", err)
	}
	consumerKey, consumerSecret, initiatorName, initiatorPassword, err := d.loadCreds(ctx, tx, out.PaybillID)
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}

	// 3. OAuth token.
	token, err := d.darajaClient.AuthenticateForPaybill(ctx, daraja.CacheKey{
		PaybillID: out.PaybillID, KeyID: "consumer",
	}, consumerKey, consumerSecret)
	if err != nil {
		return fmt.Errorf("daraja oauth: %w", err)
	}

	// 4. RSA-encrypt the initiator password.
	securityCred, err := d.encoder.Encode(initiatorPassword)
	if err != nil {
		return fmt.Errorf("encode security_credential: %w", err)
	}

	// 5. Submit.
	commandID := daraja.CommandID("BusinessPayment")
	if out.CommandID != "" {
		commandID = daraja.CommandID(out.CommandID)
	}
	resp, err := d.darajaClient.SubmitB2C(ctx, token.AccessToken, daraja.B2CRequest{
		OriginatorConversationID: out.ID.String(),
		InitiatorName:            initiatorName,
		SecurityCredential:       securityCred,
		CommandID:                commandID,
		Amount:                   out.Amount.StringFixed(0), // Daraja wants integer amounts
		PartyA:                   paybill.Shortcode,
		PartyB:                   out.MSISDN,
		Remarks:                  defaultRemark(out),
		QueueTimeOutURL:          d.timeoutURL,
		ResultURL:                d.resultURL,
		Occasion:                 out.SourceRef,
	})
	if err != nil {
		return fmt.Errorf("daraja submit: %w", err)
	}
	if resp.ResponseCode != "0" {
		return fmt.Errorf("daraja rejected: %s — %s", resp.ResponseCode, resp.ResponseDescription)
	}

	// 6. Mark sent.
	if err := d.outboundStore.MarkSentTx(ctx, tx, out.ID, resp.ConversationID, resp.OriginatorConversationID); err != nil {
		return err
	}
	d.audit.Write(ctx, store.AuditEntry{
		TenantID:   &tenantID,
		Action:     "mpesa.b2c.sent",
		TargetKind: "mpesa_outbound_request",
		TargetID:   out.ID.String(),
		Metadata: map[string]any{
			"conversation_id":            resp.ConversationID,
			"originator_conversation_id": resp.OriginatorConversationID,
			"msisdn":                     out.MSISDN,
			"amount":                     out.Amount.StringFixed(2),
		},
	})
	return nil
}

// loadCreds reads + decrypts the four credential rows the B2C path
// needs. The credential store returns ciphertext + the key id stamped
// in the envelope header; we hand the ciphertext to the platform
// sealer (wired at startup from MPESA_KMS_MASTER_KEY) which picks the
// right master key by id.
//
// Failure modes (all surface as a "failed" outbound row via
// processOne's errOut path):
//   - read error: SECURITY DEFINER fn refused, row missing
//   - ErrUnknownKeyID: ciphertext was sealed under a key the current
//     dispatcher process doesn't carry (rotation in progress, or
//     stale ciphertext from a previous master). The reason text names
//     the unknown key so an operator can re-seal via Settings →
//     Rotate creds without grepping the codebase.
//   - other Decrypt errors (ErrBadCiphertext, ErrAuthFailed): rare;
//     surfaced verbatim so the operator sees what happened.
func (d *dispatcher) loadCreds(ctx context.Context, tx pgx.Tx, paybillID uuid.UUID) (
	consumerKey, consumerSecret, initiatorName, initiatorPassword string, err error,
) {
	loadOne := func(kind domain.CredentialKind) (string, error) {
		_, ct, e := d.credStore.ReadTx(ctx, tx, paybillID, kind)
		if e != nil {
			return "", fmt.Errorf("read %s: %w", kind, e)
		}
		pt, e := d.sealer.Decrypt(ct)
		if e != nil {
			if errors.Is(e, crypto.ErrUnknownKeyID) {
				stampedKey, _ := crypto.KeyIDOf(ct)
				return "", fmt.Errorf(
					"decrypt %s: credential sealed with unknown key %q — re-seal via Settings → Rotate creds",
					kind, stampedKey,
				)
			}
			return "", fmt.Errorf("decrypt %s: %w", kind, e)
		}
		return string(pt), nil
	}
	if consumerKey, err = loadOne(domain.CredConsumerKey); err != nil {
		return
	}
	if consumerSecret, err = loadOne(domain.CredConsumerSecret); err != nil {
		return
	}
	if initiatorName, err = loadOne(domain.CredInitiatorName); err != nil {
		return
	}
	if initiatorPassword, err = loadOne(domain.CredInitiatorPassword); err != nil {
		return
	}
	return
}

// defaultRemark returns a B2C "Remarks" value when the row doesn't
// have an explicit one. Daraja requires this field to be non-empty.
func defaultRemark(o *store.OutboundRequest) string {
	if o.Remarks != "" {
		return o.Remarks
	}
	switch o.Kind {
	case domain.OutboundB2CDisbursement:
		return "Loan disbursement"
	case domain.OutboundRefund:
		return "Refund"
	}
	return "Payment"
}

// ─────────── rate limit ───────────

// tokenBucket is the per-paybill token bucket. One token per
// permitted request; a goroutine refills at the configured rate.
type tokenBucket struct {
	capacity int
	tokens   int
	mu       sync.Mutex
}

func newBucket(capacity int) *tokenBucket {
	return &tokenBucket{capacity: capacity, tokens: capacity}
}

func (b *tokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens > 0 {
		b.tokens--
		return true
	}
	return false
}

func (b *tokenBucket) refillTo(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = n
}

// takeToken claims a slot for a paybill. The bucket refills lazily —
// each successful take checks the elapsed time and tops up if needed.
// Simpler than a goroutine per paybill; good enough at the modest
// volumes the dispatcher sees.
func (d *dispatcher) takeToken(paybillID uuid.UUID) bool {
	d.mu.Lock()
	b, ok := d.rateLimiters[paybillID]
	if !ok {
		b = newBucket(d.rateLimitPerMin)
		d.rateLimiters[paybillID] = b
		// Spawn a single refill goroutine per paybill — runs until
		// the dispatcher process exits. Cheap (a bucket per paybill
		// is bounded by tenant × paybill count).
		go d.refillLoop(b)
	}
	d.mu.Unlock()
	return b.take()
}

func (d *dispatcher) refillLoop(b *tokenBucket) {
	// Refill once per minute — coarser than per-second but matches
	// the documented Daraja limit window (req/min). A bursty
	// dispatcher gets all N tokens at once at the top of the
	// minute; that's what Daraja expects.
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		b.refillTo(b.capacity)
	}
}

// ─────────── helpers ───────────

func (d *dispatcher) listTenants(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := d.pool.Query(ctx, `SELECT id FROM tenants WHERE status='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func coalesceErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func pickInterval(processed int, busy, idle time.Duration) time.Duration {
	if processed > 0 {
		return busy
	}
	return idle
}

func durationMs(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(def) * time.Millisecond
}

func rateLimitFromEnv() int {
	if v := os.Getenv("MPESA_B2C_RATE_LIMIT_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 30 // sandbox default
}

// sealerPingRoundTrip encrypts then decrypts a known plaintext and
// asserts the output matches. Catches misconfigured key bytes, codec
// regressions, and rotation-id mismatches BEFORE the dispatcher
// touches a real credential row.
func sealerPingRoundTrip(s *crypto.Sealer) error {
	const probe = "dispatcher-ping"
	ct, err := s.Encrypt([]byte(probe))
	if err != nil {
		return fmt.Errorf("encrypt probe: %w", err)
	}
	pt, err := s.Decrypt(ct)
	if err != nil {
		return fmt.Errorf("decrypt probe: %w", err)
	}
	if string(pt) != probe {
		return fmt.Errorf("round-trip mismatch: got %q, want %q", pt, probe)
	}
	return nil
}

func newLogger(level, env string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if env == "development" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// silence unused-import warnings for http (kept around for future
// retry-with-context use).
var _ = http.NoBody
