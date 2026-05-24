// One-shot converter: every pending savings.pending_approvals row
// gets a matching wf_instance so the Unified Inbox can decide it.
//
// Idempotent — picks rows WHERE workflow_instance_id IS NULL, so a
// second run only processes anything created since the last pass.
// Safe to schedule on a periodic cron if we ever want
// "auto-onboard new pending_approvals to the inbox" behaviour.
//
// USAGE
//
//   go run ./cmd/migrate-cash-approvals \
//     [-tenant <slug>]    # default: all tenants
//     [-dry-run]
//
// When -dry-run is set, no rows are inserted; the tool just prints
// the per-kind census + the mappings it would apply.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/workflow/internal/domain"
	"github.com/nexussacco/workflow/internal/jsonlogic"
)

// kindMapping covers every savings pending_approvals.kind that has a
// corresponding seeded workflow process_kind. Anything not listed
// here is logged + skipped so the operator can decide whether to
// seed a new process_kind or hand-resolve it.
var kindMapping = map[string]string{
	"deposit":                  "cash_deposit",
	"withdrawal":               "cash_withdrawal",
	"deposit_transfer":         "cash_account_transfer",
	"share_purchase":           "share_purchase",
	"share_transfer":           "share_transfer",
	"share_bonus":              "share_bonus_issue",
	"loan_disbursement":        "loan_disbursement",
	"loan_writeoff":            "loan_write_off",
	"loan_reschedule":          "loan_reschedule",
	"loan_moratorium":          "loan_moratorium",
	"loan_settlement_discount": "loan_settlement_discount",
}

