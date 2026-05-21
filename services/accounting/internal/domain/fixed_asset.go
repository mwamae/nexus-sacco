// Fixed asset domain types.

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type AssetStatus string

const (
	AssetActive             AssetStatus = "active"
	AssetDisposed           AssetStatus = "disposed"
	AssetWrittenOff         AssetStatus = "written_off"
	AssetFullyDepreciated   AssetStatus = "fully_depreciated"
)

type DepreciationMethod string

const (
	MethodStraightLine DepreciationMethod = "straight_line"
	MethodNone         DepreciationMethod = "none"
)

type FixedAsset struct {
	ID                         uuid.UUID          `json:"id"`
	TenantID                   uuid.UUID          `json:"tenant_id"`
	AssetNo                    string             `json:"asset_no"`
	Name                       string             `json:"name"`
	Description                *string            `json:"description,omitempty"`
	Category                   string             `json:"category"`
	GLAssetCode                string             `json:"gl_asset_code"`
	GLAccumulatedCode          string             `json:"gl_accumulated_code"`
	GLExpenseCode              string             `json:"gl_expense_code"`
	PurchaseDate               time.Time          `json:"purchase_date"`
	PurchaseCost               decimal.Decimal    `json:"purchase_cost"`
	SalvageValue               decimal.Decimal    `json:"salvage_value"`
	UsefulLifeMonths           int                `json:"useful_life_months"`
	DepreciationMethod         DepreciationMethod `json:"depreciation_method"`
	Location                   *string            `json:"location,omitempty"`
	Custodian                  *string            `json:"custodian,omitempty"`
	Supplier                   *string            `json:"supplier,omitempty"`
	InvoiceRef                 *string            `json:"invoice_ref,omitempty"`
	AcquisitionJournalEntryID  *uuid.UUID         `json:"acquisition_journal_entry_id,omitempty"`
	Status                     AssetStatus        `json:"status"`
	AccumulatedDepreciation    decimal.Decimal    `json:"accumulated_depreciation"`
	LastDepreciationDate       *time.Time         `json:"last_depreciation_date,omitempty"`
	DisposalJournalEntryID     *uuid.UUID         `json:"disposal_journal_entry_id,omitempty"`
	DisposalProceeds           *decimal.Decimal   `json:"disposal_proceeds,omitempty"`
	DisposalGainLoss           *decimal.Decimal   `json:"disposal_gain_loss,omitempty"`
	DisposedAt                 *time.Time         `json:"disposed_at,omitempty"`
	DisposedBy                 *uuid.UUID         `json:"disposed_by,omitempty"`
	Notes                      *string            `json:"notes,omitempty"`
	CreatedAt                  time.Time          `json:"created_at"`
	CreatedBy                  *uuid.UUID         `json:"created_by,omitempty"`
	UpdatedAt                  time.Time          `json:"updated_at"`
}

// BookValue returns cost − accumulated_depreciation as of the current
// state of the asset.
func (a *FixedAsset) BookValue() decimal.Decimal {
	return a.PurchaseCost.Sub(a.AccumulatedDepreciation)
}

type DepreciationRunStatus string

const (
	DepRunPending    DepreciationRunStatus = "pending"
	DepRunComputed   DepreciationRunStatus = "computed"
	DepRunPosted     DepreciationRunStatus = "posted"
	DepRunFailed     DepreciationRunStatus = "failed"
	DepRunSuperseded DepreciationRunStatus = "superseded"
)

type DepreciationRun struct {
	ID                uuid.UUID             `json:"id"`
	TenantID          uuid.UUID             `json:"tenant_id"`
	AsOfDate          time.Time             `json:"as_of_date"`
	PeriodYear        int                   `json:"period_year"`
	PeriodMonth       int                   `json:"period_month"`
	Status            DepreciationRunStatus `json:"status"`
	AssetsProcessed   int                   `json:"assets_processed"`
	TotalDepreciation decimal.Decimal       `json:"total_depreciation"`
	JournalEntryID    *uuid.UUID            `json:"journal_entry_id,omitempty"`
	Notes             *string               `json:"notes,omitempty"`
	ComputedAt        *time.Time            `json:"computed_at,omitempty"`
	PostedAt          *time.Time            `json:"posted_at,omitempty"`
	PostedBy          *uuid.UUID            `json:"posted_by,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
	CreatedBy         *uuid.UUID            `json:"created_by,omitempty"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

type DepreciationRunLine struct {
	ID                  uuid.UUID       `json:"id"`
	RunID               uuid.UUID       `json:"run_id"`
	AssetID             uuid.UUID       `json:"asset_id"`
	AssetNo             string          `json:"asset_no"`
	AssetName           string          `json:"asset_name"`
	Category            string          `json:"category"`
	Method              string          `json:"method"`
	Cost                decimal.Decimal `json:"cost"`
	Salvage             decimal.Decimal `json:"salvage"`
	AccumulatedBefore   decimal.Decimal `json:"accumulated_before"`
	DepreciationAmount  decimal.Decimal `json:"depreciation_amount"`
	AccumulatedAfter    decimal.Decimal `json:"accumulated_after"`
	BookValueAfter      decimal.Decimal `json:"book_value_after"`
	MonthsDepreciated   int             `json:"months_depreciated"`
}
