// Unified counterparty model — the natural-person + organisation
// merger. See migration 0007 for schema + design rationale.
//
// Existing Member + OrgMember types stay alive throughout Phase A/B
// because the FK fan-out (loans / shares / deposits etc.) still
// targets members.id. The CounterpartyID field on each side is the
// 1:1 bridge that lets the unified register coexist with the legacy
// stores until Phase C drops them.

package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type CounterpartyKind string

const (
	CounterpartyIndividual CounterpartyKind = "individual"
	CounterpartyChama      CounterpartyKind = "chama"
	CounterpartyCompany    CounterpartyKind = "company"
	CounterpartyNGO        CounterpartyKind = "ngo"
	CounterpartyChurch     CounterpartyKind = "church"
	CounterpartySchool     CounterpartyKind = "school"
	CounterpartyOther      CounterpartyKind = "other"
)

func (k CounterpartyKind) IsIndividual() bool   { return k == CounterpartyIndividual }
func (k CounterpartyKind) IsInstitutional() bool { return k != CounterpartyIndividual && k != "" }

type CounterpartyStatus string

const (
	CPStatusPending     CounterpartyStatus = "pending"
	CPStatusActive      CounterpartyStatus = "active"
	CPStatusDormant     CounterpartyStatus = "dormant"
	CPStatusSuspended   CounterpartyStatus = "suspended"
	CPStatusBlacklisted CounterpartyStatus = "blacklisted"
	CPStatusExited      CounterpartyStatus = "exited"
	CPStatusDeceased    CounterpartyStatus = "deceased"
	CPStatusRejected    CounterpartyStatus = "rejected"
)

type CounterpartyKYCState string

// Prefixed CP* so the existing org-side KYCNotStarted etc. constants in
// types.go stay valid for the legacy org store. The values are wire-
// identical, so a string compare across types still works.
const (
	CPKYCNotStarted CounterpartyKYCState = "not_started"
	CPKYCInReview   CounterpartyKYCState = "in_review"
	CPKYCVerified   CounterpartyKYCState = "verified"
	CPKYCRejected   CounterpartyKYCState = "rejected"
)

type CounterpartyRiskBand string

const (
	CPRiskLow    CounterpartyRiskBand = "low"
	CPRiskMedium CounterpartyRiskBand = "medium"
	CPRiskHigh   CounterpartyRiskBand = "high"
	CPRiskNA     CounterpartyRiskBand = "n_a"
)

// Counterparty mirrors the counterparties table. Kind-specific
// payloads live in Individual / Institution as raw JSON so callers
// that don't care about the discriminator can move the bag around
// without re-parsing.
type Counterparty struct {
	ID             uuid.UUID            `json:"id"`
	TenantID       uuid.UUID            `json:"tenant_id"`
	CPNumber       string               `json:"cp_number"`
	LegacyID       *string              `json:"legacy_id,omitempty"`
	Kind           CounterpartyKind     `json:"kind"`
	DisplayName    string               `json:"display_name"`
	TradingAs      *string              `json:"trading_as,omitempty"`
	Status         CounterpartyStatus   `json:"status"`
	KYCState       CounterpartyKYCState `json:"kyc_state"`
	RiskBand       CounterpartyRiskBand `json:"risk_band"`
	RegistrationNo *string              `json:"registration_no,omitempty"`

	Individual  json.RawMessage `json:"individual,omitempty"`
	Institution json.RawMessage `json:"institution,omitempty"`
	Contact     json.RawMessage `json:"contact"`

	JoinedAt  time.Time  `json:"joined_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`

	// LegacyTargetID — the id of the linked `members` (when
	// kind=individual) or `org_members` (otherwise) row. Populated
	// by SELECTs only; CreateTx returns leave it nil. Frontend uses
	// this to route a row in the unified register to the correct
	// legacy detail page (/members/:id or /orgs/:id) until the
	// detail pages are formally merged in a future PR.
	LegacyTargetID *uuid.UUID `json:"legacy_target_id,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy *uuid.UUID `json:"updated_by,omitempty"`
}