func main() {
	dryRun := flag.Bool("dry-run", false, "print would-be inserts without writing")
	tenantSlug := flag.String("tenant", "", "only convert pending_approvals for this tenant (default: all)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		logger.Error("DATABASE_URL not set")
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("dial", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Resolve tenant scope.
	tenants, err := loadTenants(ctx, pool, *tenantSlug)
	if err != nil {
		logger.Error("load tenants", "err", err)
		os.Exit(1)
	}
	if len(tenants) == 0 {
		logger.Warn("no tenants matched")
		return
	}

	stats := stats{}
	for _, t := range tenants {
		if err := runForTenant(ctx, pool, logger, t, *dryRun, &stats); err != nil {
			logger.Error("tenant run failed", "tenant", t.slug, "err", err)
			os.Exit(1)
		}
	}

	logger.Info("done",
		"converted", stats.converted,
		"skipped_unmapped_kind", stats.skippedUnmapped,
		"skipped_no_definition", stats.skippedNoDef,
		"dry_run", *dryRun,
	)
	if len(stats.unmappedKinds) > 0 {
		logger.Warn("unmapped kinds (seed a process_kind for these to convert)",
			"kinds", stats.unmappedKinds)
	}
}

type tenant struct {
	id   uuid.UUID
	slug string
}

type stats struct {
	converted       int
	skippedUnmapped int
	skippedNoDef    int
	unmappedKinds   []string
}

func loadTenants(ctx context.Context, pool *pgxpool.Pool, slug string) ([]tenant, error) {
	q := `SELECT id, slug FROM tenants`
	args := []any{}
	if slug != "" {
		q += ` WHERE slug = $1`
		args = append(args, slug)
	}
	q += ` ORDER BY slug`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenant
	for rows.Next() {
		var t tenant
		if err := rows.Scan(&t.id, &t.slug); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type pendingRow struct {
	id              uuid.UUID
	kind            string
	title           string
	subjectMemberID *uuid.UUID
	amount          *float64
	payload         []byte
	makerUserID     uuid.UUID
}

func runForTenant(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, t tenant, dryRun bool, st *stats) error {
	// Open one tx so the RLS GUC + every read/write stays consistent.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if dryRun {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, t.id.String()); err != nil {
		return fmt.Errorf("set tenant: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id, kind, title, subject_member_id, amount, payload, maker_user_id
		  FROM pending_approvals
		 WHERE tenant_id = $1
		   AND status = 'pending'
		   AND workflow_instance_id IS NULL
		 ORDER BY created_at
	`, t.id)
	if err != nil {
		return err
	}
	var pending []pendingRow
	for rows.Next() {
		var p pendingRow
		if err := rows.Scan(&p.id, &p.kind, &p.title, &p.subjectMemberID, &p.amount, &p.payload, &p.makerUserID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if len(pending) == 0 {
		logger.Info("no pending rows to convert", "tenant", t.slug)
		if !dryRun {
			return tx.Commit(ctx)
		}
		return nil
	}
	logger.Info("converting", "tenant", t.slug, "n", len(pending))

	for _, p := range pending {
		processKind, ok := kindMapping[p.kind]
		if !ok {
			st.skippedUnmapped++
			st.unmappedKinds = appendUnique(st.unmappedKinds, p.kind)
			logger.Warn("skipping unmapped kind",
				"pending_approval_id", p.id, "kind", p.kind, "tenant", t.slug)
			continue
		}
		// Resolve the active wf_definition for this (tenant, kind).
		var defID uuid.UUID
		var defVersion int
		err := tx.QueryRow(ctx, `
			SELECT id, version FROM wf_definitions
			 WHERE tenant_id = $1 AND process_kind = $2 AND active = true
			 LIMIT 1
		`, t.id, processKind).Scan(&defID, &defVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				st.skippedNoDef++
				logger.Warn("no active definition; skipping",
					"tenant", t.slug, "kind", p.kind, "process_kind", processKind, "pending_approval_id", p.id)
				continue
			}
			return err
		}

		// Build the context object the engine evaluates conditions
		// against + the Inbox renders. Strict superset of pending_approvals
		// columns + the original payload nested under "payload".
		ctxObj := map[string]any{
			"legacy_pending_approval_id": p.id.String(),
			"legacy_kind":                p.kind,
			"title":                      p.title,
		}
		if p.amount != nil {
			ctxObj["amount"] = *p.amount
		}
		if p.subjectMemberID != nil {
			ctxObj["counterparty_id"] = p.subjectMemberID.String()
		}
		var payloadObj any
		if len(p.payload) > 0 {
			_ = json.Unmarshal(p.payload, &payloadObj)
		}
		if payloadObj != nil {
			ctxObj["payload"] = payloadObj
		}

		// Snapshot the levels with conditions evaluated against ctxObj.
		levels, startLevel, startStatus, err := snapshotLevelsTx(ctx, tx, defID, ctxObj)
		if err != nil {
			return err
		}
		levelsJSON, _ := json.Marshal(levels)
		ctxJSON, _ := json.Marshal(ctxObj)
		var breachAt *time.Time
		if startLevel >= 0 && startLevel < len(levels) && levels[startLevel].SLADueAt != nil {
			breachAt = levels[startLevel].SLADueAt
		}

		// Subject ID for the workflow instance is the pending_approvals
		// row id itself — that's the canonical handle for cash txns.
		var instID uuid.UUID
		if dryRun {
			logger.Info("[dry-run] would convert",
				"pending_approval_id", p.id, "kind", p.kind, "process_kind", processKind,
				"levels", len(levels), "start_level", startLevel, "start_status", startStatus)
			st.converted++
			continue
		}
		summary := fmt.Sprintf("%s — %s", processKind, p.title)
		err = tx.QueryRow(ctx, `
			INSERT INTO wf_instances (
			  tenant_id, definition_id, process_kind, subject_kind, subject_id,
			  status, current_level, context, callback_url,
			  initiator_id, levels, summary, source_url, sla_breach_at
			) VALUES (
			  $1, $2, $3, 'cash_txn', $4,
			  $5, $6, $7, NULL,
			  $8, $9, $10, $11, $12
			)
			RETURNING id
		`,
			t.id, defID, processKind, p.id,
			startStatus, maxInt(startLevel, 0), ctxJSON,
			p.makerUserID, levelsJSON, summary,
			fmt.Sprintf("/cash-approvals/%s", p.id), breachAt,
		).Scan(&instID)
		if err != nil {
			return fmt.Errorf("insert wf_instance for pa=%s: %w", p.id, err)
		}
		// Audit row so the Inbox shows the migration as an event in the
		// instance's history (rather than appearing as if the user
		// created it now).
		if _, err := tx.Exec(ctx, `
			INSERT INTO wf_actions (instance_id, tenant_id, action, actor_id, comments, metadata)
			VALUES ($1, $2, 'create', $3, 'migrated from legacy pending_approvals queue', $4)
		`, instID, t.id, p.makerUserID,
			[]byte(fmt.Sprintf(`{"legacy_pending_approval_id":"%s","definition_id":"%s","definition_version":%d}`,
				p.id, defID, defVersion))); err != nil {
			return err
		}
		// Back-link the savings row.
		if _, err := tx.Exec(ctx, `
			UPDATE pending_approvals SET workflow_instance_id = $1 WHERE id = $2
		`, instID, p.id); err != nil {
			return err
		}
		st.converted++
	}

	if dryRun {
		return nil
	}
	return tx.Commit(ctx)
}

// snapshotLevelsTx loads wf_levels for the definition and produces the
// LevelState array the engine stores on wf_instances.levels — with
// conditions evaluated against ctxObj up front (same algorithm the
// engine uses at instance creation).
func snapshotLevelsTx(ctx context.Context, tx pgx.Tx, defID uuid.UUID, ctxObj map[string]any) ([]domain.LevelState, int, domain.Status, error) {
	rows, err := tx.Query(ctx, `
		SELECT level_order, name, approver_roles, approver_user_ids, quorum::text,
		       condition_expr, sla_hours, COALESCE(escalation_role,''), escalation_user_id
		  FROM wf_levels
		 WHERE definition_id = $1
		 ORDER BY level_order
	`, defID)
	if err != nil {
		return nil, -1, "", err
	}
	defer rows.Close()
	var levels []domain.LevelState
	for rows.Next() {
		var ls domain.LevelState
		var roles []string
		var users []uuid.UUID
		var quorumStr string
		var condRaw []byte
		var slaHours *int
		var escUser *uuid.UUID
		if err := rows.Scan(&ls.Order, &ls.Name, &roles, &users, &quorumStr,
			&condRaw, &slaHours, &ls.EscalationRole, &escUser); err != nil {
			return nil, -1, "", err
		}
		ls.ApproverRoles = roles
		for _, u := range users {
			ls.ApproverUserIDs = append(ls.ApproverUserIDs, u.String())
		}
		ls.Quorum = domain.Quorum(quorumStr)
		ls.Status = domain.LvlWaiting
		ls.SLAHours = slaHours
		if escUser != nil {
			ls.EscalationUser = escUser.String()
		}
		if len(condRaw) > 0 {
			var cond any
			if err := json.Unmarshal(condRaw, &cond); err != nil {
				return nil, -1, "", err
			}
			ls.Condition = cond
			if cond != nil {
				ok, err := jsonlogic.Eval(cond, ctxObj)
				if err != nil {
					return nil, -1, "", fmt.Errorf("level %d: condition: %w", ls.Order, err)
				}
				if !ok {
					ls.Status = domain.LvlSkipped
				}
			}
		}
		levels = append(levels, ls)
	}
	// First non-skipped level → starting in_progress; if all skipped,
	// instance auto-approves.
	start := -1
	for i := range levels {
		if levels[i].Status != domain.LvlSkipped {
			start = i
			break
		}
	}
	if start == -1 {
		return levels, -1, domain.StatusApproved, nil
	}
	now := time.Now().UTC()
	levels[start].Status = domain.LvlInProgress
	levels[start].EnteredAt = &now
	if levels[start].SLAHours != nil {
		due := now.Add(time.Duration(*levels[start].SLAHours) * time.Hour)
		levels[start].SLADueAt = &due
	}
	return levels, start, domain.StatusInProgress, nil
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func _silence_strings() string { return strings.TrimSpace("") }
