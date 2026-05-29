// Token issuance + SMS dispatch for guarantor consent.
//
// Centralised here so every place that adds a pending guarantee
// (the main loan-application Create, the top-up + refinance copy
// flow, and the admin "Resend invite" button) gets the same
// behaviour: a fresh token + a rendered SMS.
//
// The token row write is transactional with the guarantee insert;
// the SMS dispatch is best-effort (notifier.Notify never errors)
// so an SMS-send failure doesn't abort the application creation.

package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

// ConsentSettings carries the tenant-configurable knobs the SMS hook
// reads. Loaded once per request so we don't re-query tenant_operations
// per-guarantee.
type ConsentSettings struct {
	Enabled        bool
	Template       string
	ExpiryDays     int
	MaxOTPAttempts int
	PublicBaseURL  string
	TenantName     string
	TenantSlug     string
}

// LoadConsentSettingsTx fetches the tenant's consent-SMS settings.
// Caller is expected to run inside WithTenantTx so the tenant row
// is RLS-scoped.
func LoadConsentSettingsTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ConsentSettings, error) {
	var s ConsentSettings
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(o.guarantor_sms_enabled, true),
		  COALESCE(o.guarantor_sms_template, ''),
		  COALESCE(o.guarantor_token_expiry_days, 7),
		  COALESCE(o.guarantor_max_otp_attempts, 3),
		  COALESCE(o.guarantor_public_base_url, 'http://localhost:5173'),
		  t.name, t.slug
		  FROM tenants t
		  LEFT JOIN tenant_operations o ON o.tenant_id = t.id
		 WHERE t.id = $1
	`, tenantID).Scan(
		&s.Enabled, &s.Template, &s.ExpiryDays, &s.MaxOTPAttempts,
		&s.PublicBaseURL, &s.TenantName, &s.TenantSlug,
	)
	if err != nil {
		return nil, err
	}
	if s.Template == "" {
		s.Template = "Hi {{guarantor_name}}. {{applicant_name}} has requested you to guarantee a {{product_name}} of KES {{amount}}. To respond: {{link}} . Valid {{expiry_days}} days. Ref: {{token_short}}"
	}
	return &s, nil
}

// IssueConsentForGuarantee creates a fresh token + sends the SMS.
// guarateeID, applicantName, productName, amount must be known by
// the caller; the function loads the guarantor name/phone for the
// SMS recipient.
//
// Runs the token insert inside the supplied tx (durable with the
// guarantee row). Fires the SMS BEST-EFFORT via the notifier — the
// notifier swallows transport errors so an SMS outage doesn't roll
// back the application.
func IssueConsentForGuarantee(
	ctx context.Context, tx pgx.Tx,
	consent *store.GuarantorConsentStore, notif *notifier.Client, logger *slog.Logger,
	tenantID, guaranteeID, createdBy uuid.UUID,
	settings *ConsentSettings,
	applicantName, productName string, requestedAmount, amountGuaranteed decimal.Decimal,
	attemptNumber int,
) error {
	if !settings.Enabled {
		return nil
	}

	// Load the guarantor's contact info.
	var guarantorName, guarantorPhone string
	var guarantorCounterpartyID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT g.guarantor_counterparty_id,
		       COALESCE(cd.full_name, ''),
		       COALESCE(m.phone, '')
		  FROM loan_guarantees g
		  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = g.guarantor_counterparty_id
		  LEFT JOIN members m ON m.id = cd.member_id
		 WHERE g.id = $1
	`, guaranteeID).Scan(&guarantorCounterpartyID, &guarantorName, &guarantorPhone)
	if err != nil {
		return fmt.Errorf("load guarantor: %w", err)
	}

	// Generate + store the token.
	plaintext, hash, err := store.NewToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(time.Duration(settings.ExpiryDays) * 24 * time.Hour)
	tokenID, err := consent.CreateTx(ctx, tx, guaranteeID, hash, expiresAt, attemptNumber, createdBy)
	if err != nil {
		return err
	}
	_ = tokenID

	if guarantorPhone == "" {
		// No phone on file — record the token + skip SMS. The reminder
		// worker will retry once the SACCO updates the member's phone.
		logger.Warn("consent SMS skipped: guarantor has no phone on file",
			"guarantee_id", guaranteeID, "guarantor", guarantorName)
		return nil
	}

	// Build the URL the SMS body advertises. The slug-subdomain pattern
	// (`https://{slug}.nexussacco.local/...`) matches how the admin SPA
	// is already served per-tenant; the public route /g/{token} renders
	// the consent page.
	publicBase := strings.TrimRight(settings.PublicBaseURL, "/")
	link := fmt.Sprintf("%s/g/%s", publicBase, plaintext)

	// Render the body using the tenant template.
	body := renderConsentTemplate(settings.Template, map[string]string{
		"guarantor_name":   guarantorName,
		"applicant_name":   applicantName,
		"product_name":     productName,
		"amount":           amountGuaranteed.StringFixed(2),
		"requested_amount": requestedAmount.StringFixed(2),
		"link":             link,
		"tenant_name":      settings.TenantName,
		"expiry_days":      fmt.Sprintf("%d", settings.ExpiryDays),
		"token_short":      store.ShortRef(plaintext),
	})

	// Fire the SMS via the notification service. notifier.Notify is
	// fire-and-forget (5s timeout, internal logging on failure) so
	// we don't propagate transport errors back into the tx.
	if notif != nil {
		notif.Notify(ctx, notifier.Request{
			TenantID:          tenantID,
			EventCode:         "GUARANTOR_CONSENT_REQUEST",
			Channels:          []notifier.Channel{notifier.ChannelSMS},
			RecipientMemberID: &guarantorCounterpartyID,
			RecipientName:     guarantorName,
			RecipientPhone:    &guarantorPhone,
			SourceModule:      strPtrConsent("savings.loan_guarantee"),
			SourceRecordID:    &guaranteeID,
			Payload:           map[string]any{"body": body},
			InitiatedBy:       &createdBy,
		})
	}

	return nil
}

// renderConsentTemplate performs minimal {{key}} substitution. We
// deliberately don't pull in a template engine — these SMS bodies
// are tiny, the placeholders are a fixed set, and external libs
// would bring escaping rules SMS doesn't need.
func renderConsentTemplate(tpl string, vars map[string]string) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

func strPtrConsent(s string) *string { return &s }
