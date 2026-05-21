// Fixed assets persistence + depreciation computation.
//
// The depreciation engine iterates active assets, computes month-by-
// month straight-line depreciation since each asset's last
// depreciation date, and snapshots the result into a run row. The
// handler posts the resulting movement to the GL.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/domain"
)

type FixedAssetStore struct {
	pool *pgxpool.Pool
}

func NewFixedAssetStore(pool *pgxpool.Pool) *FixedAssetStore {
	return &FixedAssetStore{pool: pool}
}

var (
	ErrAssetNotFound      = errors.New("fixed asset not found")
	ErrAssetNotActive     = errors.New("fixed asset is not active")
	ErrDepRunNotFound     = errors.New("depreciation run not found")
	ErrDepAlreadyPosted   = errors.New("a posted depreciation run already exists for this period")
	ErrDepRunNotComputed  = errors.New("depreciation run is not in computed state")
)

// ─────────── Assets ───────────

const assetCols = `
	id, tenant_id, asset_no, name, description, category,
	gl_asset_code, gl_accumulated_code, gl_expense_code,
	purchase_date, purchase_cost, salvage_value, useful_life_months,
	depreciation_method, location, custodian, supplier, invoice_ref,
	acquisition_journal_entry_id, status, accumulated_depreciation,
	last_depreciation_date,
	disposal_journal_entry_id, disposal_proceeds, disposal_gain_loss,
	disposed_at, disposed_by, notes,
	created_at, created_by, updated_at
`

func scanAsset(row pgx.Row) (*domain.FixedAsset, error) {
	var a domain.FixedAsset
	var status, method string
	err := row.Scan(
		&a.ID, &a.TenantID, &a.AssetNo, &a.Name, &a.Description, &a.Category,
		&a.GLAssetCode, &a.GLAccumulatedCode, &a.GLExpenseCode,
		&a.PurchaseDate, &a.PurchaseCost, &a.SalvageValue, &a.UsefulLifeMonths,
		&method, &a.Location, &a.Custodian, &a.Supplier, &a.InvoiceRef,
		&a.AcquisitionJournalEntryID, &status, &a.AccumulatedDepreciation,
		&a.LastDepreciationDate,
		&a.DisposalJournalEntryID, &a.DisposalProceeds, &a.DisposalGainLoss,
		&a.DisposedAt, &a.DisposedBy, &a.Notes,
		&a.CreatedAt, &a.CreatedBy, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	a.Status = domain.AssetStatus(status)
	a.DepreciationMethod = domain.DepreciationMethod(method)
	return &a, nil
}

type CreateAssetInput struct {
	AssetNo            string
	Name               string
	Description        *string
	Category           string
	GLAssetCode        string
	GLAccumulatedCode  string
	GLExpenseCode      string
	PurchaseDate       time.Time
	PurchaseCost       decimal.Decimal
	SalvageValue       decimal.Decimal
	UsefulLifeMonths   int
	DepreciationMethod domain.DepreciationMethod
	Location           *string
	Custodian          *string
	Supplier           *string
	InvoiceRef         *string
	Notes              *string
	CreatedBy          uuid.UUID
}

func (s *FixedAssetStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateAssetInput) (*domain.FixedAsset, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO fixed_assets (
		  tenant_id, asset_no, name, description, category,
		  gl_asset_code, gl_accumulated_code, gl_expense_code,
		  purchase_date, purchase_cost, salvage_value, useful_life_months,
		  depreciation_method, location, custodian, supplier, invoice_ref,
		  notes, created_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		  $13, $14, $15, $16, $17, $18
		)
		RETURNING `+assetCols,
		in.AssetNo, in.Name, in.Description, in.Category,
		in.GLAssetCode, in.GLAccumulatedCode, in.GLExpenseCode,
		in.PurchaseDate, in.PurchaseCost, in.SalvageValue, in.UsefulLifeMonths,
		string(in.DepreciationMethod), in.Location, in.Custodian, in.Supplier, in.InvoiceRef,
		in.Notes, in.CreatedBy,
	)
	return scanAsset(row)
}

func (s *FixedAssetStore) SetAcquisitionJEIDTx(ctx context.Context, tx pgx.Tx, assetID, jeID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE fixed_assets SET acquisition_journal_entry_id = $2, updated_at = now() WHERE id = $1`,
		assetID, jeID,
	)
	return err
}

