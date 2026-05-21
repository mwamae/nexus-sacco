// Budget domain types.

package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type BudgetStatus string

const (
	BudgetDraft     BudgetStatus = "draft"
	BudgetSubmitted BudgetStatus = "submitted"
	BudgetApproved  BudgetStatus = "approved"
	BudgetArchived  BudgetStatus = "archived"
)

// CanTransition encodes the legal budget status moves.
func CanTransitionBudget(from, to BudgetStatus) bool {
	switch from {
	case BudgetDraft:
		return to == BudgetSubmitted || to == BudgetArchived
	case BudgetSubmitted:
		return to == BudgetApproved || to == BudgetDraft || to == BudgetArchived
	case BudgetApproved:
		return to == BudgetArchived
	}
	return false
}

var ErrIllegalBudgetTransition = errors.New("budget: illegal status transition")

type Budget struct {
	ID                  uuid.UUID       `json:"id"`
	TenantID            uuid.UUID       `json:"tenant_id"`
	Name                string          `json:"name"`
	FiscalYear          int             `json:"fiscal_year"`
	PeriodStart         time.Time       `json:"period_start"`
	PeriodEnd           time.Time       `json:"period_end"`
	Status              BudgetStatus    `json:"status"`
	TotalIncomeBudget   decimal.Decimal `json:"total_income_budget"`
	TotalExpenseBudget  decimal.Decimal `json:"total_expense_budget"`
	NetSurplusBudget    decimal.Decimal `json:"net_surplus_budget"`
	Notes               *string         `json:"notes,omitempty"`
	SubmittedAt         *time.Time      `json:"submitted_at,omitempty"`
	SubmittedBy         *uuid.UUID      `json:"submitted_by,omitempty"`
	ApprovedAt          *time.Time      `json:"approved_at,omitempty"`
	ApprovedBy          *uuid.UUID      `json:"approved_by,omitempty"`
	ArchivedAt          *time.Time      `json:"archived_at,omitempty"`
	ArchivedBy          *uuid.UUID      `json:"archived_by,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	CreatedBy           *uuid.UUID      `json:"created_by,omitempty"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type BudgetLine struct {
	ID            uuid.UUID       `json:"id"`
	BudgetID      uuid.UUID       `json:"budget_id"`
	AccountID     uuid.UUID       `json:"account_id"`
	AccountCode   string          `json:"account_code"`
	AccountClass  string          `json:"account_class"`
	PeriodMonth   int             `json:"period_month"`
	Amount        decimal.Decimal `json:"amount"`
	Notes         *string         `json:"notes,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}
