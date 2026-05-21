// Stage 7 scheduled job handlers.
//
// Each handler is invoked once per tick per tenant. It runs inside a
// goroutine spawned by Scheduler.runOne — handlers should be safe to
// run concurrently across tenants.

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/store"
)

// LoanReminderConfig — JSON shape on scheduled_jobs.config.
type loanReminderConfig struct {
	DaysAhead []int `json:"days_ahead"`
}

// LoanRepaymentReminderHandler — fires LOAN_INSTALLMENT_DUE for each
// loan whose next_installment_due_at is in {today + N days} for each
// N in config.days_ahead. Default: [3, 1, 0] (3 days out, 1 day out,
// due today).
func LoanRepaymentReminderHandler(notifs *store.NotificationStore, templates *store.TemplateStore) JobHandler {
	return func(ctx context.Context, pool *db.Pool, tenantID uuid.UUID, job *domain.ScheduledJob) (int, int, error) {
		cfg := loanReminderConfig{DaysAhead: []int{3, 1, 0}}
		if len(job.Config) > 0 {
			_ = json.Unmarshal(job.Config, &cfg)
		}
		if len(cfg.DaysAhead) == 0 {
			cfg.DaysAhead = []int{3, 1, 0}
		}

		// Loans whose next installment falls in any of the requested
		// days-ahead, joined to the member for delivery details.
		type row struct {
			LoanID         uuid.UUID
			LoanNo         string
			MemberID       uuid.UUID
			MemberName     string
			Phone          *string
			Email          *string
			NextDueAt      time.Time
			NextAmount     decimal.Decimal
			DaysAhead      int
		}
		var rows []row
		err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			r, err := tx.Query(ctx, `
				SELECT l.id, l.loan_no, l.member_id, m.full_name, m.phone, m.email,
				       l.next_installment_due_at, l.next_installment_amount,
				       (DATE(l.next_installment_due_at) - CURRENT_DATE)::int AS days_ahead
				FROM loans l
				JOIN members m ON m.id = l.member_id
				WHERE l.status IN ('active', 'in_arrears', 'restructured')
				  AND l.next_installment_due_at IS NOT NULL
				  AND (DATE(l.next_installment_due_at) - CURRENT_DATE)::int = ANY($1::int[])
			`, cfg.DaysAhead)
			if err != nil {
				return err
			}
			defer r.Close()
			for r.Next() {
				var row row
				var nextAt *time.Time
				if err := r.Scan(
					&row.LoanID, &row.LoanNo, &row.MemberID, &row.MemberName, &row.Phone, &row.Email,
					&nextAt, &row.NextAmount, &row.DaysAhead,
				); err != nil {
					return err
				}
				if nextAt != nil {
					row.NextDueAt = *nextAt
				}
				rows = append(rows, row)
			}
			return r.Err()
		})
		if err != nil {
			return 0, 0, err
		}

		processed := 0
		failed := 0
		for _, r := range rows {
			payload := map[string]any{
				"loan_no":     r.LoanNo,
				"amount":      r.NextAmount.String(),
				"due_date":    r.NextDueAt.Format("2006-01-02"),
				"days_ahead":  r.DaysAhead,
				"full_name":   r.MemberName,
			}
			if err := dispatchOne(ctx, pool, notifs, templates,
				tenantID, "LOAN_INSTALLMENT_DUE",
				&r.MemberID, r.MemberName, r.Phone, r.Email,
				"savings.loans", &r.LoanID, payload,
			); err != nil {
				failed++
				continue
			}
			processed++
		}
		return processed, failed, nil
	}
}

// dormancyConfig — JSON shape on scheduled_jobs.config.
type dormancyConfig struct {
	WarningDays int `json:"warning_days"`
}