func (s *FixedAssetStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.FixedAsset, error) {
	row := tx.QueryRow(ctx, `SELECT `+assetCols+` FROM fixed_assets WHERE id = $1`, id)
	a, err := scanAsset(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	return a, err
}

type ListAssetsFilter struct {
	Status   string
	Category string
	Limit    int
}

func (s *FixedAssetStore) ListTx(ctx context.Context, tx pgx.Tx, filter ListAssetsFilter) ([]domain.FixedAsset, error) {
	q := `SELECT ` + assetCols + ` FROM fixed_assets WHERE 1=1`
	var args []any
	pos := 1
	if filter.Status != "" {
		q += fmt.Sprintf(" AND status = $%d", pos)
		args = append(args, filter.Status)
		pos++
	}
	if filter.Category != "" {
		q += fmt.Sprintf(" AND category = $%d", pos)
		args = append(args, filter.Category)
		pos++
	}
	q += " ORDER BY purchase_date DESC, asset_no"
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT $%d", pos)
		args = append(args, filter.Limit)
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.FixedAsset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// RecordDisposalTx flips an asset to 'disposed' and stores the journal
// entry reference + computed gain/loss.
func (s *FixedAssetStore) RecordDisposalTx(
	ctx context.Context, tx pgx.Tx,
	assetID uuid.UUID, proceeds, gainLoss decimal.Decimal,
	jeID uuid.UUID, userID uuid.UUID,
) (*domain.FixedAsset, error) {
	cmd, err := tx.Exec(ctx, `
		UPDATE fixed_assets
		   SET status = 'disposed',
		       disposal_journal_entry_id = $2,
		       disposal_proceeds = $3,
		       disposal_gain_loss = $4,
		       disposed_at = now(),
		       disposed_by = $5,
		       updated_at = now()
		 WHERE id = $1 AND status = 'active'
	`, assetID, jeID, proceeds, gainLoss, userID)
	if err != nil {
		return nil, err
	}
	if cmd.RowsAffected() == 0 {
		return nil, ErrAssetNotActive
	}
	return s.GetTx(ctx, tx, assetID)
}

// ─────────── Depreciation runs ───────────

// ActiveDepreciableAssetsTx — assets eligible for depreciation: status
// active, method != 'none', and book value still above salvage.
func (s *FixedAssetStore) ActiveDepreciableAssetsTx(ctx context.Context, tx pgx.Tx) ([]domain.FixedAsset, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+assetCols+` FROM fixed_assets
		  WHERE status = 'active'
		    AND depreciation_method <> 'none'
		    AND useful_life_months > 0
		    AND purchase_cost - accumulated_depreciation > salvage_value
		  ORDER BY purchase_date, asset_no`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.FixedAsset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// DepreciationDraft — the computed-but-not-persisted line for a single
// asset within a run. Built before we insert any rows so the handler
// can validate totals before committing.
type DepreciationDraft struct {
	Asset              domain.FixedAsset
	AccumulatedBefore  decimal.Decimal
	DepreciationAmount decimal.Decimal
	AccumulatedAfter   decimal.Decimal
	BookValueAfter     decimal.Decimal
	MonthsDepreciated  int
}

// ComputeDepreciationDrafts iterates eligible assets and computes the
// straight-line depreciation that should accrue between each asset's
// last_depreciation_date (or purchase_date) and `asOf`. Caps the
// accumulated amount at cost − salvage.
func ComputeDepreciationDrafts(assets []domain.FixedAsset, asOf time.Time) []DepreciationDraft {
	out := make([]DepreciationDraft, 0, len(assets))
	for _, a := range assets {
		from := a.PurchaseDate
		if a.LastDepreciationDate != nil && a.LastDepreciationDate.After(from) {
			from = *a.LastDepreciationDate
		}
		months := monthsBetween(from, asOf)
		if months <= 0 {
			continue
		}
		depreciable := a.PurchaseCost.Sub(a.SalvageValue)
		if depreciable.IsZero() || depreciable.IsNegative() {
			continue
		}
		monthly := depreciable.Div(decimal.NewFromInt(int64(a.UsefulLifeMonths))).Round(2)
		period := monthly.Mul(decimal.NewFromInt(int64(months)))

		// Cap so accumulated never exceeds depreciable amount.
		maxRemaining := depreciable.Sub(a.AccumulatedDepreciation)
		if period.GreaterThan(maxRemaining) {
			period = maxRemaining
		}
		if period.IsZero() || period.IsNegative() {
			continue
		}

		accumAfter := a.AccumulatedDepreciation.Add(period)
		out = append(out, DepreciationDraft{
			Asset:              a,
			AccumulatedBefore:  a.AccumulatedDepreciation,
			DepreciationAmount: period,
			AccumulatedAfter:   accumAfter,
			BookValueAfter:     a.PurchaseCost.Sub(accumAfter),
			MonthsDepreciated:  months,
		})
	}
	return out
}

// monthsBetween counts whole calendar months between two dates,
// flooring at the previous month's first day. e.g. 2026-01-15 →
// 2026-04-15 = 3 months. Excludes the from-month so we don't
// double-depreciate.
func monthsBetween(from, to time.Time) int {
	if !to.After(from) {
		return 0
	}
	years := to.Year() - from.Year()
	months := int(to.Month()) - int(from.Month())
	return years*12 + months
}

// CreateRunWithLinesTx — persists a depreciation run + its lines, and
// updates each asset's accumulated_depreciation + last_depreciation_date.
// Returns the run in 'computed' status. Caller is responsible for
// posting the GL entry.
type CreateDepRunInput struct {
	AsOfDate  time.Time
	Notes     *string
	CreatedBy uuid.UUID
}

func (s *FixedAssetStore) CreateRunWithLinesTx(
	ctx context.Context, tx pgx.Tx,
	in CreateDepRunInput,
	drafts []DepreciationDraft,
) (*domain.DepreciationRun, error) {
	year := in.AsOfDate.Year()
	month := int(in.AsOfDate.Month())

	// A posted run for the same period blocks creation.
	var existing int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM depreciation_runs
		 WHERE period_year = $1 AND period_month = $2 AND status = 'posted'
	`, year, month).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, ErrDepAlreadyPosted
	}

	// Supersede any prior pending/computed/failed run for this period.
	if _, err := tx.Exec(ctx, `
		UPDATE depreciation_runs
		   SET status = 'superseded', updated_at = now()
		 WHERE period_year = $1 AND period_month = $2
		   AND status IN ('pending','computed','failed')
	`, year, month); err != nil {
		return nil, err
	}

	runID := uuid.New()
	var total decimal.Decimal
	for _, d := range drafts {
		total = total.Add(d.DepreciationAmount)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO depreciation_runs (
		  id, tenant_id, as_of_date, period_year, period_month, status,
		  assets_processed, total_depreciation, notes, created_by, computed_at
		) VALUES (
		  $1, current_tenant_id(), $2, $3, $4, 'computed', $5, $6, $7, $8, now()
		)
	`, runID, in.AsOfDate, year, month, len(drafts), total, in.Notes, in.CreatedBy); err != nil {
		return nil, err
	}

	for _, d := range drafts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO depreciation_run_lines (
			  tenant_id, run_id, asset_id, asset_no, asset_name, category, method,
			  cost, salvage, accumulated_before, depreciation_amount,
			  accumulated_after, book_value_after, months_depreciated
			) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`,
			runID, d.Asset.ID, d.Asset.AssetNo, d.Asset.Name, d.Asset.Category,
			string(d.Asset.DepreciationMethod),
			d.Asset.PurchaseCost, d.Asset.SalvageValue, d.AccumulatedBefore,
			d.DepreciationAmount, d.AccumulatedAfter, d.BookValueAfter, d.MonthsDepreciated,
		); err != nil {
			return nil, err
		}

		// Bump the asset.
		newStatus := string(d.Asset.Status)
		if d.AccumulatedAfter.GreaterThanOrEqual(d.Asset.PurchaseCost.Sub(d.Asset.SalvageValue)) {
			newStatus = string(domain.AssetFullyDepreciated)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE fixed_assets
			   SET accumulated_depreciation = $2,
			       last_depreciation_date   = $3,
			       status                   = $4,
			       updated_at               = now()
			 WHERE id = $1
		`, d.Asset.ID, d.AccumulatedAfter, in.AsOfDate, newStatus); err != nil {
			return nil, err
		}
	}

	return s.GetRunTx(ctx, tx, runID)
}

