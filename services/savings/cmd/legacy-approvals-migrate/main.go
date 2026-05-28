// legacy-approvals-migrate — one-shot backfill that promotes every
// in-flight pending_approvals row to a wf_instance.
//
// # Purpose
//
// The unified-approvals migration PR moved every cash kind off the
// legacy savings/pending_approvals table onto the workflow engine.
// That move is forward-only — new approvals queue under workflow —
// but a fleet of tenants will have unfinished pending_approvals rows
// at the moment of deploy. This script runs once at first boot and
// migrates each one into its matching wf_instance so approvers see
// it in the Inbox immediately instead of stranded on a soon-to-be-
// retired surface.
//
// Operational shape
//
//	default (no flag)   dry-run; reports counts per tenant and per
//	                    kind without mutating anything
//	--apply             perform the backfill; mark each migrated
//	                    pending_approvals.status = 'migrated' and
//	                    stamp wf_instances.legacy_pending_approval_id
//	--tenant-slug=X     limit the run to one tenant (skips others)
//
// The script is idempotent: rerunning skips rows already marked
// 'migrated'. The forward query is `status = 'pending'`, so a row
// flipped to migrated on a previous run drops out of the candidate
// set.
//
// # Mapping
//
// processKindForApprovalKind handles the 5 enum-vs-process_kind name
// drifts (deposit→cash_deposit, withdrawal→cash_withdrawal,
// deposit_transfer→cash_account_transfer, share_bonus→share_bonus_issue,
// loan_writeoff→loan_write_off). The remaining 14 kinds map 1:1.
//
// Each wf_instance is created via workflowclient.CreateInstanceTx —
// the same path savings handlers use natively — so the snapshot of
// levels + the callback_url + the maker attribution are identical to
// a freshly-queued instance. The dispatcher will fire the matching
// wf_callbacks/<kind>.go on terminal transition exactly as it would
// for a new instance.
//
// What this script does NOT do
//
//   - Touch wf_instances that already exist (idempotency).
//   - Migrate non-pending pending_approvals rows. approved / declined
//     / cancelled / execution_error are terminal in the legacy table;
//     promoting them would create a duplicate audit trail without
//     value. The legacy rows stay queryable for historical
//     reconciliation under their existing statuses.
//   - Touch the per-tenant approval_* toggles. P5's migration moves
//     those onto the wf_definition active flag — the backfill just
//     handles the in-flight rows.
//
// Run sequence at first deploy
//
//  1. Apply both new migrations (workflow 0012, savings 0035).
//  2. Restart workflow + savings.
//  3. Start the callback-dispatcher.
//  4. ./legacy-approvals-migrate           # dry-run report
//  5. ./legacy-approvals-migrate --apply
//  6. Confirm: SELECT count(*) FROM pending_approvals WHERE status='pending';
//     Expect 0. Any remaining rows are kinds the mapping doesn't
//     cover (none today; this is a sanity check for future enum
//     additions).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/handler"
	"github.com/nexussacco/savings/internal/workflowclient"
)

// legacyRow mirrors the columns the backfill reads. Decoupled from
// domain.PendingApproval so a future field add on the domain struct
// can't accidentally break the migration.
type legacyRow struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	Kind             string
	Title            string
	SubjectMemberID  *uuid.UUID
	SubjectAccountID *uuid.UUID
	SubjectLoanID    *uuid.UUID
	Amount           *decimal.Decimal
	Payload          json.RawMessage
	MakerUserID      uuid.UUID
	CreatedAt        time.Time
}

type tenantRow struct {
	ID   uuid.UUID
	Slug string
}

type kindReport struct {
	candidates int
	migrated   int
	skipped    int // mapping unknown OR no active wf_definition
	failed     int
}

type tenantReport struct {
	slug   string
	total  int
	byKind map[string]*kindReport
}

