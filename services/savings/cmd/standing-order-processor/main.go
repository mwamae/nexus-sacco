// Standing-order processor (DSID Phase 2.2).
//
// Polls recurring_deposits where status='active' and next_run_at <= now,
// dispatches each row to its source-specific handler, records a run
// row, applies retry+suspend policy per tenant_operations, and SMSes
// the member on failure / suspend.
//
// Sources (one branch each):
//   manual_reminder — SMS the member a reminder + mark 'skipped' + advance
//   payroll         — look up matching checkoff_batches; if none, fail
//                     with error_code=payroll_unmatched. Posting itself
//                     is handled by the existing check-off post path.
//   mpesa_pull      — POST /v1/mpesa/stk/push on the mpesa service. On
//                     synchronous accept, mark 'sent' + advance; the
//                     STK callback later lands a deposit through the
//                     existing distribution waterfall.
//   fosa_debit      — in-tx deposit transfer (FOSA → BOSA). Insufficient
//                     funds → fail with error_code=insufficient_funds.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/postingops"
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

const (
	pollInterval = 5 * time.Minute
	dueBatch     = 200
)

type tenantPolicy struct {
	MaxRetries           int
	RetryBackoffHours    int
	SuspendAfterFailures int
	NotifyOnFailure      bool
	NotifyOnSuspend      bool
}

