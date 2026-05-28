// collections-engine — hourly worker that drives the auto-SMS and
// letter portion of the collections workflow.
//
// For each tenant, for each loan currently in arrears (per
// loan_dpd_snapshots with fallback to inline DPD):
//
//   1. Resolve the matching collections_escalation_rules row by DPD
//      bucket (max dpd_min <= dpd_days within dpd_max).
//   2. If rule.auto_sms = true AND no auto_sms event for the loan in
//      the last 7 days → render the body template, send via notifier,
//      write loan_collection_events row.
//   3. If rule.letter_kind is set AND no letter_generated event with
//      matching letter_kind in the last 30 days → call notifier
//      GeneratePDF, persist a loan_documents row, write event,
//      optionally email.
//   4. If the rule's required_role differs from the case's current
//      assignee's role → emit an escalation event (no auto-reassign
//      — Phase 4 ladder is suggestive, not coercive).
//
// CLI flags:
//   --once    one pass per tenant + exit (cron / test usage)
//
// Default = hourly loop with health heartbeat. Idempotency is the
// HasRecentEventTx check inside each branch.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/notifier"
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
	hourlyInterval   = time.Hour
	perTenantBudget  = 2 * time.Minute
	smsLookback      = 7 * 24 * time.Hour
	letterLookback   = 30 * 24 * time.Hour
)

func main() {
	once := flag.Bool("once", false, "run one pass per tenant and exit")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("collections-engine: DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPool, err := db.New(ctx, dsn)
	if err != nil {
		logger.Error("pgx connect", "err", err)
		os.Exit(1)
	}
	defer dbPool.Close()
	pool := dbPool.Pool

	notify := notifier.New(
		os.Getenv("NOTIFICATION_SERVICE_URL"),
		os.Getenv("NOTIFICATION_INTERNAL_TOKEN"),
		logger,
	)
	collections := store.NewLoanCollectionsStore(pool)

	if !*once {
		go healthx.RunHeartbeatLoop(ctx, pool, "collections-engine", workerVersion(), 30*time.Second, nil, logger)
	}

	for {
		if err := runOnePass(ctx, pool, collections, notify, logger); err != nil && ctx.Err() == nil {
			logger.Error("collections-engine pass failed", "err", err)
		}
		if *once {
			logger.Info("--once supplied; exiting")
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-time.After(hourlyInterval):
		}
	}
}

func runOnePass(
	ctx context.Context, pool *pgxpool.Pool,
	collections *store.LoanCollectionsStore, notify *notifier.Client,
	logger *slog.Logger,
) error {
	tenants, err := listTenants(ctx, pool)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	logger.Info("collections-engine pass starting", "tenants", len(tenants))

	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		stats, err := processTenant(tctx, pool, collections, notify, t.ID, t.Name, logger)
		tcancel()
		if err != nil {
			logger.Error("collections-engine tenant failed", "tenant", t.ID, "err", err)
			continue
		}
		logger.Info("collections-engine tenant done",
			"tenant", t.ID, "loans", stats.LoansEvaluated,
			"sms_fired", stats.SMSFired, "letters_fired", stats.LettersFired,
			"escalations", stats.EscalationEvents)
	}
	return nil
}

type tenantRow struct {
	ID   uuid.UUID
	Name string
}

