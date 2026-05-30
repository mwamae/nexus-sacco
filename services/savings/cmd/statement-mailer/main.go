// statement-mailer — daily worker that fires quarterly (or per-tenant
// cron) member statements via email.
//
// Each tick:
//   1. For every tenant, parse tenant_operations.statement_mail_cron.
//   2. If the previous fire of that cron falls into [last-tick, now],
//      this tenant is due — fan out to its members.
//   3. For each member with statement_email = true who hasn't already
//      received this period, render deposits + shares + interest +
//      dividend statements and queue an email with all four PDFs as
//      attachments. Insert a row in statement_mailings for idempotency.
//
// CLI flags:
//   --once      run one tick + exit (cron / test)
//   --tenant    limit to one tenant slug
//   --period    override period_label (default 'YYYY-Qn' from today)

package main

import (
	"context"
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
	"github.com/robfig/cron/v3"

	"github.com/nexussacco/savings/internal/db"
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
	tickInterval    = 1 * time.Hour
	perTenantBudget = 5 * time.Minute
)

func main() {
	once := flag.Bool("once", false, "run one tick + exit")
	tenantFlag := flag.String("tenant", "", "limit to one tenant slug (optional)")
	periodFlag := flag.String("period", "", "override period_label (default 'YYYY-Qn')")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("statement-mailer: DATABASE_URL required")
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

	notif := notifier.New(
		envOr("NOTIFICATION_SERVICE_URL", "http://localhost:8085"),
		os.Getenv("NOTIFICATION_INTERNAL_TOKEN"),
		logger,
	)
	statementsStore := store.NewStatementsStore(dbPool.Pool)

	if !*once {
		go healthx.RunHeartbeatLoop(ctx, dbPool.Pool, "statement-mailer", workerVersion(), 30*time.Second, nil, logger)
	}

	var lastTick time.Time
	for {
		now := time.Now().UTC()
		if err := runOneTick(ctx, dbPool, statementsStore, notif, logger, *tenantFlag, *periodFlag, lastTick, now); err != nil && ctx.Err() == nil {
			logger.Error("tick failed", "err", err)
		}
		lastTick = now
		if *once {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(tickInterval):
		}
	}
}

func runOneTick(
	ctx context.Context, pool *db.Pool, statements *store.StatementsStore,
	notif *notifier.Client, logger *slog.Logger,
	tenantSlugFilter string, periodOverride string,
	prevTick, now time.Time,
) error {
	tenants, err := listTenants(ctx, pool.Pool, tenantSlugFilter)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}
	if len(tenants) == 0 {
		return nil
	}
	logger.Info("statement-mailer tick", "tenants", len(tenants))
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	for _, t := range tenants {
		tctx, tcancel := context.WithTimeout(ctx, perTenantBudget)
		stats, err := processTenant(tctx, pool, statements, notif, logger, parser, t, prevTick, now, periodOverride)
		tcancel()
		if err != nil {
			logger.Error("tenant pass failed", "tenant", t.Slug, "err", err)
			continue
		}
		if stats.queued > 0 || stats.skipped > 0 {
			logger.Info("tenant pass", "tenant", t.Slug, "queued", stats.queued, "skipped", stats.skipped, "errors", stats.errors)
		}
	}
	return nil
}

type tenantRow struct {
	ID   uuid.UUID
	Slug string
	Name string
}

