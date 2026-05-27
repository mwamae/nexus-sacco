// Domain types — the canonical shape the rest of the service (stores,
// handlers, tests) sees. Wire serialisation lives here because JSON
// shape is part of the API contract.

package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Environment string

const (
	EnvSandbox    Environment = "sandbox"
	EnvProduction Environment = "production"
)

type PaybillPurpose string

const (
	PurposeCollection   PaybillPurpose = "collection"
	PurposeDisbursement PaybillPurpose = "disbursement"
	PurposeBoth         PaybillPurpose = "both"
)

type PaybillStatus string

const (
	PaybillActive   PaybillStatus = "active"
	PaybillDisabled PaybillStatus = "disabled"
)

type CredentialKind string

const (
	CredConsumerKey       CredentialKind = "consumer_key"
	CredConsumerSecret    CredentialKind = "consumer_secret"
	CredPasskey           CredentialKind = "passkey"
	CredInitiatorName     CredentialKind = "initiator_name"
	CredInitiatorPassword CredentialKind = "initiator_password"
)

// Paybill is one registered Safaricom paybill / till + the metadata
// the dashboard surfaces. Credentials live in mpesa_paybill_credentials
// and are joined only at the moment a handler needs to talk to Daraja.
type Paybill struct {
	ID                   uuid.UUID      `json:"id"`
	TenantID             uuid.UUID      `json:"tenant_id"`
	Label                string         `json:"label"`
	Shortcode            string         `json:"shortcode"`
	Purpose              PaybillPurpose `json:"purpose"`
	Scope                []string       `json:"scope"`
	Environment          Environment    `json:"environment"`
	Status               PaybillStatus  `json:"status"`
	DistributionPolicyID *uuid.UUID     `json:"distribution_policy_id,omitempty"`
	// Phase 2 additions:
	StrictValidation    bool   `json:"strict_validation"`
	AllowMSISDNFallback bool   `json:"allow_msisdn_fallback"`
	// WebhookToken is the per-paybill shared secret Safaricom appends
	// to the validation/confirmation URL as `?token=…`. Returned in
	// full on the create-paybill response so the operator can paste
	// it into the Daraja portal; thereafter it's surfaced only via
	// the explicit "rotate token" path (phase 3).
	WebhookToken string     `json:"webhook_token,omitempty"`
	// IsDefault flags the paybill the loan-disburse picker uses when
	// no explicit paybill is named. At most one paybill per tenant
	// per purpose should carry this flag; enforced by partial UNIQUE
	// index in migration 0007.
	IsDefault bool       `json:"is_default"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// InboundStatus mirrors the mpesa_inbound_status enum: the lifecycle
// of a received C2B event through the distribution engine.
type InboundStatus string

const (
	InboundReceived    InboundStatus = "received"
	InboundDistributed InboundStatus = "distributed"
	InboundFailed      InboundStatus = "failed"
)

// ResolvedVia is the resolver's verdict: which branch matched, or
// 'unallocated' when none did.
type ResolvedVia string

const (
	ViaMemberNo         ResolvedVia = "member_no"
	ViaCPNumber         ResolvedVia = "cp_number"
	ViaLoanNo           ResolvedVia = "loan_no"
	ViaDepositAccountNo ResolvedVia = "deposit_account_no"
	ViaMSISDN           ResolvedVia = "msisdn"
	ViaUnallocated      ResolvedVia = "unallocated"
)

// OutboundKind mirrors the mpesa_outbound_kind enum.
type OutboundKind string

const (
	OutboundB2CDisbursement OutboundKind = "b2c_disbursement"
	OutboundRefund          OutboundKind = "refund"
)

// OutboundStatus mirrors the mpesa_outbound_status enum (phase-4
// added 'reversed').
type OutboundStatus string

const (
	OutboundPending   OutboundStatus = "pending"
	OutboundSent      OutboundStatus = "sent"
	OutboundCompleted OutboundStatus = "completed"
	OutboundFailed    OutboundStatus = "failed"
	OutboundReversed  OutboundStatus = "reversed"
)

// InboundEvent is one C2B confirmation as stored. The raw Safaricom
// payload is preserved verbatim in RawPayload so the resolver can be
// re-run from history if its rules change.
type InboundEvent struct {
	ID                 uuid.UUID       `json:"id"`
	TenantID           uuid.UUID       `json:"tenant_id"`
	PaybillID          *uuid.UUID      `json:"paybill_id,omitempty"`
	Shortcode          string          `json:"shortcode"`
	TransactionID      string          `json:"transaction_id"` // Safaricom MpesaReceiptNumber
	TransactionTime    *time.Time      `json:"transaction_time,omitempty"`
	Amount             string          `json:"amount"`
	MSISDN             string          `json:"msisdn,omitempty"`
	BillRef            string          `json:"bill_ref,omitempty"`
	RawPayload         json.RawMessage `json:"raw_payload"`
	Status             InboundStatus   `json:"status"`
	ResolvedMemberID   *uuid.UUID      `json:"resolved_member_id,omitempty"`
	ResolvedVia        *ResolvedVia    `json:"resolved_via,omitempty"`
	WorkflowInstanceID *uuid.UUID      `json:"workflow_instance_id,omitempty"`
	ReceivedAt         time.Time       `json:"received_at"`
}

// CredentialMetadata is what the credential CRUD endpoints return.
// `ciphertext` is intentionally NOT exposed via JSON — the only way
// to obtain bytes is via the SECURITY DEFINER function inside the
// service, and even then only the test-auth handler is expected to
// trigger that path in phase 1.
type CredentialMetadata struct {
	ID        uuid.UUID      `json:"id"`
	PaybillID uuid.UUID      `json:"paybill_id"`
	Kind      CredentialKind `json:"kind"`
	KeyID     string         `json:"key_id"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// DistributionPolicy is the waterfall definition a paybill points at
// when an inbound payment needs splitting. Phase 1 only ships the
// schema; CRUD comes in phase 2.
type DistributionPolicy struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Waterfall   any       `json:"waterfall"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
