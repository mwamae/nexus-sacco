// Counterparty mirror writers — keep counterparties in sync with the
// legacy members + org_members tables during Phase A/B.
//
// Two end-user paths create rows in `members`:
//   1. POST /v1/members (admin direct create) — MemberHandler.Create
//   2. Application approval — ApplicationHandler.Approve
// Both call the matching mirror function below inside the same tx as
// the legacy insert, so the bridge counterparty_id is stamped before
// commit. The existing application-side mirror in application.go
// already calls into the CounterpartyStore directly; this file
// centralises the shape so the two paths emit identical rows.

package handler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/store"
)

// mirrorMemberCreateToCounterpartyTx mirrors a freshly-created
// `members` row into counterparties. Caller is responsible for the
// tx + tenant scoping; the function only emits one CounterpartyStore
// create + one bridge UPDATE.
func mirrorMemberCreateToCounterpartyTx(
	ctx context.Context,
	tx pgx.Tx,
	cps *store.CounterpartyStore,
	tenantID uuid.UUID,
	m *domain.Member,
	actorID uuid.UUID,
) error {
	individual, _ := json.Marshal(map[string]any{
		"gender":            m.Gender,
		"date_of_birth":     m.DateOfBirth,
		"id_doc_kind":       m.IDDocKind,
		"id_doc_number":     m.IDDocNumber,
		"kra_pin":           m.KraPIN,
		"employment_status": m.EmploymentStatus,
		"employer":          m.Employer,
		"payroll_no":        m.PayrollNo,
		"job_title":         m.JobTitle,
	})
	contact, _ := json.Marshal(map[string]any{
		"phone":            m.Phone,
		"email":            m.Email,
		"county":           m.County,
		"sub_county":       m.SubCounty,
		"physical_address": m.PhysicalAddress,
	})
	legacy := m.MemberNo
	cp, err := cps.CreateTx(ctx, tx, store.CreateInput{
		TenantID:    tenantID,
		LegacyID:    &legacy,
		Kind:        domain.CounterpartyIndividual,
		DisplayName: m.FullName,
		Status:      mapMemberStatusToCP(m.Status),
		KYCState:    domain.CPKYCNotStarted,
		RiskBand:    domain.CPRiskNA,
		Individual:  individual,
		Contact:     contact,
		CreatedBy:   ptrIfSet(actorID),
	})
	if err != nil {
		return fmt.Errorf("counterparty insert: %w", err)
	}
	return cps.SetCounterpartyOnMemberTx(ctx, tx, m.ID, cp.ID)
}

// mirrorOrgCreateToCounterpartyTx — same shape, the org side.
func mirrorOrgCreateToCounterpartyTx(
	ctx context.Context,
	tx pgx.Tx,
	cps *store.CounterpartyStore,
	tenantID uuid.UUID,
	o *domain.Org,
	actorID uuid.UUID,
) error {
	institution, _ := json.Marshal(map[string]any{
		"legacy_org_kind":      string(o.Kind),
		"registration_no":      o.RegistrationNo,
		"date_of_registration": o.DateOfRegistration,
		"date_of_operation":    o.DateOfOperation,
		"industry":             o.Industry,
		"nature_of_business":   o.NatureOfBusiness,
		"member_count":         o.MemberCount,
		"employee_count":       o.EmployeeCount,
		// Officials / signatories / mandate / etc. live in their
		// own tables and aren't materialised here on create —
		// they're added via later PATCH calls. The Phase B detail
		// reads still hit /v1/orgs/:id for the rich shape.
		"needs_sync": true,
	})
	contact, _ := json.Marshal(map[string]any{
		"county":           o.County,
		"sub_county":       o.SubCounty,
		"physical_address": o.PhysicalAddress,
		"postal_address":   o.PostalAddress,
	})
	var tradingAs *string
	if o.TradingName != "" {
		t := o.TradingName
		tradingAs = &t
	}
	var regNo *string
	if o.RegistrationNo != "" {
		r := o.RegistrationNo
		regNo = &r
	}
	legacy := o.OrgNo
	cp, err := cps.CreateTx(ctx, tx, store.CreateInput{
		TenantID:       tenantID,
		LegacyID:       &legacy,
		Kind:           mapOrgKindToCP(o.Kind),
		DisplayName:    o.RegisteredName,
		TradingAs:      tradingAs,
		Status:         mapOrgStatusToCP(o.Status),
		KYCState:       mapOrgKYCToCP(o.KYCStatus),
		RiskBand:       mapOrgRiskToCP(o.RiskCategory),
		RegistrationNo: regNo,
		Institution:    institution,
		Contact:        contact,
		CreatedBy:      ptrIfSet(actorID),
	})
	if err != nil {
		return fmt.Errorf("counterparty insert: %w", err)
	}
	return cps.SetCounterpartyOnOrgTx(ctx, tx, o.ID, cp.ID)
}

// ─── enum mappers — identical to the SQL backfill in migration 0008 ───

func mapMemberStatusToCP(s domain.MemberStatus) domain.CounterpartyStatus {
	return domain.CounterpartyStatus(string(s)) // enums are 1:1
}

func mapOrgKindToCP(k domain.OrgKind) domain.CounterpartyKind {
	switch k {
	case "group", "chama":
		return domain.CounterpartyChama
	case "ltd", "sole_prop", "cooperative":
		return domain.CounterpartyCompany
	case "ngo":
		return domain.CounterpartyNGO
	case "church":
		return domain.CounterpartyChurch
	case "school":
		return domain.CounterpartySchool
	default:
		return domain.CounterpartyOther
	}
}

func mapOrgStatusToCP(s domain.OrgStatus) domain.CounterpartyStatus {
	if string(s) == "closed" {
		return domain.CPStatusExited
	}
	return domain.CounterpartyStatus(string(s))
}

func mapOrgKYCToCP(k domain.KYCReviewStatus) domain.CounterpartyKYCState {
	return domain.CounterpartyKYCState(string(k))
}

func mapOrgRiskToCP(r domain.RiskCategory) domain.CounterpartyRiskBand {
	switch r {
	case domain.RiskLow:
		return domain.CPRiskLow
	case domain.RiskMedium:
		return domain.CPRiskMedium
	case domain.RiskHigh:
		return domain.CPRiskHigh
	default:
		return domain.CPRiskNA
	}
}

func ptrIfSet(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}
