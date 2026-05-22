// Counterparty co-creators — the post-Phase D primary write path.
//
// During Phase A/B these were "mirror writes" — secondary copies of
// rows whose source-of-truth lived in members / org_members. After
// the Phase D drop of the unified_counterparties feature flag the
// model inverts: counterparties is the register of record, and the
// matching members / org_members row lives only because the savings
// service still keys its FKs off members.id (until a deeper Phase D+
// promotes counterparty_id to load-bearing on those tables).
//
// Three call sites create both rows together inside one tx:
//   1. POST /v1/members (admin direct create) — MemberHandler.Create
//   2. POST /v1/orgs    (admin direct create) — OrgHandler.Create
//   3. Application approval — ApplicationHandler.Approve
// Each helper returns the new counterparty id so the caller can
// stamp it on whatever row needs the bridge (application.materialized_counterparty_id, etc.).

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

// createCounterpartyForMemberTx — creates a counterparty row that
// shadows the freshly-inserted members row + stamps the bridge
// FK on the members row. Returns the new counterparty id so the
// caller can carry it forward (e.g. for stamping on the matching
// application row).
func createCounterpartyForMemberTx(
	ctx context.Context,
	tx pgx.Tx,
	cps *store.CounterpartyStore,
	tenantID uuid.UUID,
	m *domain.Member,
	actorID uuid.UUID,
) (uuid.UUID, error) {
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
		return uuid.Nil, fmt.Errorf("counterparty insert: %w", err)
	}
	if err := cps.SetCounterpartyOnMemberTx(ctx, tx, m.ID, cp.ID); err != nil {
		return uuid.Nil, err
	}
	return cp.ID, nil
}

// createCounterpartyForOrgTx — same shape, the org side. Returns the
// new counterparty id so the caller can carry it forward.
func createCounterpartyForOrgTx(
	ctx context.Context,
	tx pgx.Tx,
	cps *store.CounterpartyStore,
	tenantID uuid.UUID,
	o *domain.Org,
	actorID uuid.UUID,
) (uuid.UUID, error) {
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
		return uuid.Nil, fmt.Errorf("counterparty insert: %w", err)
	}
	if err := cps.SetCounterpartyOnOrgTx(ctx, tx, o.ID, cp.ID); err != nil {
		return uuid.Nil, err
	}
	return cp.ID, nil
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
