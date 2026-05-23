// Provisioning domain — loan loss provisioning per SASRA matrix.
//
// A provision run snapshots the portfolio's classification + required
// provision on a given as-of date, then posts the *movement* (this
// run's total provision − the previous posted run's total) to the
// general ledger:
//
//   DR  5210 Loan Loss Provisioning Expense
//   CR  1120 Loan Loss Provision (contra-asset)
//
// If the portfolio improved (provisions are released), the legs flip.
//
// Classification thresholds + provision rates come from tenant_operations
// so each tenant can tighten or relax SASRA defaults. The provisioning
// computation is otherwise mechanical: classify → rate × outstanding.

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type ProvisionRunStatus string

const (
	ProvisionPending    ProvisionRunStatus = "pending"
	ProvisionComputed   ProvisionRunStatus = "computed"
	ProvisionPosted     ProvisionRunStatus = "posted"
	ProvisionFailed     ProvisionRunStatus = "failed"
	ProvisionSuperseded ProvisionRunStatus = "superseded"
)

type ProvisionRun struct {
	ID                 uuid.UUID          `json:"id"`
	TenantID           uuid.UUID          `json:"tenant_id"`
	AsOfDate           time.Time          `json:"as_of_date"`
	Status             ProvisionRunStatus `json:"status"`
	LoansClassified    int                `json:"loans_classified"`
	TotalOutstanding   decimal.Decimal    `json:"total_outstanding"`
	TotalProvision     decimal.Decimal    `json:"total_provision"`
	PreviousProvision  decimal.Decimal    `json:"previous_provision"`
	Movement           decimal.Decimal    `json:"movement"`
	JournalEntryRef    *string            `json:"journal_entry_ref,omitempty"`
	Notes              *string            `json:"notes,omitempty"`
	ComputedAt         *time.Time         `json:"computed_at,omitempty"`
	PostedAt           *time.Time         `json:"posted_at,omitempty"`
	PostedBy           *uuid.UUID         `json:"posted_by,omitempty"`
	CreatedAt          time.Time          `json:"created_at"`
	CreatedBy          *uuid.UUID         `json:"created_by,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

type ProvisionRunLine struct {
	ID                     uuid.UUID       `json:"id"`
	RunID                  uuid.UUID       `json:"run_id"`
	LoanID                 uuid.UUID       `json:"loan_id"`
	CounterpartyID               uuid.UUID       `json:"counterparty_id"`
	LoanNo                 string          `json:"loan_no"`
	DaysPastDue            int             `json:"days_past_due"`
	Classification         string          `json:"classification"`
	Outstanding            decimal.Decimal `json:"outstanding"`
	ProvisionRate          decimal.Decimal `json:"provision_rate"`
	ProvisionAmount        decimal.Decimal `json:"provision_amount"`
	PreviousClassification *string         `json:"previous_classification,omitempty"`
	PreviousProvision      decimal.Decimal `json:"previous_provision"`
}

// ProvisioningRates carries the per-bucket percentages pulled from
// tenant_operations. Performing always provisions at 0 in the SASRA
// matrix — included here for completeness/clarity.
type ProvisioningRates struct {
	Performing  decimal.Decimal // always 0.00
	Watch       decimal.Decimal
	Substandard decimal.Decimal
	Doubtful    decimal.Decimal
	Loss        decimal.Decimal
}

func (r ProvisioningRates) RateFor(classification string) decimal.Decimal {
	switch classification {
	case "watch":
		return r.Watch
	case "substandard":
		return r.Substandard
	case "doubtful":
		return r.Doubtful
	case "loss":
		return r.Loss
	}
	return decimal.Zero
}
