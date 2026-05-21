// Notifier — thin HTTP client to the central notification service.
//
// Every cash-touching module in the platform routes notifications
// through this client rather than calling SMS / email providers
// directly. Failures here MUST be non-fatal to the caller: if the
// notification service is down, the underlying business event (loan
// approval, deposit, etc.) still commits — we log + move on.

package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type Client struct {
	BaseURL       string
	InternalToken string
	HTTP          *http.Client
	Logger        *slog.Logger
}

type Channel string

const (
	ChannelInApp Channel = "in_app"
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
)

type Request struct {
	TenantID          uuid.UUID      `json:"tenant_id"`
	EventCode         string         `json:"event_code"`
	Priority          string         `json:"priority,omitempty"`
	Channels          []Channel      `json:"channels,omitempty"`
	RecipientMemberID *uuid.UUID     `json:"recipient_member_id,omitempty"`
	RecipientUserID   *uuid.UUID     `json:"recipient_user_id,omitempty"`
	RecipientName     string         `json:"recipient_name,omitempty"`
	RecipientPhone    *string        `json:"recipient_phone,omitempty"`
	RecipientEmail    *string        `json:"recipient_email,omitempty"`
	SourceModule      *string        `json:"source_module,omitempty"`
	SourceRecordID    *uuid.UUID     `json:"source_record_id,omitempty"`
	DeepLink          *string        `json:"deep_link,omitempty"`
	Payload           map[string]any `json:"payload,omitempty"`
	InitiatedBy       *uuid.UUID     `json:"initiated_by,omitempty"`
}

// New creates a client. BaseURL like "http://localhost:8085". An empty
// BaseURL disables the client (Notify becomes a no-op) — useful for
// dev environments where the notification service isn't running.
func New(baseURL, internalToken string, logger *slog.Logger) *Client {
	return &Client{
		BaseURL:       baseURL,
		InternalToken: internalToken,
		HTTP:          &http.Client{Timeout: 5 * time.Second},
		Logger:        logger,
	}
}

// Notify fires a notification. Never blocks the caller for long
// (5 sec timeout) and never returns an error worth aborting on — at
// worst the business operation succeeded but the user didn't get
// notified.
func (c *Client) Notify(ctx context.Context, req Request) {
	if c == nil || c.BaseURL == "" {
		return
	}
	body, err := json.Marshal(req)
	if err != nil {
		c.log("marshal", err, req)
		return
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/internal/v1/notify", bytes.NewReader(body))
	if err != nil {
		c.log("build request", err, req)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.InternalToken != "" {
		httpReq.Header.Set("X-Internal-Token", c.InternalToken)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		c.log("send", err, req)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		c.log(fmt.Sprintf("status=%d body=%s", resp.StatusCode, string(b)), nil, req)
		return
	}
	// Drain body to allow connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)
}

func (c *Client) log(stage string, err error, req Request) {
	if c.Logger == nil {
		return
	}
	c.Logger.Warn("notifier: send failed",
		"stage", stage, "err", err, "event", req.EventCode, "tenant", req.TenantID)
}
