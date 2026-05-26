// Daraja B2C payment-request method.
//
// One method on the shared Client. Used by the b2c-dispatcher to
// hand a queued outbound payment over to Safaricom. The immediate
// response carries a conversation_id + originator_conversation_id;
// the actual result lands at the configured ResultURL minutes later
// (handled by services/mpesa/internal/handler/b2c.go::Result).

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

// CommandID identifies which B2C product Safaricom should use.
// SalaryPayment = bulk B2C to KYC'd Safaricom customers.
// BusinessPayment = same shape, different ledger code on their side.
// PromotionPayment = bonuses + welfare; not used by SACCOs but
// accepted here so the field's API surface mirrors the catalogue.
type CommandID string

const (
	CmdSalaryPayment    CommandID = "SalaryPayment"
	CmdBusinessPayment  CommandID = "BusinessPayment"
	CmdPromotionPayment CommandID = "PromotionPayment"
)

// IsValid says whether the supplied CommandID is one of Safaricom's
// documented values. Callers should reject anything else before
// queuing a row.
func (c CommandID) IsValid() bool {
	switch c {
	case CmdSalaryPayment, CmdBusinessPayment, CmdPromotionPayment:
		return true
	}
	return false
}

// B2CRequest is the body Daraja expects on
// POST /mpesa/b2c/v1/paymentrequest. Field names match Safaricom's
// API doc verbatim — the JSON tag is what crosses the wire.
type B2CRequest struct {
	OriginatorConversationID string    `json:"OriginatorConversationID"`
	InitiatorName            string    `json:"InitiatorName"`
	SecurityCredential       string    `json:"SecurityCredential"`
	CommandID                CommandID `json:"CommandID"`
	Amount                   string    `json:"Amount"`
	PartyA                   string    `json:"PartyA"`     // SACCO shortcode
	PartyB                   string    `json:"PartyB"`     // Recipient MSISDN
	Remarks                  string    `json:"Remarks"`
	QueueTimeOutURL          string    `json:"QueueTimeOutURL"`
	ResultURL                string    `json:"ResultURL"`
	Occasion                 string    `json:"Occasion,omitempty"`
}

// B2CResponse — the synchronous reply. Status fields tell us whether
// Daraja queued the request (ResponseCode "0") or rejected it
// outright. The final outcome lands at ResultURL.
type B2CResponse struct {
	ConversationID           string `json:"ConversationID"`
	OriginatorConversationID string `json:"OriginatorConversationID"`
	ResponseCode             string `json:"ResponseCode"`
	ResponseDescription      string `json:"ResponseDescription"`
}

// SubmitB2C posts a signed B2C payment request to Daraja. Caller is
// responsible for the OAuth bearer (via AuthenticateForPaybill) +
// for filling SecurityCredential via the security_credential encoder.
//
// Returns the response when the HTTP layer succeeded (regardless of
// ResponseCode); callers inspect ResponseCode + ResponseDescription
// to decide whether to mark the outbound row sent or failed.
func (c *Client) SubmitB2C(ctx context.Context, bearer string, in B2CRequest) (*B2CResponse, error) {
	if bearer == "" {
		return nil, errors.New("daraja b2c: empty bearer token")
	}
	if !in.CommandID.IsValid() {
		return nil, fmt.Errorf("daraja b2c: invalid CommandID %q", in.CommandID)
	}
	if in.InitiatorName == "" || in.SecurityCredential == "" {
		return nil, errors.New("daraja b2c: initiator credentials missing")
	}

	buf, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal b2c body: %w", err)
	}
	endpoint := c.BaseURL + "/mpesa/b2c/v1/paymentrequest"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("build b2c request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("b2c request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daraja b2c: status %d body %s", resp.StatusCode, string(body))
	}
	var out B2CResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode b2c: %w (body=%s)", err, string(body))
	}
	return &out, nil
}
