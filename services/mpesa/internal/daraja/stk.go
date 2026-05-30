// Daraja STK Push (Lipa Na M-PESA Online) — initiate a customer-
// approved C2B payment by pushing a confirmation prompt to the
// member's phone.
//
// Used by the standing-order processor's mpesa_pull source: the
// SACCO pushes a prompt; the member taps "OK + PIN"; Daraja later
// POSTs an async callback with the result. The successful path also
// fires a normal C2B confirmation against the paybill's webhook URL
// (some Safaricom envs do, some don't — we therefore mirror the
// STK callback into mpesa_inbound_events ourselves to guarantee
// the distribution waterfall runs exactly once).

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

// STKTransactionType — Safaricom's documented values. CustomerPayBillOnline
// for paybills; CustomerBuyGoodsOnline for till numbers.
type STKTransactionType string

const (
	STKPaybillOnline   STKTransactionType = "CustomerPayBillOnline"
	STKBuyGoodsOnline  STKTransactionType = "CustomerBuyGoodsOnline"
)

// STKPushRequest is the body Daraja expects on
// POST /mpesa/stkpush/v1/processrequest. Field names match the
// Safaricom API doc verbatim.
type STKPushRequest struct {
	BusinessShortCode string             `json:"BusinessShortCode"` // paybill number
	Password          string             `json:"Password"`          // base64(shortcode+passkey+timestamp)
	Timestamp         string             `json:"Timestamp"`         // YYYYMMDDHHMMSS
	TransactionType   STKTransactionType `json:"TransactionType"`
	Amount            string             `json:"Amount"`            // integer KES, as a string
	PartyA            string             `json:"PartyA"`            // payer MSISDN (254XXXXXXXXX)
	PartyB            string             `json:"PartyB"`            // paybill / till
	PhoneNumber       string             `json:"PhoneNumber"`       // payer MSISDN, same as PartyA for paybill
	CallBackURL       string             `json:"CallBackURL"`
	AccountReference  string             `json:"AccountReference"`  // ≤12 chars; what BillRef would have been
	TransactionDesc   string             `json:"TransactionDesc"`   // ≤13 chars
}

// STKPushResponse — synchronous reply. ResponseCode "0" means
// "queued — wait for the async callback"; anything else is a hard
// reject (don't bother waiting).
type STKPushResponse struct {
	MerchantRequestID   string `json:"MerchantRequestID"`
	CheckoutRequestID   string `json:"CheckoutRequestID"`
	ResponseCode        string `json:"ResponseCode"`
	ResponseDescription string `json:"ResponseDescription"`
	CustomerMessage     string `json:"CustomerMessage"`
}

// STKCallbackEnvelope mirrors the shape Daraja POSTs to CallBackURL
// after the user responds (or the request times out).
type STKCallbackEnvelope struct {
	Body struct {
		StkCallback struct {
			MerchantRequestID string `json:"MerchantRequestID"`
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode        int    `json:"ResultCode"`
			ResultDesc        string `json:"ResultDesc"`
			CallbackMetadata  struct {
				Item []struct {
					Name  string      `json:"Name"`
					Value interface{} `json:"Value"`
				} `json:"Item"`
			} `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}

// PickCallbackItem reads a single Name=… field out of the metadata
// array. Returns the empty string when missing.
func PickCallbackItem(env STKCallbackEnvelope, name string) string {
	for _, it := range env.Body.StkCallback.CallbackMetadata.Item {
		if it.Name != name {
			continue
		}
		switch v := it.Value.(type) {
		case string:
			return v
		case float64:
			return fmt.Sprintf("%v", v)
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// SubmitSTKPush hits Daraja's STK initiate endpoint. Caller must
// have signed in via AuthenticateForPaybill (bearer) and pre-built
// the Password via PasswordForSTKPush.
//
// Returns the response when the HTTP layer succeeded (regardless of
// ResponseCode); the caller inspects ResponseCode + ResponseDescription
// to decide whether to wait on the async callback or fail fast.
func (c *Client) SubmitSTKPush(ctx context.Context, bearer string, in STKPushRequest) (*STKPushResponse, []byte, error) {
	if bearer == "" {
		return nil, nil, errors.New("daraja stk: empty bearer token")
	}
	if in.BusinessShortCode == "" || in.PartyA == "" || in.PartyB == "" ||
		in.Password == "" || in.Timestamp == "" || in.CallBackURL == "" {
		return nil, nil, errors.New("daraja stk: missing required fields")
	}
	if in.TransactionType == "" {
		in.TransactionType = STKPaybillOnline
	}
	if in.PhoneNumber == "" {
		in.PhoneNumber = in.PartyA
	}

	buf, err := json.Marshal(in)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal stk body: %w", err)
	}
	endpoint := c.BaseURL + "/mpesa/stkpush/v1/processrequest"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, nil, fmt.Errorf("build stk request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("stk request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, body, fmt.Errorf("daraja stk: status %d body %s", resp.StatusCode, string(body))
	}
	var out STKPushResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, body, fmt.Errorf("decode stk: %w (body=%s)", err, string(body))
	}
	return &out, body, nil
}
