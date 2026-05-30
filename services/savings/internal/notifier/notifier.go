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

	// PDFAttachments — when set, the notification service generates each
	// PDF before dispatching, and attaches the bytes to the outbound
	// email. Each entry mirrors a PDFGenerateRequest minus the per-call
	// boilerplate (tenant + initiator come from the parent Request).
	PDFAttachments []PDFAttachmentSpec `json:"pdf_attachments,omitempty"`
}

// PDFAttachmentSpec — one PDF to attach to the outbound email. The
// notification service renders it via the same /internal/v1/pdf/generate
// path GeneratePDF uses, then attaches the resulting bytes to the
// SMTP message. The notification side derives the filename from
// document_type + subject_label; Filename here is caller-side only
// (notification's request decoder is strict and would reject the
// unknown field, so it's marked with json:"-").
type PDFAttachmentSpec struct {
	DocumentType     string         `json:"document_type"`
	Filename         string         `json:"-"`
	SubjectMemberID  *uuid.UUID     `json:"subject_member_id,omitempty"`
	SubjectLoanID    *uuid.UUID     `json:"subject_loan_id,omitempty"`
	SubjectAccountID *uuid.UUID     `json:"subject_account_id,omitempty"`
	SubjectLabel     string         `json:"subject_label,omitempty"`
	Payload          map[string]any `json:"payload,omitempty"`
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

// ─────────── PDF render (Phase G) ───────────

// PDFGenerateRequest mirrors the internal /pdf/generate endpoint's
// body shape (services/notification/internal/handler/pdf.go). Returned
// PDF document id is what the savings receipt stores as
// receipts.pdf_document_id so the frontend can deep-link to download.
type PDFGenerateRequest struct {
	TenantID         uuid.UUID      `json:"tenant_id"`
	DocumentType     string         `json:"document_type"`
	SubjectMemberID  *uuid.UUID     `json:"subject_member_id,omitempty"`
	SubjectLoanID    *uuid.UUID     `json:"subject_loan_id,omitempty"`
	SubjectAccountID *uuid.UUID     `json:"subject_account_id,omitempty"`
	SubjectLabel     string         `json:"subject_label,omitempty"`
	Payload          map[string]any `json:"payload,omitempty"`
	GeneratedBy      *uuid.UUID     `json:"generated_by,omitempty"`
}

type PDFGenerateResponse struct {
	ID             uuid.UUID `json:"id"`
	DocumentType   string    `json:"document_type"`
	DownloadToken  string    `json:"download_token"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	StoragePath    string    `json:"storage_path"`
}

// GeneratePDF synchronously renders a PDF via the notification
// service. Unlike Notify, this DOES return an error because the
// caller cares about the result (a missing PDF means a broken
// "Download receipt" button on the desk's posted view).
func (c *Client) GeneratePDF(ctx context.Context, req PDFGenerateRequest) (*PDFGenerateResponse, error) {
	if c == nil || c.BaseURL == "" {
		return nil, fmt.Errorf("notifier client disabled (empty BaseURL)")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal pdf request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/internal/v1/pdf/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build pdf request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.InternalToken != "" {
		httpReq.Header.Set("X-Internal-Token", c.InternalToken)
	}
	// PDF rendering is heavier than a normal notify — chromedp + disk
	// write can take a few seconds for big templates. Use a generous
	// timeout but still bounded.
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send pdf request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("notification pdf service returned %d: %s", resp.StatusCode, string(b))
	}
	// The /internal/v1/pdf/generate handler responds with the canonical
	// envelope { "data": {...PDFDocument} }. Decode through that.
	var env struct {
		Data PDFGenerateResponse `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode pdf response: %w", err)
	}
	return &env.Data, nil
}

// FetchPDFBytes server-side fetches the PDF body from the notification
// service's public-download endpoint (/d/{token}). The token IS the
// credential; no Authorization header. Used by the savings handler to
// stream rendered statement PDFs back to the browser without exposing
// the notification host.
func (c *Client) FetchPDFBytes(ctx context.Context, downloadToken string) ([]byte, error) {
	if c == nil || c.BaseURL == "" {
		return nil, fmt.Errorf("notifier client disabled (empty BaseURL)")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/d/"+downloadToken, nil)
	if err != nil {
		return nil, fmt.Errorf("build pdf fetch: %w", err)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch pdf: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("notification pdf download returned %d: %s", resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) log(stage string, err error, req Request) {
	if c.Logger == nil {
		return
	}
	c.Logger.Warn("notifier: send failed",
		"stage", stage, "err", err, "event", req.EventCode, "tenant", req.TenantID)
}