// DormancyWarningHandler — fires DORMANCY_WARNING for each member who
// last transacted before (today - warning_days). Sends one notification
// per matching member per run.
func DormancyWarningHandler(notifs *store.NotificationStore, templates *store.TemplateStore) JobHandler {
	return func(ctx context.Context, pool *db.Pool, tenantID uuid.UUID, job *domain.ScheduledJob) (int, int, error) {
		cfg := dormancyConfig{WarningDays: 90}
		if len(job.Config) > 0 {
			_ = json.Unmarshal(job.Config, &cfg)
		}
		if cfg.WarningDays <= 0 {
			cfg.WarningDays = 90
		}

		type row struct {
			MemberID   uuid.UUID
			MemberNo   string
			MemberName string
			Phone      *string
			Email      *string
			LastSeen   *time.Time
		}
		var rows []row
		err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			r, err := tx.Query(ctx, `
				SELECT m.id, m.member_no, m.full_name, m.phone, m.email,
				       m.last_activity_at
				FROM members m
				WHERE m.status = 'active'
				  AND (m.last_activity_at IS NULL OR m.last_activity_at < now() - ($1 || ' days')::interval)
			`, fmt.Sprintf("%d", cfg.WarningDays))
			if err != nil {
				return err
			}
			defer r.Close()
			for r.Next() {
				var row row
				if err := r.Scan(&row.MemberID, &row.MemberNo, &row.MemberName, &row.Phone, &row.Email, &row.LastSeen); err != nil {
					return err
				}
				rows = append(rows, row)
			}
			return r.Err()
		})
		if err != nil {
			return 0, 0, err
		}

		processed, failed := 0, 0
		for _, r := range rows {
			payload := map[string]any{
				"member_no":           r.MemberNo,
				"days_until_dormant":  cfg.WarningDays / 2, // rough — half the warning window
				"full_name":           r.MemberName,
			}
			if err := dispatchOne(ctx, pool, notifs, templates,
				tenantID, "DORMANCY_WARNING",
				&r.MemberID, r.MemberName, r.Phone, r.Email,
				"notification.scheduler.dormancy", &r.MemberID, payload,
			); err != nil {
				failed++
				continue
			}
			processed++
		}
		return processed, failed, nil
	}
}

// dispatchOne — shared helper used by every job handler that needs to
// fire a single notification. Resolves channel templates from the
// event's default_channels list.
func dispatchOne(
	ctx context.Context, pool *db.Pool,
	notifs *store.NotificationStore, templates *store.TemplateStore,
	tenantID uuid.UUID, eventCode string,
	recipientMemberID *uuid.UUID, recipientName string,
	phone, email *string,
	sourceModule string, sourceRecordID *uuid.UUID,
	payload map[string]any,
) error {
	return pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		notif, err := notifs.CreateTx(ctx, tx, store.CreateInput{
			EventCode:         eventCode,
			Priority:          domain.PriorityInfo,
			RecipientMemberID: recipientMemberID,
			RecipientName:     recipientName,
			RecipientPhone:    phone,
			RecipientEmail:    email,
			SourceModule:      &sourceModule,
			SourceRecordID:    sourceRecordID,
			Payload:           payload,
		})
		if err != nil {
			return err
		}
		// Render each channel listed on the event catalog. Falls back to
		// in_app + sms + email if we can't reach the catalog.
		// Stage 7 ships the simpler version: render whichever templates
		// exist (in_app, sms, email) and skip channels with no template.
		for _, ch := range []domain.Channel{domain.ChannelInApp, domain.ChannelSMS, domain.ChannelEmail} {
			// Skip SMS if no phone, email if no email — never queue
			// undeliverable rows.
			if ch == domain.ChannelSMS && phone == nil {
				continue
			}
			if ch == domain.ChannelEmail && email == nil {
				continue
			}
			tpl, err := templates.ActiveByEventChannelTx(ctx, tx, eventCode, ch)
			if err != nil {
				return err
			}
			if tpl == nil {
				continue
			}
			body := store.RenderTemplate(tpl.Body, payload)
			var subject *string
			if tpl.Subject != nil {
				rendered := store.RenderTemplate(*tpl.Subject, payload)
				subject = &rendered
			}
			tplID := tpl.ID
			if _, err := notifs.CreateDeliveryTx(ctx, tx, store.CreateDeliveryInput{
				NotificationID: notif.ID,
				Channel:        ch,
				TemplateID:     &tplID,
				Subject:        subject,
				Body:           body,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