func listTenants(ctx context.Context, pool *pgxpool.Pool, filter string) ([]tenantRow, error) {
	q := `SELECT id, slug, name FROM tenants WHERE slug <> 'platform'`
	var args []any
	if filter != "" {
		q += ` AND slug = $1`
		args = append(args, filter)
	}
	q += ` ORDER BY slug`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenantRow
	for rows.Next() {
		var t tenantRow
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type tenantStats struct{ queued, skipped, errors int }

func processTenant(
	ctx context.Context, pool *db.Pool, statements *store.StatementsStore,
	notif *notifier.Client, logger *slog.Logger,
	parser cron.Parser,
	t tenantRow, prevTick, now time.Time, periodOverride string,
) (tenantStats, error) {
	var stats tenantStats

	err := pool.WithTenantTx(ctx, t.ID, func(tx pgx.Tx) error {
		// Load schedule.
		var cronExpr string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(statement_mail_cron, '0 6 1 */3 *')
			  FROM tenant_operations LIMIT 1
		`).Scan(&cronExpr); err != nil {
			return fmt.Errorf("load cron: %w", err)
		}
		sched, err := parser.Parse(cronExpr)
		if err != nil {
			logger.Warn("invalid cron, skipping tenant", "tenant", t.Slug, "cron", cronExpr, "err", err)
			return nil
		}
		// Only fire when the cron's next-from-prev lands inside (prev, now].
		// On the very first tick (prevTick zero), use now-tickInterval as
		// a safe back-window.
		if prevTick.IsZero() {
			prevTick = now.Add(-tickInterval)
		}
		nextFire := sched.Next(prevTick)
		if nextFire.After(now) {
			return nil // not due
		}
		periodLabel := periodOverride
		if periodLabel == "" {
			periodLabel = quarterLabel(now)
		}
		logger.Info("tenant due for statement mail", "tenant", t.Slug, "period", periodLabel)

		// Members with statement_email=true who don't yet have a row for
		// this period.
		dueRows, err := tx.Query(ctx, `
			SELECT cd.counterparty_id,
			       COALESCE(cd.full_name, ''),
			       COALESCE(cd.member_no, ''),
			       m.id,
			       m.email::text
			  FROM member_notification_preferences p
			  JOIN counterparty_directory cd ON cd.counterparty_id = p.counterparty_id
			  JOIN members m ON m.id = cd.member_id
			 WHERE p.statement_email = true
			   AND m.email IS NOT NULL AND m.email <> ''
			   AND NOT EXISTS (
			     SELECT 1 FROM statement_mailings sm
			      WHERE sm.tenant_id = current_tenant_id()
			        AND sm.counterparty_id = cd.counterparty_id
			        AND sm.period_label = $1
			   )
		`, periodLabel)
		if err != nil {
			return err
		}
		type due struct {
			CounterpartyID, MemberID uuid.UUID
			Name, MemberNo, Email   string
		}
		var batch []due
		for dueRows.Next() {
			var d due
			if err := dueRows.Scan(&d.CounterpartyID, &d.Name, &d.MemberNo, &d.MemberID, &d.Email); err != nil {
				dueRows.Close()
				return err
			}
			batch = append(batch, d)
		}
		dueRows.Close()
		if err := dueRows.Err(); err != nil {
			return err
		}

		fy := currentFYLabel(now)
		from, to := lastQuarterRange(now)
		for _, d := range batch {
			specs, mailErr := buildAttachmentsTx(ctx, tx, statements, t, d.CounterpartyID, d.MemberID, d.Name, d.MemberNo, from, to, fy, periodLabel)
			if mailErr != nil {
				stats.errors++
				logger.Error("build attachments", "tenant", t.Slug, "member", d.MemberNo, "err", mailErr)
				continue
			}
			if len(specs) == 0 {
				stats.skipped++
				continue
			}
			// Queue the email via notifier. Best-effort — notifier.Notify
			// is fire-and-forget; the notification service handles retry +
			// delivery audit.
			email := d.Email
			subject := fmt.Sprintf("%s — your %s statements", t.Name, periodLabel)
			body := fmt.Sprintf("Dear %s,\n\nPlease find attached your %s statements (deposits, shares, interest, dividend where applicable).\n\nKind regards,\n%s",
				d.Name, periodLabel, t.Name)
			notif.Notify(ctx, notifier.Request{
				TenantID:          t.ID,
				EventCode:         "MEMBER_STATEMENT_EMAIL",
				Channels:          []notifier.Channel{notifier.ChannelEmail},
				RecipientMemberID: &d.CounterpartyID,
				RecipientName:     d.Name,
				RecipientEmail:    &email,
				Payload: map[string]any{
					"subject": subject,
					"body":    body,
				},
				PDFAttachments: specs,
			})
			kinds := make([]string, 0, len(specs))
			for _, s := range specs {
				kinds = append(kinds, s.DocumentType)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO statement_mailings (
				  tenant_id, counterparty_id, period_label, email_address, statement_kinds
				) VALUES (current_tenant_id(), $1, $2, $3, $4)
				ON CONFLICT (tenant_id, counterparty_id, period_label) DO NOTHING
			`, d.CounterpartyID, periodLabel, d.Email, kinds); err != nil {
				stats.errors++
				logger.Error("mailing row insert", "tenant", t.Slug, "member", d.MemberNo, "err", err)
				continue
			}
			stats.queued++
		}
		return nil
	})
	return stats, err
}

