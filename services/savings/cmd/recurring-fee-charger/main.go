// Recurring-fee charger (DSID Phase 2.2).
//
// Daily tick (configurable via FEE_CHARGER_INTERVAL_HOURS, default 24).
// For each active fee definition, scan every active account on the
// product. If a charge row for (account, fee, current_period) does
// not already exist, insert one + post the JE:
//   DR <product liability acct>
//   CR <fee.gl_credit_code>
//
// Insufficient available balance is captured as
// status='insufficient_funds' (no JE posted) so the officer dashboard
// can chase the member; idempotency guaranteed by the UNIQUE on
// (account, fee, period).

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/shared/healthx"
)

var version string

func workerVersion() string {
	if version != "" {
		return version
	}
	if v := os.Getenv("BUILD_VERSION"); v != "" {
		return v
	}
	return "dev"
}

func main() {
	once := flag.Bool("once", false, "scan once and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	intervalHr := 24
	if v := os.Getenv("FEE_CHARGER_INTERVAL_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			intervalHr = n
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db: connect", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	postingClient, _ := posting.New(cfg.AccountingURL, cfg.AccountingInternalToken, logger)
	feesStore := store.NewRecurringFeeStore(dbPool.Pool)

	if !*once {
		go healthx.RunHeartbeatLoop(
			ctx, dbPool.Pool, "recurring-fee-charger", workerVersion(),
			30*time.Second, nil, logger,
		)
	}

	logger.Info("recurring-fee-charger: starting", "interval_hr", intervalHr, "once", *once)

	if err := runOnce(ctx, dbPool, feesStore, postingClient, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("run", "err", err)
	}
	if *once {
		return
	}
	tick := time.NewTicker(time.Duration(intervalHr) * time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := runOnce(ctx, dbPool, feesStore, postingClient, logger); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("run", "err", err)
			}
		}
	}
}

func runOnce(ctx context.Context, dbPool *db.Pool, fees *store.RecurringFeeStore, postingClient *posting.Client, logger *slog.Logger) error {
	// 1. Collect distinct tenants with any active recurring fee.
	rows, err := dbPool.Pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		  FROM deposit_product_recurring_fees
		 WHERE active = true
		   AND starts_on <= CURRENT_DATE
		   AND (ends_on IS NULL OR ends_on >= CURRENT_DATE)
	`)
	if err != nil {
		return fmt.Errorf("scan tenants: %w", err)
	}
	var tenantIDs []uuid.UUID
	for rows.Next() {
		var t uuid.UUID
		if err := rows.Scan(&t); err == nil {
			tenantIDs = append(tenantIDs, t)
		}
	}
	rows.Close()

	now := time.Now().UTC()
	totalPosted, totalSkipped, totalInsufficient := 0, 0, 0
	for _, tid := range tenantIDs {
		posted, skipped, insufficient, err := runTenant(ctx, dbPool, fees, postingClient, tid, now, logger)
		if err != nil {
			logger.Error("run tenant", "tenant_id", tid, "err", err)
			continue
		}
		totalPosted += posted
		totalSkipped += skipped
		totalInsufficient += insufficient
	}
	logger.Info("recurring-fee-charger: pass complete",
		"posted", totalPosted, "skipped", totalSkipped, "insufficient_funds", totalInsufficient)
	return nil
}

func runTenant(ctx context.Context, dbPool *db.Pool, fees *store.RecurringFeeStore, postingClient *posting.Client, tenantID uuid.UUID, now time.Time, logger *slog.Logger) (int, int, int, error) {
	posted, skipped, insufficient := 0, 0, 0
	var feeDefs []store.RecurringFee
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		l, err := fees.ListActiveAllTx(ctx, tx)
		if err != nil {
			return err
		}
		feeDefs = l
		return nil
	}); err != nil {
		return 0, 0, 0, err
	}

	for _, def := range feeDefs {
		period := store.PeriodLabel(def.Frequency, now)
		// Scan accounts on the product. Run each account's charge in
		// its own tx so a single failure (or row-level lock conflict)
		// doesn't roll back the whole sweep.
		type acctRow struct {
			ID               uuid.UUID
			AccountNo        string
			AvailableBalance string
		}
		var accts []acctRow
		if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT id, account_no, available_balance::text
				  FROM deposit_accounts
				 WHERE product_id = $1 AND status = 'active'
			`, def.ProductID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var a acctRow
				if err := rows.Scan(&a.ID, &a.AccountNo, &a.AvailableBalance); err != nil {
					return err
				}
				accts = append(accts, a)
			}
			return rows.Err()
		}); err != nil {
			logger.Error("scan accounts for fee", "fee_id", def.ID, "err", err)
			continue
		}

		for _, a := range accts {
			done, ins, err := chargeOne(ctx, dbPool, fees, postingClient, tenantID, def, a.ID, a.AccountNo, a.AvailableBalance, period)
			if err != nil {
				logger.Warn("charge", "fee_id", def.ID, "account_id", a.ID, "err", err)
				continue
			}
			switch {
			case ins == false:
				skipped++
			case done == "posted":
				posted++
			case done == "insufficient_funds":
				insufficient++
			}
		}
	}
	return posted, skipped, insufficient, nil
}

func chargeOne(
	ctx context.Context,
	dbPool *db.Pool,
	fees *store.RecurringFeeStore,
	postingClient *posting.Client,
	tenantID uuid.UUID,
	def store.RecurringFee,
	accountID uuid.UUID,
	accountNo string,
	availableBalanceText string,
	period string,
) (status string, inserted bool, err error) {
	err = dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Insufficient funds branch: record row but no JE.
		var available_lt_amount bool
		_ = tx.QueryRow(ctx,
			`SELECT $1::numeric < $2::numeric`, availableBalanceText, def.Amount.String(),
		).Scan(&available_lt_amount)

		chargeStatus := "posted"
		if available_lt_amount {
			chargeStatus = "insufficient_funds"
		}
		ch, ins, rerr := fees.RecordChargeTx(ctx, tx, store.RecordRecurringFeeChargeInput{
			TenantID:        tenantID,
			AccountID:       accountID,
			FeeDefinitionID: def.ID,
			PeriodLabel:     period,
			Amount:          def.Amount,
			Status:          chargeStatus,
		})
		if rerr != nil {
			return rerr
		}
		inserted = ins
		status = chargeStatus
		if !ins || chargeStatus != "posted" || postingClient == nil {
			return nil
		}
		// Look up the product's liability code; for simplicity use
		// fee_catalog mapping or fall back to 2100 (FOSA liability).
		// In a full impl, mirror postingops.resolveLiabilityAcct.
		var liabAcct string
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(NULLIF(liability_gl_code, ''), '2100')
			  FROM deposit_products WHERE id = $1
		`, def.ProductID).Scan(&liabAcct)
		if liabAcct == "" {
			liabAcct = "2100"
		}

		if err := postingClient.PostTx(ctx, tx, posting.PostInput{
			TenantID:     tenantID,
			EntryDate:    time.Now(),
			SourceModule: "savings.recurring_fee",
			SourceRef:    ch.ID.String(),
			Narration:    fmt.Sprintf("Recurring %s fee for %s (%s)", def.FeeKind, accountNo, period),
			Lines: []posting.Line{
				{AccountCode: liabAcct, Debit: def.Amount, Narration: "Fee debit (" + accountNo + ")"},
				{AccountCode: def.GLCreditCode, Credit: def.Amount, Narration: "Recurring fee income"},
			},
		}); err != nil {
			return fmt.Errorf("post fee je: %w", err)
		}
		return nil
	})
	return
}
