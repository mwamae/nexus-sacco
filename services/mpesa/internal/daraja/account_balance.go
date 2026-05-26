// Daraja AccountBalance — asynchronous balance query.
//
// Safaricom doesn't ship a first-class "statement" API. The
// reconciler approximates one by:
//   1. Calling AccountBalance to capture the paybill's float +
//      headline totals (the daily reconciler runs at 02:00 tenant-
//      local, so the captured balance is "yesterday's close").
//   2. Joining our mpesa_inbound_events + mpesa_outbound_requests
//      against the captured totals.
//
// Like B2C, AccountBalance is asynchronous: the synchronous response
// confirms the request was queued; the actual balance lands at the
// configured ResultURL minutes later. The reconciler treats a
// completed statement-pull as one whose Result callback wrote the
// real totals into mpesa_statement_pulls.result_raw.
//
// For phase 6 we ship the SDK surface + the request-side persistence.
// The Result callback wiring lives in cmd/reconciler — it shares the
// b2c callback skeleton; the result-handler is registered under
// /v1/mpesa/account-balance/{paybill_id}/result (phase 7 if not
// already routed).

package daraja

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// AccountBalanceRequest mirrors Safaricom's documented body. Field
// names match the wire JSON exactly so this struct doubles as
// documentation.
type AccountBalanceRequest struct {
	InitiatorName        string `json:"Initiator"`
	SecurityCredential   string `json:"SecurityCredential"`
	CommandID            string `json:"CommandID"` // always "AccountBalance"
	PartyA               string `json:"PartyA"`    // SACCO shortcode
	IdentifierType       string `json:"IdentifierType"` // "4" = Organization shortcode
	Remarks              string `json:"Remarks"`
	QueueTimeOutURL      string `json:"QueueTimeOutURL"`
	ResultURL            string `json:"ResultURL"`
}

// AccountBalanceResponse — the synchronous reply. ConversationID
// identifies the pending request; the real numbers land at ResultURL.
type AccountBalanceResponse struct {
	OriginatorConversationID string `json:"OriginatorConversationID"`
	ConversationID           string `json:"ConversationID"`
	ResponseCode             string `json:"ResponseCode"`
	ResponseDescription      string `json:"ResponseDescription"`
}

// SubmitAccountBalance posts a balance-query request to Daraja.
// Returns the response when the HTTP succeeded; the caller checks
// ResponseCode to decide whether to mark the statement-pull row
// "queued" or "failed".
func (c *Client) SubmitAccountBalance(ctx context.Context, bearer string, in AccountBalanceRequest) (*AccountBalanceResponse, error) {
	if bearer == "" {
		return nil, errors.New("daraja account_balance: empty bearer token")
	}
	if in.InitiatorName == "" || in.SecurityCredential == "" {
		return nil, errors.New("daraja account_balance: initiator credentials missing")
	}
	if in.CommandID == "" {
		in.CommandID = "AccountBalance"
	}
	if in.IdentifierType == "" {
		in.IdentifierType = "4" // Organization shortcode
	}

	buf, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal account_balance body: %w", err)
	}
	endpoint := c.BaseURL + "/mpesa/accountbalance/v1/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("build account_balance request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("account_balance request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daraja account_balance: status %d body %s", resp.StatusCode, string(body))
	}
	var out AccountBalanceResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode account_balance: %w (body=%s)", err, string(body))
	}
	return &out, nil
}