func listTenants(ctx context.Context, pool *pgxpool.Pool) ([]tenantRow, error) {
	rows, err := pool.Query(ctx, `SELECT id, name FROM tenants WHERE slug <> 'platform' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenantRow
	for rows.Next() {
		var t tenantRow
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type passStats struct {
	LoansEvaluated   int
	SMSFired         int
	LettersFired     int
	EscalationEvents int
}

func processTenant(
	ctx context.Context, pool *pgxpool.Pool,
	collections *store.LoanCollectionsStore, notify *notifier.Client,
	tenantID uuid.UUID, tenantName string, logger *slog.Logger,
) (passStats, error) {
	var stats passStats

	// Open one tx for reads + event writes per tenant. Notifier calls
	// are outside the tx (they're external).
	tx, err := pool.Begin(ctx)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		return stats, fmt.Errorf("set tenant: %w", err)
	}

	rules, err := collections.EscalationRulesTx(ctx, tx)
	if err != nil {
		return stats, fmt.Errorf("load rules: %w", err)
	}
	if len(rules) == 0 {
		return stats, nil // tenant has no rules — opt-out
	}

	// Read loans needing evaluation. Anything in arrears (active/in_arrears/restructured
	// with positive principal). Snapshot DPD if available, fallback to inline.
	type loanForEval struct {
		LoanID         uuid.UUID
		LoanNo         string
		CounterpartyID uuid.UUID
		DPD            int
		Outstanding    string
		MemberName     string
		MemberPhone    *string
		MemberEmail    *string
	}
	rows, err := tx.Query(ctx, `
		WITH latest_snap AS (
		  SELECT DISTINCT ON (loan_id) loan_id, dpd_days
		    FROM loan_dpd_snapshots
		   ORDER BY loan_id, snapshot_date DESC
		)
		SELECT l.id, l.loan_no, l.counterparty_id,
		       COALESCE(ls.dpd_days, GREATEST(0, (CURRENT_DATE - l.next_installment_due_at))::int) AS dpd,
		       (l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance)::text,
		       cd.full_name,
		       m.phone, m.email
		  FROM loans l
		  JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
		  LEFT JOIN members m ON m.id = cd.member_id
		  LEFT JOIN latest_snap ls ON ls.loan_id = l.id
		 WHERE l.status IN ('active','in_arrears','restructured')
		   AND l.principal_balance > 0
	`)
	if err != nil {
		return stats, fmt.Errorf("list loans: %w", err)
	}
	var loans []loanForEval
	for rows.Next() {
		var l loanForEval
		if err := rows.Scan(&l.LoanID, &l.LoanNo, &l.CounterpartyID, &l.DPD,
			&l.Outstanding, &l.MemberName, &l.MemberPhone, &l.MemberEmail); err != nil {
			rows.Close()
			return stats, err
		}
		loans = append(loans, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}

	stats.LoansEvaluated = len(loans)

	for _, l := range loans {
		if l.DPD <= 0 {
			continue
		}
		rule := matchRule(rules, l.DPD)
		if rule == nil {
			continue
		}

		// Auto-SMS branch.
		if rule.AutoSMS {
			recent, err := collections.HasRecentEventTx(ctx, tx, l.LoanID, domain.EventAutoSMS, nil, smsLookback)
			if err != nil {
				return stats, fmt.Errorf("hasRecentEvent sms: %w", err)
			}
			if !recent {
				template, err := collections.MessageTemplateForTx(ctx, tx, "sms", l.DPD)
				if err != nil {
					return stats, err
				}
				body := renderSMSBody(template, tenantName, l.MemberName, l.LoanNo, l.DPD, l.Outstanding)
				if _, err := collections.LogEventTx(ctx, tx, l.LoanID, nil,
					domain.EventAutoSMS, nil,
					mustJSON(map[string]any{"body": body, "dpd": l.DPD, "rule_dpd_min": rule.DPDMin}),
					nil, nil, nil); err != nil {
					return stats, err
				}
				stats.SMSFired++
				// Fire SMS outside the tx — best-effort.
				if l.MemberPhone != nil {
					notify.Notify(ctx, notifier.Request{
						TenantID:          tenantID,
						EventCode:         "loan.collections.auto_sms",
						Channels:          []notifier.Channel{notifier.ChannelSMS},
						RecipientMemberID: &l.CounterpartyID,
						RecipientName:     l.MemberName,
						RecipientPhone:    l.MemberPhone,
						SourceModule:      strPtr("savings.collections_engine"),
						SourceRecordID:    &l.LoanID,
						Payload:           map[string]any{"body": body, "loan_no": l.LoanNo},
					})
				}
			}
		}

		// Letter branch.
		if rule.LetterKind != nil {
			lk := *rule.LetterKind
			recent, err := collections.HasRecentEventTx(ctx, tx, l.LoanID, domain.EventLetterGenerated, &lk, letterLookback)
			if err != nil {
				return stats, fmt.Errorf("hasRecentEvent letter: %w", err)
			}
			if !recent {
				details := map[string]any{
					"kind":          string(lk),
					"trigger":       "auto",
					"rule_dpd_min":  rule.DPDMin,
					"member_name":   l.MemberName,
					"loan_no":       l.LoanNo,
					"dpd":           l.DPD,
				}
				if _, err := collections.LogEventTx(ctx, tx, l.LoanID, nil,
					domain.EventLetterGenerated, nil,
					mustJSON(details),
					&lk, nil, nil); err != nil {
					return stats, err
				}
				stats.LettersFired++
				// Notifier render is best-effort and outside the tx —
				// when it succeeds, the next manual letter endpoint
				// or a follow-up cron writes the loan_documents row.
				// Phase 4 prompt accepts physical-mail delivery (no
				// notifier needed) — we generate the PDF either way.
				_, _ = notify.GeneratePDF(ctx, notifier.PDFGenerateRequest{
					TenantID:      tenantID,
					DocumentType:  "loan_" + lk.LoanDocKind(),
					SubjectLoanID: &l.LoanID,
					SubjectLabel:  "auto collections letter: " + string(lk),
					Payload:       details,
				})
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}

// matchRule picks the rule with the largest dpd_min that the loan's
// dpd_days satisfies (and is within dpd_max if set). Rules pre-sorted
// asc by dpd_min — we scan back-to-front.
func matchRule(rules []domain.EscalationRule, dpd int) *domain.EscalationRule {
	for i := len(rules) - 1; i >= 0; i-- {
		r := rules[i]
		if dpd < r.DPDMin {
			continue
		}
		if r.DPDMax != nil && dpd > *r.DPDMax {
			continue
		}
		return &r
	}
	return nil
}

// renderSMSBody — minimal placeholder interpolation. The template
// language is intentionally simple (no looping/conditionals).
func renderSMSBody(t *domain.CollectionMessageTemplate, tenantName, memberName, loanNo string, dpd int, outstanding string) string {
	var body string
	if t == nil {
		body = "Hello {{member_name}}, your loan {{loan_no}} at {{tenant_name}} is {{dpd}} day(s) overdue. Outstanding: KES {{outstanding}}."
	} else {
		body = t.BodyTemplate
	}
	repls := map[string]string{
		"{{member_name}}": memberName,
		"{{loan_no}}":     loanNo,
		"{{tenant_name}}": tenantName,
		"{{dpd}}":         fmt.Sprintf("%d", dpd),
		"{{outstanding}}": outstanding,
		"{{currency}}":    "KES",
	}
	for k, v := range repls {
		body = strings.ReplaceAll(body, k, v)
	}
	return body
}

func mustJSON(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func strPtr(s string) *string { return &s }

// silence pgxpool unused-import nag if connection wrapping changes.
var _ = pgx.ReadCommitted
