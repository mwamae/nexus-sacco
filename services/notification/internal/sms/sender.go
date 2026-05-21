// Africa's Talking SMS sender.
//
// Three providers:
//   • mock        — never hits the network; returns a deterministic
//                   message id so the worker pipeline can be tested
//                   without an AT account. Marked "sent" by the
//                   worker; "delivered" only when the mock webhook
//                   is called (or after a short grace period).
//   • sandbox     — POST https://api.sandbox.africastalking.com/version1/messaging
//   • production  — POST https://api.africastalking.com/version1/messaging
//
// We use the v1 (form-encoded) endpoint rather than v3 (JSON) because
// it's the most stable across AT account types and the response shape
// is identical for our needs.

package sms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nexussacco/notification/internal/domain"
)

type Message struct {
	To   string // E.164, e.g. +254712345678
	From string // sender id (alphanumeric) — falls back to cfg.SenderID
	Body string
}

type SendResult struct {
	ProviderMessageID string
	Cost              string // e.g. "KES 1.0000"
	StatusText        string // e.g. "Success"
}

// Send dispatches one SMS via the configured provider.
// Errors here are transient (network / 5xx) — the worker reschedules.
// Per-recipient business failures (invalid number, insufficient
// balance, blacklist) are bubbled as errors too; the worker decides
// whether to retry based on the AT status text.
func Send(httpClient *http.Client, cfg *domain.SMSConfig, msg Message) (*SendResult, error) {
	if cfg == nil {
		return nil, errors.New("sms: nil config")
	}
	if msg.To == "" {
		return nil, errors.New("sms: recipient phone is empty")
	}
	if msg.Body == "" {
		return nil, errors.New("sms: body is empty")
	}
	sender := msg.From
	if sender == "" {
		sender = cfg.SenderID
	}

	if cfg.Provider == domain.SMSProviderMock {
		// Deterministic-ish id so tests can correlate.
		id := fmt.Sprintf("MOCK-%d-%s", time.Now().UnixNano(), uuid.NewString()[:8])
		return &SendResult{ProviderMessageID: id, Cost: "MOCK 0.0", StatusText: "Success"}, nil
	}

	endpoint := "https://api.africastalking.com/version1/messaging"
	if cfg.Provider == domain.SMSProviderSandbox {
		endpoint = "https://api.sandbox.africastalking.com/version1/messaging"
	}
	if cfg.Username == "" || cfg.APIKey == "" {
		return nil, errors.New("sms: AT username and api_key are required for non-mock providers")
	}

	form := url.Values{}
	form.Set("username", cfg.Username)
	form.Set("to", msg.To)
	form.Set("message", msg.Body)
	if sender != "" {
		form.Set("from", sender)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("apikey", cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("at: %d %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode >= 400 {
		// 4xx is typically client error (bad creds, bad number) — retry
		// won't help. Still bubble up so the worker can fail-final on
		// repeated 4xx.
		return nil, fmt.Errorf("at: %d %s", resp.StatusCode, string(body))
	}

	// AT response: { "SMSMessageData": { "Message": "Sent to 1/1 Total Cost: KES 1.0000",
	//                "Recipients": [{ "statusCode":101,"number":"+254...","cost":"KES 1.0000","status":"Success","messageId":"ATXid_..." }] } }
	var atResp struct {
		SMSMessageData struct {
			Message    string `json:"Message"`
			Recipients []struct {
				StatusCode int    `json:"statusCode"`
				Number     string `json:"number"`
				Cost       string `json:"cost"`
				Status     string `json:"status"`
				MessageID  string `json:"messageId"`
			} `json:"Recipients"`
		} `json:"SMSMessageData"`
	}
	if err := json.Unmarshal(body, &atResp); err != nil {
		return nil, fmt.Errorf("parse at response: %w (body: %s)", err, string(body))
	}
	if len(atResp.SMSMessageData.Recipients) == 0 {
		return nil, fmt.Errorf("at: no recipients in response (body: %s)", string(body))
	}
	rec := atResp.SMSMessageData.Recipients[0]
	if rec.Status != "Success" {
		return nil, fmt.Errorf("at: recipient status %q (code=%d)", rec.Status, rec.StatusCode)
	}
	return &SendResult{
		ProviderMessageID: rec.MessageID,
		Cost:              rec.Cost,
		StatusText:        rec.Status,
	}, nil
}

// DefaultClient returns a sensible http.Client for SMS dispatch.
func DefaultClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}