func loadTenantPolicy(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (tenantPolicy, error) {
	p := tenantPolicy{MaxRetries: 3, RetryBackoffHours: 24, SuspendAfterFailures: 3, NotifyOnFailure: true, NotifyOnSuspend: true}
	err := tx.QueryRow(ctx, `
		SELECT standing_order_max_retries,
		       standing_order_retry_backoff_hours,
		       standing_order_suspend_after_failures,
		       standing_order_notify_on_failure,
		       standing_order_notify_on_suspend
		  FROM tenant_operations WHERE tenant_id = $1
	`, tenantID).Scan(&p.MaxRetries, &p.RetryBackoffHours, &p.SuspendAfterFailures, &p.NotifyOnFailure, &p.NotifyOnSuspend)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	return p, err
}

func main() {
	once := flag.Bool("once", false, "drain one batch and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db: connect", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	rdStore := store.NewRecurringDepositStore(dbPool.Pool)
	depStore := store.NewDepositStore(dbPool.Pool)

	notifyClient := notifier.New(cfg.NotificationURL, cfg.NotificationInternalToken, logger)
	postingClient, _ := posting.New(cfg.AccountingURL, cfg.AccountingInternalToken, logger)

	mpesaURL := strings.TrimRight(cfg.MpesaURL, "/")
	mpesaToken := cfg.MpesaInternalToken

	if !*once {
		go healthx.RunHeartbeatLoop(
			ctx, dbPool.Pool, "standing-order-processor", workerVersion(),
			30*time.Second, nil, logger,
		)
	}

	logger.Info("standing-order-processor: starting",
		"poll_interval", pollInterval, "once", *once)

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		if err := drain(ctx, dbPool, rdStore, depStore, postingClient, notifyClient, mpesaURL, mpesaToken, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("drain loop", "err", err)
		}
		if *once {
			logger.Info("standing-order-processor: --once supplied, exiting")
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("standing-order-processor: shutting down")
			return
		case <-tick.C:
		}
	}
}

// drain pulls every distinct tenant with at least one due row, then
// per tenant runs the dispatch loop inside that tenant's tx.
func drain(
	ctx context.Context,
	dbPool *db.Pool,
	rd *store.RecurringDepositStore,
	dep *store.DepositStore,
	postingClient *posting.Client,
	notify *notifier.Client,
	mpesaURL, mpesaToken string,
	logger *slog.Logger,
) error {
	// 1. Find tenants with due rows. Bypasses tenant context so we can
	// scan across tenants in one shot.
	tenantRows, err := dbPool.Pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		  FROM recurring_deposits
		 WHERE status = 'active' AND next_run_at <= now()
		 LIMIT 200
	`)
	if err != nil {
		return fmt.Errorf("scan due tenants: %w", err)
	}
	var tenantIDs []uuid.UUID
	for tenantRows.Next() {
		var t uuid.UUID
		if err := tenantRows.Scan(&t); err == nil {
			tenantIDs = append(tenantIDs, t)
		}
	}
	tenantRows.Close()

	for _, tid := range tenantIDs {
		if err := drainTenant(ctx, dbPool, rd, dep, postingClient, notify, mpesaURL, mpesaToken, tid, logger); err != nil {
			logger.Error("drain tenant", "tenant_id", tid, "err", err)
		}
	}
	return nil
}

func drainTenant(
	ctx context.Context,
	dbPool *db.Pool,
	rd *store.RecurringDepositStore,
	dep *store.DepositStore,
	postingClient *posting.Client,
	notify *notifier.Client,
	mpesaURL, mpesaToken string,
	tenantID uuid.UUID,
	logger *slog.Logger,
) error {
	var policy tenantPolicy
	var due []store.RecurringDeposit
	if err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		p, perr := loadTenantPolicy(ctx, tx, tenantID)
		if perr != nil {
			return perr
		}
		policy = p
		d, derr := rd.DueTx(ctx, tx, dueBatch)
		if derr != nil {
			return derr
		}
		due = d
		return nil
	}); err != nil {
		return err
	}

	for _, row := range due {
		// Each row gets its own tx so a failure in one doesn't roll
		// back the others.
		if err := processOne(ctx, dbPool, rd, dep, postingClient, notify, mpesaURL, mpesaToken, row, policy, logger); err != nil {
			logger.Error("process standing order", "id", row.ID, "err", err)
		}
	}
	return nil
}

func processOne(
	ctx context.Context,
	dbPool *db.Pool,
	rd *store.RecurringDepositStore,
	dep *store.DepositStore,
	postingClient *posting.Client,
	notify *notifier.Client,
	mpesaURL, mpesaToken string,
	row store.RecurringDeposit,
	policy tenantPolicy,
	logger *slog.Logger,
) error {
	period := row.NextRunAt.UTC().Format("2006-01-02")

	return dbPool.WithTenantTx(ctx, row.TenantID, func(tx pgx.Tx) error {
		lastAttempt, err := rd.LastAttemptForPeriodTx(ctx, tx, row.ID, period)
		if err != nil {
			return err
		}
		attemptNo := lastAttempt + 1
		if attemptNo > policy.MaxRetries+1 {
			// Already exhausted retries for this period — flip suspended.
			return suspend(ctx, tx, rd, notify, row, "max retries exceeded", policy)
		}

		var runStatus, errCode, errMsg string
		var postedTxnID *uuid.UUID

		switch row.Source {
		case "manual_reminder":
			runStatus = "skipped"
			fireReminderSMS(ctx, notify, row)
			// Advance immediately — reminders aren't expected to "post".
			if _, err := rd.AdvanceNextRunTx(ctx, tx, row.ID, row.Frequency, time.Now()); err != nil {
				return err
			}
			_, err = rd.RecordRunTx(ctx, tx, store.RecordRunInput{
				TenantID:        row.TenantID,
				StandingOrderID: row.ID,
				Amount:          row.Amount,
				AttemptNo:       attemptNo,
				PeriodLabel:     period,
				Status:          runStatus,
			})
			return err

		case "payroll":
			matched := false
			if row.SourcePayrollEmployer != nil {
				_ = tx.QueryRow(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM checkoff_batches
						 WHERE tenant_id = $1
						   AND employer_code = $2
						   AND posted_at IS NOT NULL
						   AND posted_at >= $3 - interval '7 days'
					)
				`, row.TenantID, *row.SourcePayrollEmployer, row.NextRunAt).Scan(&matched)
			}
			if matched {
				runStatus = "success"
				if _, err := rd.AdvanceNextRunTx(ctx, tx, row.ID, row.Frequency, time.Now()); err != nil {
					return err
				}
			} else {
				runStatus = "failed"
				errCode = "payroll_unmatched"
				errMsg = "no posted check-off batch for the payroll period"
			}

		case "mpesa_pull":
			ok, mpErr := initiateSTK(ctx, mpesaURL, mpesaToken, row)
			if ok {
				runStatus = "success"
				if _, err := rd.AdvanceNextRunTx(ctx, tx, row.ID, row.Frequency, time.Now()); err != nil {
					return err
				}
			} else {
				runStatus = "failed"
				errCode = "mpesa_initiate_failed"
				if mpErr != nil {
					errMsg = mpErr.Error()
				}
			}

		case "fosa_debit":
			postedID, ferr := executeFosaDebit(ctx, tx, dep, postingClient, row)
			if ferr == nil {
				runStatus = "success"
				postedTxnID = &postedID
				if _, err := rd.AdvanceNextRunTx(ctx, tx, row.ID, row.Frequency, time.Now()); err != nil {
					return err
				}
			} else {
				runStatus = "failed"
				if errors.Is(ferr, errInsufficientFunds) {
					errCode = "insufficient_funds"
				} else {
					errCode = "fosa_debit_failed"
				}
				errMsg = ferr.Error()
			}

		default:
			runStatus = "failed"
			errCode = "unknown_source"
			errMsg = "unknown source: " + row.Source
		}

		var nextRetry *time.Time
		if runStatus == "failed" {
			t := time.Now().Add(time.Duration(policy.RetryBackoffHours) * time.Hour * time.Duration(attemptNo))
			nextRetry = &t
			_, suspended, err := rd.MarkFailureTx(ctx, tx, store.FailureOpts{
				ID:                     row.ID,
				NextRetryAt:            t,
				SuspendAfterFailures:   policy.SuspendAfterFailures,
				ReasonNotesIfSuspended: errCode + ": " + errMsg,
			})
			if err != nil {
				return err
			}
			if suspended {
				fireSuspendSMS(ctx, notify, row, policy)
			} else if policy.NotifyOnFailure {
				fireFailureSMS(ctx, notify, row, errMsg, t)
			}
		}

		_, err = rd.RecordRunTx(ctx, tx, store.RecordRunInput{
			TenantID:        row.TenantID,
			StandingOrderID: row.ID,
			Amount:          row.Amount,
			AttemptNo:       attemptNo,
			PeriodLabel:     period,
			Status:          runStatus,
			ErrorCode:       errCode,
			ErrorMessage:    errMsg,
			PostedTxnID:     postedTxnID,
			NextRetryAt:     nextRetry,
		})
		return err
	})
}

