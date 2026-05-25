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
// In-process composition (calling the accounting Engine directly
// from PostTx, no HTTP hop) is deferred to a follow-up — see the
// Step-0 brief that proposed Option B for context.

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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ErrOutboxInsert wraps any failure of the in-tx outbox INSERT.
// Handlers surface this as 502 + code "gl_post_failed" to the
// caller, and the business write rolls back.
var ErrOutboxInsert = errors.New("posting: outbox insert failed")

type Client struct {
	BaseURL       string
	InternalToken string
	HTTP          *http.Client
	Logger        *slog.Logger
	// Disabled — when true, every Post() is a no-op. Lets dev environments
	// run without the accounting service while we wire integrations.
	Disabled bool
}

func New(baseURL, internalToken string, logger *slog.Logger) *Client {
	return &Client{
		BaseURL:       baseURL,
		InternalToken: internalToken,
		HTTP:          &http.Client{Timeout: 8 * time.Second},
		Logger:        logger,
		Disabled:      baseURL == "",
	}
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
	if c == nil || c.Disabled {
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
// When Disabled (dev / test), PostTx is a no-op — preserves
// today's "no GL evidence in dev without a running accounting"
// semantic. The test fixtures rely on this. In production
// Disabled is never set; the dispatcher binary fails fast if the
// accounting URL is empty.
//
// Caller doesn't roll back on a Disabled return; only an actual
// outbox-insert failure (disk full, constraint violation) returns
// non-nil. Any return wrapping ErrOutboxInsert is the signal to
// surface 502 + roll the business write back.
func (c *Client) PostTx(ctx context.Context, tx pgx.Tx, in PostInput) error {
	if c == nil || c.Disabled {
		return nil // dev / test — outbox stays empty
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
