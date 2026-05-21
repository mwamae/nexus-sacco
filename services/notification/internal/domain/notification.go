// Notification module domain types.

package domain

import (
	"time"

	"github.com/google/uuid"
)

// ─────────── Enums ───────────

type Category string

const (
	CategoryTransactional Category = "transactional"
	CategoryCampaign      Category = "campaign"
	CategorySystem        Category = "system"
)

type Priority string

const (
	PriorityInfo    Priority = "info"
	PrioritySuccess Priority = "success"
	PriorityWarning Priority = "warning"
	PriorityError   Priority = "error"
)

type Channel string

const (
	ChannelInApp Channel = "in_app"
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
)

func (c Channel) Valid() bool {
	switch c {
	case ChannelInApp, ChannelSMS, ChannelEmail:
		return true
	}
	return false
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusQueued    Status = "queued"
	StatusSent      Status = "sent"
	StatusDelivered Status = "delivered"
	StatusRead      Status = "read"
	StatusFailed    Status = "failed"
)

// ─────────── Entities ───────────

type Event struct {
	Code             string    `json:"code"`
	Category         Category  `json:"category"`
	DefaultPriority  Priority  `json:"default_priority"`
	Description      string    `json:"description"`
	DefaultChannels  []Channel `json:"default_channels"`
	AllowedVariables []string  `json:"allowed_variables"`
	HasPDFAttachment bool      `json:"has_pdf_attachment"`
	IsActive         bool      `json:"is_active"`
	CreatedAt        time.Time `json:"created_at"`
}

type Template struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	EventCode string    `json:"event_code"`
	Channel   Channel   `json:"channel"`
	Subject   *string   `json:"subject,omitempty"`
	Body      string    `json:"body"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Notification struct {
	ID                uuid.UUID  `json:"id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	EventCode         string     `json:"event_code"`
	Priority          Priority   `json:"priority"`
	RecipientMemberID *uuid.UUID `json:"recipient_member_id,omitempty"`
	RecipientUserID   *uuid.UUID `json:"recipient_user_id,omitempty"`
	RecipientName     string     `json:"recipient_name"`
	RecipientPhone    *string    `json:"recipient_phone,omitempty"`
	RecipientEmail    *string    `json:"recipient_email,omitempty"`
	SourceModule      *string    `json:"source_module,omitempty"`
	SourceRecordID    *uuid.UUID `json:"source_record_id,omitempty"`
	DeepLink          *string    `json:"deep_link,omitempty"`
	Payload           []byte     `json:"payload"`
	InitiatedBy       *uuid.UUID `json:"initiated_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

type Delivery struct {
	ID                uuid.UUID  `json:"id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	NotificationID    uuid.UUID  `json:"notification_id"`
	Channel           Channel    `json:"channel"`
	TemplateID        *uuid.UUID `json:"template_id,omitempty"`
	Subject           *string    `json:"subject,omitempty"`
	Body              string     `json:"body"`
	Status            Status     `json:"status"`
	AttemptCount      int        `json:"attempt_count"`
	QueuedAt          *time.Time `json:"queued_at,omitempty"`
	SentAt            *time.Time `json:"sent_at,omitempty"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
	ReadAt            *time.Time `json:"read_at,omitempty"`
	FailedAt          *time.Time `json:"failed_at,omitempty"`
	FailureReason     *string    `json:"failure_reason,omitempty"`
	ProviderMessageID *string    `json:"provider_message_id,omitempty"`
	AttachmentPaths   []string   `json:"attachment_paths,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// FeedItem joins a notification with its in-app delivery row for
// member / staff inbox listings. The single in-app body+status is
// embedded directly so the frontend doesn't have to do its own join.
type FeedItem struct {
	Notification
	Body        string     `json:"body"`
	InAppStatus Status     `json:"in_app_status"`
	ReadAt      *time.Time `json:"read_at,omitempty"`
}

// SMTPEncryption is the wire mode for the SMTP connection.
type SMTPEncryption string

const (
	SMTPNone     SMTPEncryption = "none"
	SMTPStartTLS SMTPEncryption = "starttls"
	SMTPTLS      SMTPEncryption = "tls" // implicit TLS, port 465
)

// SMTPConfig is the per-tenant SMTP configuration. Password is stored
// encrypted at rest; the in-memory struct here may hold the decrypted
// plaintext when the worker is preparing to send.
type SMTPConfig struct {
	TenantID    uuid.UUID      `json:"tenant_id"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	Username    string         `json:"username"`
	Password    string         `json:"-"` // decrypted, never marshalled
	Encryption  SMTPEncryption `json:"encryption"`
	FromAddress string         `json:"from_address"`
	FromName    string         `json:"from_name"`
	ReplyTo     *string        `json:"reply_to,omitempty"`
	IsActive    bool           `json:"is_active"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// SMSProvider selects the dispatch backend. 'mock' is for dev — the
// worker simulates a successful send without hitting any network.
type SMSProvider string

const (
	SMSProviderMock       SMSProvider = "mock"
	SMSProviderSandbox    SMSProvider = "sandbox"
	SMSProviderProduction SMSProvider = "production"
)

// ─────────── PDF documents (Stage 5) ───────────

type PDFTemplate struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	DocumentType string    `json:"document_type"`
	VersionNo    int       `json:"version_no"`
	Label        string    `json:"label"`
	HTMLBody     string    `json:"html_body"`
	PageSize     string    `json:"page_size"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

type PDFDocument struct {
	ID                uuid.UUID  `json:"id"`
	TenantID          uuid.UUID  `json:"tenant_id"`
	DocumentType      string     `json:"document_type"`
	TemplateID        *uuid.UUID `json:"template_id,omitempty"`
	TemplateVersion   *int       `json:"template_version,omitempty"`
	SubjectMemberID   *uuid.UUID `json:"subject_member_id,omitempty"`
	SubjectLoanID     *uuid.UUID `json:"subject_loan_id,omitempty"`
	SubjectAccountID  *uuid.UUID `json:"subject_account_id,omitempty"`
	SubjectLabel      string     `json:"subject_label"`
	Payload           []byte     `json:"payload"`
	StoragePath       string     `json:"-"`                // never returned to clients
	FileSizeBytes     int        `json:"file_size_bytes"`
	DownloadToken     *string    `json:"download_token,omitempty"`
	TokenExpiresAt    *time.Time `json:"token_expires_at,omitempty"`
	DownloadCount     int        `json:"download_count"`
	LastDownloadedAt  *time.Time `json:"last_downloaded_at,omitempty"`
	GeneratedAt       time.Time  `json:"generated_at"`
	GeneratedBy       *uuid.UUID `json:"generated_by,omitempty"`
}

// SMSConfig is the per-tenant Africa's Talking configuration. ApiKey
// and WebhookSecret are stored encrypted at rest; this struct may hold
// the decrypted plaintext at worker / send time.
type SMSConfig struct {
	TenantID       uuid.UUID   `json:"tenant_id"`
	Provider       SMSProvider `json:"provider"`
	Username       string      `json:"username"`
	APIKey         string      `json:"-"` // decrypted, never marshalled
	SenderID       string      `json:"sender_id"`
	RatePerMinute  int         `json:"rate_per_minute"`
	WebhookSecret  string      `json:"-"` // decrypted, never marshalled
	IsActive       bool        `json:"is_active"`
	UpdatedAt      time.Time   `json:"updated_at"`
}