func main() {
	apply := flag.Bool("apply", false,
		"perform the backfill (default is dry-run + report only)")
	tenantSlug := flag.String("tenant-slug", "",
		"limit the run to one tenant by slug (default: every tenant)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("legacy-approvals-migrate: DATABASE_URL is required")
		os.Exit(1)
	}
	selfURL := os.Getenv("SAVINGS_SELF_URL")
	if selfURL == "" {
		selfURL = "http://localhost:8084"
		logger.Warn("legacy-approvals-migrate: SAVINGS_SELF_URL not set; defaulting to localhost — the callback dispatcher will POST here for terminal events on migrated instances. Set explicitly in prod.")
	}

	ctx := context.Background()
	pool, err := db.New(ctx, dsn)
	if err != nil {
		logger.Error("connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	wfClient := workflowclient.New()

	tenants, err := listTenants(ctx, pool, *tenantSlug)
	if err != nil {
		logger.Error("list tenants", "err", err)
		os.Exit(1)
	}
	if len(tenants) == 0 {
		logger.Warn("no tenants matched", "slug", *tenantSlug)
		return
	}

	logger.Info("legacy-approvals-migrate: starting",
		"mode", modeStr(*apply), "tenants", len(tenants), "callback_url_base", selfURL)

	reports := []tenantReport{}
	totalCandidates, totalMigrated, totalSkipped, totalFailed := 0, 0, 0, 0

	for _, t := range tenants {
		report, err := migrateTenant(ctx, pool, wfClient, t, selfURL, *apply, logger)
		if err != nil {
			logger.Error("tenant migration failed", "slug", t.Slug, "err", err)
			os.Exit(1)
		}
		reports = append(reports, report)
		for _, k := range report.byKind {
			totalCandidates += k.candidates
			totalMigrated += k.migrated
			totalSkipped += k.skipped
			totalFailed += k.failed
		}
	}

	printReport(reports, *apply)
	logger.Info("legacy-approvals-migrate: complete",
		"mode", modeStr(*apply),
		"candidates", totalCandidates,
		"migrated", totalMigrated,
		"skipped", totalSkipped,
		"failed", totalFailed,
	)
	if totalFailed > 0 {
		os.Exit(2)
	}
}

func modeStr(apply bool) string {
	if apply {
		return "APPLY"
	}
	return "DRY-RUN"
}

func listTenants(ctx context.Context, pool *db.Pool, onlySlug string) ([]tenantRow, error) {
	q := `SELECT id, slug FROM tenants WHERE slug <> 'platform'`
	args := []any{}
	if onlySlug != "" {
		q += ` AND slug = $1`
		args = append(args, onlySlug)
	}
	q += ` ORDER BY slug`
	rows, err := pool.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenantRow
	for rows.Next() {
		var t tenantRow
		if err := rows.Scan(&t.ID, &t.Slug); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// migrateTenant runs the full scan for one tenant inside a single
// WithTenantTx so RLS scoping holds. Each row gets its own
// CreateInstanceTx + UPDATE pair inside that tx; a single row
// failure rolls back JUST that row's writes by virtue of the
// per-row tx — see processRow below.
func migrateTenant(
	ctx context.Context, pool *db.Pool, wf *workflowclient.Client,
	t tenantRow, selfURL string, apply bool, logger *slog.Logger,
) (tenantReport, error) {
	report := tenantReport{slug: t.Slug, byKind: map[string]*kindReport{}}

	// Read the candidate set in one query (no per-row I/O for the
	// scan). RLS requires the tenant tx; we open a read tx for the
	// scan and close it before per-row writes.
	var candidates []legacyRow
	err := pool.WithTenantTx(ctx, t.ID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, kind, title,
			       subject_member_id, subject_account_id, subject_loan_id,
			       amount, payload, maker_user_id, created_at
			  FROM pending_approvals
			 WHERE status = 'pending'
			 ORDER BY created_at
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r legacyRow
			if err := rows.Scan(&r.ID, &r.TenantID, &r.Kind, &r.Title,
				&r.SubjectMemberID, &r.SubjectAccountID, &r.SubjectLoanID,
				&r.Amount, &r.Payload, &r.MakerUserID, &r.CreatedAt,
			); err != nil {
				return err
			}
			candidates = append(candidates, r)
		}
		return rows.Err()
	})
	if err != nil {
		return report, err
	}
	report.total = len(candidates)
	if len(candidates) == 0 {
		return report, nil
	}

	for _, r := range candidates {
		k := report.byKind[r.Kind]
		if k == nil {
			k = &kindReport{}
			report.byKind[r.Kind] = k
		}
		k.candidates++

		processKind := handler.ProcessKindForApprovalKind(domain.ApprovalKind(r.Kind))
		if processKind == "" {
			k.skipped++
			logger.Warn("skip: no process_kind mapping for legacy kind",
				"tenant", t.Slug, "pa_id", r.ID, "kind", r.Kind,
				"hint", "add a case to processKindForApprovalKind if this kind needs migrating")
			continue
		}

		if !apply {
			// Dry-run — just count whether the active definition exists.
			ok, err := definitionExists(ctx, pool, t.ID, processKind)
			if err != nil {
				k.failed++
				logger.Error("definition check failed", "tenant", t.Slug, "pa_id", r.ID, "err", err)
				continue
			}
			if !ok {
				k.skipped++
				logger.Warn("skip (dry-run): no active wf_definition",
					"tenant", t.Slug, "kind", r.Kind, "process_kind", processKind)
				continue
			}
			k.migrated++ // dry-run "would-migrate" count
			continue
		}

		// Apply path — per-row tx so one failing row doesn't roll back
		// every prior insert.
		migrated, err := processRow(ctx, pool, wf, t, r, processKind, selfURL)
		if err != nil {
			k.failed++
			logger.Error("migrate row failed",
				"tenant", t.Slug, "pa_id", r.ID, "kind", r.Kind, "err", err)
			continue
		}
		if !migrated {
			k.skipped++
			continue
		}
		k.migrated++
		logger.Info("migrated", "tenant", t.Slug, "kind", r.Kind, "pa_id", r.ID)
	}

	return report, nil
}

// processRow creates the wf_instance for one legacy row and flips
// the row to 'migrated'. Both writes happen in the same tx so an
// instance-create failure leaves the legacy row in pending — a
// re-run picks it up.
//
// Returns (migrated=true, nil) on success. Returns (false, nil) when
// no active wf_definition exists for the row's process_kind (the
// caller logs + counts; we don't error so the run continues).
func processRow(
	ctx context.Context, pool *db.Pool, wf *workflowclient.Client,
	t tenantRow, r legacyRow, processKind string, selfURL string,
) (bool, error) {
	migrated := false
	err := pool.WithTenantTx(ctx, t.ID, func(tx pgx.Tx) error {
		// Re-check inside the tx — a concurrent run by another
		// operator could have already migrated this row.
		var stillPending bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pending_approvals WHERE id = $1 AND status = 'pending')
		`, r.ID).Scan(&stillPending); err != nil {
			return err
		}
		if !stillPending {
			return nil
		}

		// Build the context map. Payload is the original request body
		// (jsonb in the legacy table) — embed under "payload" to match
		// what natively-queued wf instances ship.
		var payloadAny any
		if len(r.Payload) > 0 {
			if err := json.Unmarshal(r.Payload, &payloadAny); err != nil {
				// Treat malformed JSON as a hard error — the executor
				// would fail to decode it anyway. The operator can
				// inspect + manually decline the legacy row.
				return fmt.Errorf("payload is not valid JSON: %w", err)
			}
		}

		// Resolve the wf SubjectID — the legacy table has three
		// possible subject pointers; pick the one that matches what
		// the in-tx Approval handler would have set today.
		subjectID := subjectIDFromLegacy(r)
		if subjectID == uuid.Nil {
			return fmt.Errorf("no subject id on legacy row (member/account/loan all nil)")
		}

		instanceID, err := wf.CreateInstanceTx(ctx, tx, workflowclient.CreateInstanceInput{
			TenantID:    t.ID,
			ProcessKind: processKind,
			SubjectKind: handler.SubjectKindFor(domain.ApprovalKind(r.Kind)),
			SubjectID:   subjectID,
			Context:     map[string]any{"payload": payloadAny},
			Summary:     r.Title,
			SourceURL:   handler.SourceURLFor(domain.ApprovalKind(r.Kind), subjectID),
			CallbackURL: selfURL + "/internal/v1/workflow-terminal-action",
			MakerUserID: r.MakerUserID,
		})
		if errors.Is(err, workflowclient.ErrDefinitionNotFound) {
			// No active definition on this tenant — leave the legacy
			// row alone; an operator decides whether to seed the
			// definition or decline the row.
			return nil
		}
		if err != nil {
			return fmt.Errorf("create wf_instance: %w", err)
		}

		// Stamp the back-pointer + flip the legacy row to migrated.
		// Both in the same tx — partial commit would orphan the
		// legacy pending row pointing nowhere.
		if _, err := tx.Exec(ctx, `
			UPDATE wf_instances SET legacy_pending_approval_id = $1 WHERE id = $2
		`, r.ID, instanceID); err != nil {
			return fmt.Errorf("stamp legacy backref: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE pending_approvals SET status = 'migrated' WHERE id = $1
		`, r.ID); err != nil {
			return fmt.Errorf("mark legacy as migrated: %w", err)
		}
		migrated = true
		return nil
	})
	return migrated, err
}