// buildAttachmentsTx — assemble the 4 statements for one member, return
// PDFAttachmentSpec slice. Statements with no underlying data (e.g.
// dividend before any run is posted) are silently skipped.
func buildAttachmentsTx(
	ctx context.Context, tx pgx.Tx, statements *store.StatementsStore,
	t tenantRow, cpID, memberID uuid.UUID, name, memberNo string,
	from, to time.Time, fy, periodLabel string,
) ([]notifier.PDFAttachmentSpec, error) {
	common := store.StatementCommon{
		TenantName:       t.Name,
		TenantAddress:    "",
		TenantDisclaimer: "This statement is computer-generated and does not require a signature.",
		MemberName:       name,
		MemberNo:         memberNo,
		PeriodLabel:      periodLabel,
		GeneratedDate:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	}
	var specs []notifier.PDFAttachmentSpec

	// Deposits — quarter window.
	dep, err := statements.BuildDepositStatementTx(ctx, tx, common, cpID, nil, from, to)
	if err == nil && dep != nil {
		specs = append(specs, notifier.PDFAttachmentSpec{
			DocumentType:    "deposit_statement",
			Filename:        fmt.Sprintf("deposit-statement-%s-%s.pdf", memberNo, periodLabel),
			SubjectMemberID: &cpID,
			SubjectLabel:    name,
			Payload:         dep.ToPayload(),
		})
	}
	// Shares — FY window.
	shareFrom, shareTo, _ := parseFYWindow(fy)
	if !shareFrom.IsZero() {
		sh, err := statements.BuildShareStatementTx(ctx, tx, common, cpID, shareFrom, shareTo)
		if err == nil && sh != nil {
			specs = append(specs, notifier.PDFAttachmentSpec{
				DocumentType:    "share_statement",
				Filename:        fmt.Sprintf("share-statement-%s-%s.pdf", memberNo, fy),
				SubjectMemberID: &cpID,
				SubjectLabel:    name,
				Payload:         sh.ToPayload(),
			})
		}
	}
	// Interest — only when a run was posted for this FY.
	ir, err := statements.BuildInterestStatementTx(ctx, tx, common, memberID, fy)
	if err == nil && ir != nil && ir.RunNo != "" {
		specs = append(specs, notifier.PDFAttachmentSpec{
			DocumentType:    "interest_statement",
			Filename:        fmt.Sprintf("interest-statement-%s-%s.pdf", memberNo, fy),
			SubjectMemberID: &cpID,
			SubjectLabel:    name,
			Payload:         ir.ToPayload(),
		})
	}
	// Dividend — same.
	dv, err := statements.BuildDividendStatementTx(ctx, tx, common, memberID, fy)
	if err == nil && dv != nil && dv.RunNo != "" {
		specs = append(specs, notifier.PDFAttachmentSpec{
			DocumentType:    "dividend_statement",
			Filename:        fmt.Sprintf("dividend-statement-%s-%s.pdf", memberNo, fy),
			SubjectMemberID: &cpID,
			SubjectLabel:    name,
			Payload:         dv.ToPayload(),
		})
	}
	return specs, nil
}

// ─────────── helpers ───────────

func quarterLabel(t time.Time) string {
	q := (int(t.Month())-1)/3 + 1
	return fmt.Sprintf("%d-Q%d", t.Year(), q)
}

// lastQuarterRange — returns [from, to) for the quarter that just ended
// before `now` (i.e. when we send Q2 statements in July, this returns
// Apr 1 → Jul 1).
func lastQuarterRange(now time.Time) (time.Time, time.Time) {
	month := int(now.Month())
	quarterStart := month - ((month-1)%3) - 3 // 1, 4, 7, 10 minus 3
	year := now.Year()
	if quarterStart < 1 {
		quarterStart += 12
		year--
	}
	from := time.Date(year, time.Month(quarterStart), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 3, 0)
	return from, to
}

// currentFYLabel — July-to-June FY convention used by the rest of the codebase.
func currentFYLabel(now time.Time) string {
	if now.Month() >= time.July {
		return fmt.Sprintf("%d-%d", now.Year(), now.Year()+1)
	}
	return fmt.Sprintf("%d-%d", now.Year()-1, now.Year())
}

func parseFYWindow(fy string) (time.Time, time.Time, error) {
	fy = strings.TrimPrefix(strings.TrimPrefix(fy, "FY"), "-")
	parts := strings.Split(fy, "-")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, fmt.Errorf("bad fy: %q", fy)
	}
	var y1, y2 int
	fmt.Sscanf(parts[0], "%d", &y1)
	fmt.Sscanf(parts[1], "%d", &y2)
	if y2 != y1+1 {
		return time.Time{}, time.Time{}, fmt.Errorf("bad fy: %q", fy)
	}
	start := time.Date(y1, time.July, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(y2, time.July, 1, 0, 0, 0, 0, time.UTC)
	return start, end, nil
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
