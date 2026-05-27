// HTTP client + transactional outbox for the accounting service's
// /internal/v1/post endpoint.
//
// Two entry points:
//
//   • PostTx(ctx, tx, in)  — handlers call this INSIDE their
//                            business tx. Inserts a posting_outbox
//                            row (migration 0032). The row commits
//                            atomically with the business write; a
//                            background dispatcher
//                            (cmd/posting-dispatcher) drains the
//                            outbox by calling Post() below.
//                            Restores the "no transaction is
//                            financially complete without a GL
//                            entry" invariant — a blip on the
//                            accounting service no longer loses
//                            the post.
//
//   • Post(ctx, in)        — the raw HTTP path. Called by the
//                            dispatcher (and a few system-initiated
//                            paths like provisioning batches that
//                            don't have a business tx to attach
//                            to). The accounting service dedups on
//                            (source_module, source_ref) so retries
//                            from the dispatcher are safe.
//
// Dry-run posture (formerly "Disabled"): the previous version
// silently skipped PostTx when BaseURL was empty. That hid the
// bug where a freshly-started dev environment with no accounting
// service appears to work but is quietly losing JEs. The new
// posture:
//
//   • New() returns an error when BaseURL == "" UNLESS the env
//     var SAVINGS_ALLOW_NO_ACCOUNTING=true is set (test-only
//     escape; the integration tests + unit fixtures set it).
//   • PostTx in DryRun mode logs a WARNING per call so every
//     money event is visible in stderr — impossible to overlook
//     during a dev session.
//   • Production / dev paths never set DryRun. The postingcheck
//     analyzer fails CI when any non-_test.go file assigns
//     DryRun=true.

package posting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ErrOutboxInsert wraps any failure of the in-tx outbox INSERT.
// Handlers surface this as 502 + code "gl_post_failed" to the
// caller, and the business write rolls back.
var ErrOutboxInsert = errors.New("posting: outbox insert failed")

// ErrNoAccountingURL is returned by New() when BaseURL is empty
// AND SAVINGS_ALLOW_NO_ACCOUNTING is not "true". The server
// binaries surface this with an actionable error message and
// exit non-zero — failing fast beats a silent dev where every
// money event is dropped.
var ErrNoAccountingURL = errors.New(
	"posting: ACCOUNTING_SERVICE_URL is empty. " +
		"Set it to a reachable accounting service (default: http://localhost:8086) " +
		"or set SAVINGS_ALLOW_NO_ACCOUNTING=true to bypass (tests only).",
)

type Client struct {
	BaseURL       string
	InternalToken string
	HTTP          *http.Client
	Logger        *slog.Logger
	// DryRun — when true, PostTx logs a WARNING per call and skips
	// the outbox insert. Only set in tests via a struct literal
	// (Posting.Client{DryRun: true}) or via the SAVINGS_ALLOW_NO_ACCOUNTING
	// escape; production code MUST NEVER set this. The postingcheck
	// analyzer enforces this — any non-test file that assigns
	// DryRun=true fails CI.
	DryRun bool
}

// New constructs a posting.Client. Returns ErrNoAccountingURL when
// baseURL is empty AND the SAVINGS_ALLOW_NO_ACCOUNTING test escape
// is not set; the binaries' main.go surfaces the error + exits.
//
// When the escape IS set, returns a Client with DryRun=true so the
// per-call WARNING fires and every money event remains visible in
// the logs.
func New(baseURL, internalToken string, logger *slog.Logger) (*Client, error) {
	if baseURL == "" {
		if os.Getenv("SAVINGS_ALLOW_NO_ACCOUNTING") != "true" {
			return nil, ErrNoAccountingURL
		}
		if logger != nil {
			logger.Warn("posting.New: SAVINGS_ALLOW_NO_ACCOUNTING=true — running in dry-run mode; every money event will log a WARNING but no outbox row will be written. NEVER use this outside tests.")
		}
		return &Client{
			InternalToken: internalToken,
			HTTP:          &http.Client{Timeout: 8 * time.Second},
			Logger:        logger,
			DryRun:        true,
		}, nil
	}
	return &Client{
		BaseURL:       baseURL,
		InternalToken: internalToken,
		HTTP:          &http.Client{Timeout: 8 * time.Second},
		Logger:        logger,
	}, nil
}

// Line — single debit OR credit leg. Caller passes the chart-of-
// accounts code (1000, 2000, 4000, etc.); the engine resolves it.
type Line struct {
	AccountCode string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	Narration   string
}

type PostInput struct {
	TenantID     uuid.UUID
	EntryDate    time.Time // optional; defaults to today
	ValueDate    time.Time
	SourceModule string // e.g. "savings.deposits", "savings.loans"
	SourceRef    string // upstream transaction id
	Narration    string
	Lines        []Line
}

// ErrPostingDisabled is returned when the client is configured with
// no base URL (dev mode). Handlers can decide whether to treat this
// as fatal or proceed without posting.
var ErrPostingDisabled = errors.New("posting client disabled (no accounting base URL)")

