// Domain types — the canonical shape the rest of the service (stores,
// handlers, tests) sees. Wire serialisation lives here because JSON
// shape is part of the API contract.

package domain

import (
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
	CreatedBy            *uuid.UUID     `json:"created_by,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
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