func subjectIDFromLegacy(r legacyRow) uuid.UUID {
	// Priority: account → loan → member → tenant-wide (for kinds
	// like share_bonus_issue that have no per-row subject).
	switch {
	case r.SubjectAccountID != nil:
		return *r.SubjectAccountID
	case r.SubjectLoanID != nil:
		return *r.SubjectLoanID
	case r.SubjectMemberID != nil:
		return *r.SubjectMemberID
	default:
		return r.TenantID
	}
}

func definitionExists(ctx context.Context, pool *db.Pool, tenantID uuid.UUID, processKind string) (bool, error) {
	var exists bool
	err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM wf_definitions
				 WHERE tenant_id = $1 AND process_kind = $2 AND active = true
			)
		`, tenantID, processKind).Scan(&exists)
	})
	return exists, err
}

func printReport(reports []tenantReport, apply bool) {
	header := "DRY-RUN"
	if apply {
		header = "APPLY"
	}
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────────────────────────┐")
	fmt.Printf("│  legacy-approvals-migrate report (%s)\n", header)
	fmt.Println("├─────────────────────────────────────────────────────────────────────┤")
	for _, r := range reports {
		fmt.Printf("│  %-30s  total=%d\n", r.slug, r.total)
		// Sort kinds for stable output.
		kinds := make([]string, 0, len(r.byKind))
		for k := range r.byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			kr := r.byKind[k]
			label := "migrated"
			if !apply {
				label = "would-migrate"
			}
			fmt.Printf("│    %-26s  candidates=%d  %s=%d  skipped=%d  failed=%d\n",
				k, kr.candidates, label, kr.migrated, kr.skipped, kr.failed)
		}
	}
	fmt.Println("└─────────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}
