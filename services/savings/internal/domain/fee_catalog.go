// Fee-catalog domain types. The catalog is the per-tenant list of
// stand-alone fees the Collection Desk's "Fee payment" line type can
// reference. Loan- and deposit-product fees stay in their own tables;
// this catalog is for fees that aren't tied to a product (membership,
// statement, ad-hoc, welfare, etc.).

package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type FeeCatalogEntry struct {
	ID             uuid.UUID       `json:"id"`
	TenantID       uuid.UUID       `json:"tenant_id"`
	Code           string          `json:"code"`
	Label          string          `json:"label"`
	Description    *string         `json:"description,omitempty"`
	AmountDefault  decimal.Decimal `json:"amount_default"`
	AmountEditable bool            `json:"amount_editable"`
	GLCreditCode   string          `json:"gl_credit_code"`
	IsActive       bool            `json:"is_active"`
	SortOrder      int             `json:"sort_order"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}