const depRunCols = `
	id, tenant_id, as_of_date, period_year, period_month, status,
	assets_processed, total_depreciation, journal_entry_id, notes,
	computed_at, posted_at, posted_by, created_at, created_by, updated_at
`

func scanDepRun(row pgx.Row) (*domain.DepreciationRun, error) {
	var r domain.DepreciationRun
	var status string
	err := row.Scan(
		&r.ID, &r.TenantID, &r.AsOfDate, &r.PeriodYear, &r.PeriodMonth, &status,
		&r.AssetsProcessed, &r.TotalDepreciation, &r.JournalEntryID, &r.Notes,
		&r.ComputedAt, &r.PostedAt, &r.PostedBy, &r.CreatedAt, &r.CreatedBy, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.Status = domain.DepreciationRunStatus(status)
	return &r, nil
}

func (s *FixedAssetStore) GetRunTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.DepreciationRun, error) {
	row := tx.QueryRow(ctx, `SELECT `+depRunCols+` FROM depreciation_runs WHERE id = $1`, id)
	r, err := scanDepRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDepRunNotFound
	}
	return r, err
}

func (s *FixedAssetStore) ListRunsTx(ctx context.Context, tx pgx.Tx, limit int) ([]domain.DepreciationRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := tx.Query(ctx,
		`SELECT `+depRunCols+` FROM depreciation_runs ORDER BY as_of_date DESC, created_at DESC LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DepreciationRun{}
	for rows.Next() {
		r, err := scanDepRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *FixedAssetStore) ListRunLinesTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID) ([]domain.DepreciationRunLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, run_id, asset_id, asset_no, asset_name, category, method,
		       cost, salvage, accumulated_before, depreciation_amount,
		       accumulated_after, book_value_after, months_depreciated
		  FROM depreciation_run_lines WHERE run_id = $1
		  ORDER BY asset_no
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DepreciationRunLine{}
	for rows.Next() {
		var l domain.DepreciationRunLine
		if err := rows.Scan(
			&l.ID, &l.RunID, &l.AssetID, &l.AssetNo, &l.AssetName, &l.Category, &l.Method,
			&l.Cost, &l.Salvage, &l.AccumulatedBefore, &l.DepreciationAmount,
			&l.AccumulatedAfter, &l.BookValueAfter, &l.MonthsDepreciated,
		); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *FixedAssetStore) MarkRunPostedTx(
	ctx context.Context, tx pgx.Tx,
	runID, jeID uuid.UUID, userID uuid.UUID,
) (*domain.DepreciationRun, error) {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM depreciation_runs WHERE id = $1`, runID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDepRunNotFound
		}
		return nil, err
	}
	if status != string(domain.DepRunComputed) {
		return nil, ErrDepRunNotComputed
	}
	if _, err := tx.Exec(ctx, `
		UPDATE depreciation_runs
		   SET status = 'posted',
		       journal_entry_id = $2,
		       posted_at = now(),
		       posted_by = $3,
		       updated_at = now()
		 WHERE id = $1
	`, runID, jeID, userID); err != nil {
		return nil, err
	}
	return s.GetRunTx(ctx, tx, runID)
}