type lineDTO struct {
	AccountCode string `json:"account_code"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Narration   string `json:"narration,omitempty"`
}

type requestBody struct {
	TenantID     uuid.UUID `json:"tenant_id"`
	EntryDate    string    `json:"entry_date,omitempty"`
	ValueDate    string    `json:"value_date,omitempty"`
	SourceModule string    `json:"source_module"`
	SourceRef    string    `json:"source_ref"`
	Narration    string    `json:"narration"`
	Lines        []lineDTO `json:"lines"`
}

// Post fires the journal entry. Returns nil on success (including
// idempotent replays — the accounting service deduplicates by
// (source_module, source_ref)). Non-nil error means the caller should
// surface a 5xx to its client and roll back the business write.
func (c *Client) Post(ctx context.Context, in PostInput) error {
	if c == nil || c.DryRun {
		return ErrPostingDisabled
	}
	if len(in.Lines) < 2 {
		return errors.New("posting: at least two lines required")
	}

	lines := make([]lineDTO, 0, len(in.Lines))
	for _, ln := range in.Lines {
		l := lineDTO{AccountCode: ln.AccountCode, Narration: ln.Narration}
		if !ln.Debit.IsZero() {
			l.Debit = ln.Debit.StringFixed(2)
		}
		if !ln.Credit.IsZero() {
			l.Credit = ln.Credit.StringFixed(2)
		}
		lines = append(lines, l)
	}

	body := requestBody{
		TenantID:     in.TenantID,
		SourceModule: in.SourceModule,
		SourceRef:    in.SourceRef,
		Narration:    in.Narration,
		Lines:        lines,
	}
	if !in.EntryDate.IsZero() {
		body.EntryDate = in.EntryDate.Format("2006-01-02")
	}
	if !in.ValueDate.IsZero() {
		body.ValueDate = in.ValueDate.Format("2006-01-02")
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("posting: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/internal/v1/post", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("posting: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.InternalToken != "" {
		req.Header.Set("X-Internal-Token", c.InternalToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("posting: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("posting: accounting returned %d: %s",
			resp.StatusCode, string(b))
	}
	// Drain so the conn can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// outboxPayload is the JSON shape persisted in posting_outbox.payload.
// Distinct from requestBody above (the wire shape) because we want
// the dispatcher to rebuild a fresh PostInput, not an opaque pre-
// marshalled HTTP body — keeps the dispatcher debuggable and lets
// us evolve the wire shape independently of historical outbox rows.
type outboxPayload struct {
	TenantID     uuid.UUID `json:"tenant_id"`
	EntryDate    string    `json:"entry_date,omitempty"`
	ValueDate    string    `json:"value_date,omitempty"`
	SourceModule string    `json:"source_module"`
	SourceRef    string    `json:"source_ref"`
	Narration    string    `json:"narration"`
	Lines        []lineDTO `json:"lines"`
}

// PostTx writes the GL entry to the outbox inside the caller's tx.
// Returns nil on success; the dispatcher will pick up the row on
// its next poll and HTTP-post it.
//
// Dry-run posture: when DryRun=true (test mode only, set via the
// SAVINGS_ALLOW_NO_ACCOUNTING escape), PostTx logs a WARNING per
// call + skips the outbox insert. Every dev session that's missing
// accounting now emits a noisy WARN per money event — impossible
// to overlook.
//
// Caller doesn't roll back on a DryRun no-op; only an actual outbox-
// insert failure (disk full, constraint violation) returns non-nil.
// Any return wrapping ErrOutboxInsert is the signal to surface 502 +
// roll the business write back.
func (c *Client) PostTx(ctx context.Context, tx pgx.Tx, in PostInput) error {
	if c == nil {
		return nil
	}
	if c.DryRun {
		if c.Logger != nil {
			c.Logger.Warn("posting.PostTx: DRY-RUN — outbox row skipped",
				"source_module", in.SourceModule,
				"source_ref", in.SourceRef,
				"tenant_id", in.TenantID,
				"hint", "Set ACCOUNTING_SERVICE_URL to a reachable accounting service to actually post.")
		}
		return nil
	}
	if len(in.Lines) < 2 {
		return errors.New("posting: at least two lines required")
	}
	if in.SourceModule == "" || in.SourceRef == "" {
		return errors.New("posting: source_module and source_ref are required")
	}

	lines := make([]lineDTO, 0, len(in.Lines))
	for _, ln := range in.Lines {
		l := lineDTO{AccountCode: ln.AccountCode, Narration: ln.Narration}
		if !ln.Debit.IsZero() {
			l.Debit = ln.Debit.StringFixed(2)
		}
		if !ln.Credit.IsZero() {
			l.Credit = ln.Credit.StringFixed(2)
		}
		lines = append(lines, l)
	}

	body := outboxPayload{
		TenantID:     in.TenantID,
		SourceModule: in.SourceModule,
		SourceRef:    in.SourceRef,
		Narration:    in.Narration,
		Lines:        lines,
	}
	if !in.EntryDate.IsZero() {
		body.EntryDate = in.EntryDate.Format("2006-01-02")
	}
	if !in.ValueDate.IsZero() {
		body.ValueDate = in.ValueDate.Format("2006-01-02")
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrOutboxInsert, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO posting_outbox (tenant_id, payload)
		VALUES ($1, $2::jsonb)
	`, in.TenantID, buf); err != nil {
		return fmt.Errorf("%w: %v", ErrOutboxInsert, err)
	}
	return nil
}