var errInsufficientFunds = errors.New("insufficient funds")

func executeFosaDebit(ctx context.Context, tx pgx.Tx, dep *store.DepositStore, postingClient *posting.Client, row store.RecurringDeposit) (uuid.UUID, error) {
	if row.SourceAccountID == nil {
		return uuid.Nil, errors.New("source_account_id is required for fosa_debit")
	}
	// Load both accounts inside the tx (FOR UPDATE happens inside PostTxnTx).
	srcAcct, err := loadAccount(ctx, tx, *row.SourceAccountID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load source: %w", err)
	}
	tgtAcct, err := loadAccount(ctx, tx, row.TargetAccountID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load target: %w", err)
	}
	if srcAcct.AvailableBalance.LessThan(row.Amount) {
		return uuid.Nil, errInsufficientFunds
	}

	channel := domain.DepChannelStandingOrder
	narration := "Standing order transfer to " + tgtAcct.AccountNo

	// Debit source.
	srcTxn, err := dep.PostTxnTx(ctx, tx, store.PostDepInput{
		Account:             srcAcct,
		TxnType:             domain.TxnDepTransferOut,
		Amount:              row.Amount,
		Channel:             &channel,
		Narration:           &narration,
		CounterpartyAccount: tgtAcct,
		InitiatedBy:         row.CreatedBy,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("post debit: %w", err)
	}
	// Credit target.
	creditNar := "Standing order transfer from " + srcAcct.AccountNo
	tgtTxn, err := dep.PostTxnTx(ctx, tx, store.PostDepInput{
		Account:             tgtAcct,
		TxnType:             domain.TxnDepTransferIn,
		Amount:              row.Amount,
		Channel:             &channel,
		Narration:           &creditNar,
		CounterpartyAccount: srcAcct,
		CounterpartyTxnID:   &srcTxn.ID,
		InitiatedBy:         row.CreatedBy,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("post credit: %w", err)
	}
	// JE.
	if postingClient != nil {
		if err := postingops.PostDepositTransferTx(ctx, tx, postingops.Deps{Posting: postingClient}, postingops.DepositTransferInput{
			TenantID:      row.TenantID,
			FromTxnID:     srcTxn.ID,
			ToTxnID:       tgtTxn.ID,
			Amount:        row.Amount,
			FromAccountNo: srcAcct.AccountNo,
			ToAccountNo:   tgtAcct.AccountNo,
			FromProductID: srcAcct.ProductID,
			ToProductID:   tgtAcct.ProductID,
		}); err != nil {
			return uuid.Nil, fmt.Errorf("post je: %w", err)
		}
	}
	return tgtTxn.ID, nil
}

func loadAccount(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.DepositAccount, error) {
	var a domain.DepositAccount
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, counterparty_id, product_id, account_no, status::text,
		       current_balance, available_balance
		  FROM deposit_accounts WHERE id = $1
	`, id).Scan(
		&a.ID, &a.TenantID, &a.CounterpartyID, &a.ProductID, &a.AccountNo, &a.Status,
		&a.CurrentBalance, &a.AvailableBalance,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// initiateSTK fires the STK Push via the mpesa service. Returns (ok, err).
func initiateSTK(ctx context.Context, mpesaURL, token string, row store.RecurringDeposit) (bool, error) {
	if mpesaURL == "" || token == "" {
		return false, errors.New("mpesa service not configured (MPESA_SERVICE_URL / MPESA_INTERNAL_TOKEN)")
	}
	msisdn := ""
	if row.SourceMSISDN != nil {
		msisdn = *row.SourceMSISDN
	}
	if msisdn == "" {
		return false, errors.New("source_msisdn is empty (no member fallback wired)")
	}
	// We need a paybill_id — pulled from tenant_operations.default_mpesa_paybill_id.
	// Until that's added, this fails with a clear error so the operator
	// notices and ramp-up of mpesa_pull stays gated.
	body := map[string]any{
		"paybill_id":        nil, // operator-configured per tenant in a follow-up
		"msisdn":            msisdn,
		"amount":            row.Amount.StringFixed(2),
		"account_reference": row.TargetAccountID.String()[:8],
		"transaction_desc":  "Standing order",
		"source_module":     "savings.standing_order",
		"source_ref":        row.ID.String(),
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		mpesaURL+"/v1/mpesa/stk/push", bytes.NewReader(buf))
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Internal-Token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("mpesa stk/push status %d", resp.StatusCode)
	}
	return true, nil
}

// ─────────── notification helpers (fire-and-forget) ───────────

func fireReminderSMS(ctx context.Context, n *notifier.Client, row store.RecurringDeposit) {
	if n == nil {
		return
	}
	msisdn := ""
	if row.SourceMSISDN != nil {
		msisdn = *row.SourceMSISDN
	}
	cp := row.CounterpartyID
	n.Notify(ctx, notifier.Request{
		TenantID:          row.TenantID,
		EventCode:         "MEMBER_STANDING_ORDER_REMINDER",
		Channels:          []notifier.Channel{notifier.ChannelSMS},
		RecipientMemberID: &cp,
		RecipientPhone:    nilIfEmpty(msisdn),
		Payload: map[string]any{
			"amount":    row.Amount.StringFixed(2),
			"due_date":  row.NextRunAt.UTC().Format("2006-01-02"),
			"frequency": row.Frequency,
		},
	})
}

func fireFailureSMS(ctx context.Context, n *notifier.Client, row store.RecurringDeposit, reason string, nextRetry time.Time) {
	if n == nil {
		return
	}
	cp := row.CounterpartyID
	n.Notify(ctx, notifier.Request{
		TenantID:          row.TenantID,
		EventCode:         "MEMBER_STANDING_ORDER_FAILED",
		Channels:          []notifier.Channel{notifier.ChannelSMS},
		RecipientMemberID: &cp,
		Payload: map[string]any{
			"amount":     row.Amount.StringFixed(2),
			"reason":     reason,
			"next_retry": nextRetry.UTC().Format(time.RFC3339),
		},
	})
}

func fireSuspendSMS(ctx context.Context, n *notifier.Client, row store.RecurringDeposit, policy tenantPolicy) {
	if n == nil {
		return
	}
	cp := row.CounterpartyID
	n.Notify(ctx, notifier.Request{
		TenantID:          row.TenantID,
		EventCode:         "MEMBER_STANDING_ORDER_SUSPENDED",
		Channels:          []notifier.Channel{notifier.ChannelSMS, notifier.ChannelInApp},
		RecipientMemberID: &cp,
		Payload: map[string]any{
			"amount":               row.Amount.StringFixed(2),
			"consecutive_failures": policy.SuspendAfterFailures,
		},
	})
}

func suspend(ctx context.Context, tx pgx.Tx, rd *store.RecurringDepositStore, n *notifier.Client, row store.RecurringDeposit, reason string, policy tenantPolicy) error {
	susp := "suspended"
	_, err := rd.UpdateTx(ctx, tx, store.UpdateRecurringDepositInput{
		ID:          row.ID,
		Status:      &susp,
		ReasonNotes: &reason,
	})
	if err != nil {
		return err
	}
	fireSuspendSMS(ctx, n, row, policy)
	return nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// keep decimal import alive for future per-row math helpers
var _ = decimal.Zero
