// HTTP client for the accounting service's /internal/v1/post endpoint.
// Mirrors the savings → accounting client (services/savings/internal/posting).
//
// Used by the member-service activation pipeline to post the
// registration-fee journal entry once an application has been
// approved + materialized. Idempotent on (source_module, source_ref)
// — re-running an activation against the same application yields the
// same journal entry.

package accounting

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
	"github.com/shopspring/decimal"
)

type Client struct {
	BaseURL       string
	InternalToken string
	HTTP          *http.Client
	Logger        *slog.Logger
	Disabled      bool
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

type Line struct {
	AccountCode string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	Narration   string
}

type PostInput struct {
	TenantID     uuid.UUID
	EntryDate    time.Time
	ValueDate    time.Time
	SourceModule string
	SourceRef    string
	Narration    string
	Lines        []Line
}

// PostResult is the subset of the accounting service's response we care
// about — just the journal entry id so the caller can store it on the
// application row.
type PostResult struct {
	EntryID uuid.UUID
	EntryNo string
}

var ErrDisabled = errors.New("accounting client disabled (no base URL)")

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

// responseEnvelope matches the accounting service's HTTP 200 / 201
// shape: { "data": { "entry": {...}, "idempotent": bool } } on the
// idempotent path, or { "data": { ...entry fields direct... } } on a
// fresh post. The PostResult fields are parsed from either.
type responseEnvelope struct {
	Data struct {
		// Fresh-post path: top-level entry fields
		ID      uuid.UUID `json:"id"`
		EntryNo string    `json:"entry_no"`
		// Idempotent path: wrapped under .entry
		Entry struct {
			ID      uuid.UUID `json:"id"`
			EntryNo string    `json:"entry_no"`
		} `json:"entry"`
		Idempotent bool `json:"idempotent"`
	} `json:"data"`
}

func (c *Client) Post(ctx context.Context, in PostInput) (*PostResult, error) {
	if c == nil || c.Disabled {
		return nil, ErrDisabled
	}
	if len(in.Lines) < 2 {
		return nil, errors.New("at least two lines required")
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
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/internal/v1/post", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.InternalToken != "" {
		req.Header.Set("X-Internal-Token", c.InternalToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("accounting returned %d: %s", resp.StatusCode, string(respBody))
	}
	var env responseEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	result := &PostResult{}
	if env.Data.Entry.ID != uuid.Nil {
		result.EntryID = env.Data.Entry.ID
		result.EntryNo = env.Data.Entry.EntryNo
	} else {
		result.EntryID = env.Data.ID
		result.EntryNo = env.Data.EntryNo
	}
	return result, nil
}
