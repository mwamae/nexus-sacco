// savingsclient — HTTP client for the mpesa→savings handoff.
//
// Phase-4 use case: after a B2C result lands, mpesa needs to tell
// savings "the disbursement is done, finalize the loan and post the
// principal-side GL". We can't do this via shared-DB writes because
// the savings logic (open repayment schedule, set principal_disbursed,
// recompute next-due) lives in ExecuteDisbursementTx + isn't yet in
// the finance/ shared module — phase 4 keeps this lane as HTTP and
// lets phase 5 / a future finance extraction consolidate.
//
// Atomicity: the HTTP call sits OUTSIDE any mpesa tx (it has to —
// savings owns its own tx). If the call fails after Daraja has
// confirmed payment, mpesa's RecordFinalizationAttemptTx stamps the
// failure on the outbound row and the reconciler retries on its own
// schedule. The outbound row IS the source of truth that the call
// has been attempted.

package savingsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type Client struct {
	BaseURL       string
	InternalToken string
	HTTP          *http.Client
}

func New(baseURL, internalToken string) *Client {
	return &Client{
		BaseURL:       baseURL,
		InternalToken: internalToken,
		HTTP:          &http.Client{Timeout: 15 * time.Second},
	}
}

type finalizeReq struct {
	MpesaReceipt string `json:"mpesa_receipt"`
}

// FinalizeDisbursement posts to savings's
// /internal/v1/loans/{id}/finalize-disbursement endpoint. Returns
// nil on 2xx; non-nil error includes the response body for
// debugging.
func (c *Client) FinalizeDisbursement(ctx context.Context, loanID uuid.UUID, mpesaReceipt string) error {
	if c == nil || c.BaseURL == "" {
		return errors.New("savingsclient: no base URL configured (MPESA_SAVINGS_URL)")
	}
	if loanID == uuid.Nil {
		return errors.New("savingsclient: loan_id is required")
	}
	buf, _ := json.Marshal(finalizeReq{MpesaReceipt: mpesaReceipt})
	url := fmt.Sprintf("%s/internal/v1/loans/%s/finalize-disbursement", c.BaseURL, loanID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.InternalToken != "" {
		req.Header.Set("X-Internal-Token", c.InternalToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("savings finalize: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("savings finalize: status %d body %s", resp.StatusCode, string(body))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
